// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/schema"
)

// newPublicationIntegrationRepo connects, applies the embedded schema, and returns a
// repository over a clean newsletter_publications table.
func newPublicationIntegrationRepo(t *testing.T) *PostgresPublicationRepo {
	t.Helper()
	dsn := os.Getenv("NEWSLETTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWSLETTER_TEST_DATABASE_URL not set — skipping Postgres-backed publication repository tests")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Runtime guard: refuse to run unless the connected database positively
	// identifies as test-only. A mispointed env var (shared dev, staging, prod)
	// fails loudly here before any destructive statement runs.
	var dbName string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&dbName); err != nil {
		pool.Close()
		t.Fatalf("identify database: %v", err)
	}
	if !strings.Contains(dbName, "test") {
		pool.Close()
		t.Fatalf("refusing destructive tests against database %q — NEWSLETTER_TEST_DATABASE_URL must point at a dedicated test database whose name contains \"test\"", dbName)
	}

	if err := schema.Apply(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("apply schema: %v", err)
	}

	db := bun.NewDB(stdlib.OpenDBFromPool(pool), pgdialect.New())
	t.Cleanup(func() {
		_ = db.Close()
		pool.Close()
	})

	if _, err := db.ExecContext(ctx, "TRUNCATE newsletter_publications CASCADE"); err != nil {
		t.Fatalf("truncate newsletter_publications: %v", err)
	}
	return NewPostgresPublicationRepo(db)
}

// TestPublicationCreate tests basic creation of a publication.
func TestPublicationCreate(t *testing.T) {
	repo := newPublicationIntegrationRepo(t)

	pub := &model.NewsletterPublication{
		ProjectUID:     "project-1",
		Slug:           "test-pub",
		Name:           "Test Publication",
		WrapperContent: []byte("{}"),
		CreatedBy:      "test-user",
	}

	if err := repo.Create(context.Background(), pub); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if pub.ID == uuid.Nil {
		t.Error("Create did not populate ID")
	}
	if pub.Version != 1 {
		t.Errorf("Create version = %d, want 1", pub.Version)
	}
}

// TestPublicationGet tests fetching a publication by ID.
func TestPublicationGet(t *testing.T) {
	repo := newPublicationIntegrationRepo(t)

	pub := &model.NewsletterPublication{
		ProjectUID:     "project-1",
		Slug:           "test-pub",
		Name:           "Test Publication",
		WrapperContent: []byte("{}"),
		CreatedBy:      "test-user",
	}
	if err := repo.Create(context.Background(), pub); err != nil {
		t.Fatalf("Create: %v", err)
	}

	fetched, err := repo.Get(context.Background(), "project-1", pub.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if fetched.ID != pub.ID {
		t.Errorf("Get ID mismatch: got %s, want %s", fetched.ID, pub.ID)
	}
	if fetched.Name != pub.Name {
		t.Errorf("Get Name mismatch: got %s, want %s", fetched.Name, pub.Name)
	}
}

// TestPublicationGetWrongProject tests that Get respects project_uid.
func TestPublicationGetWrongProject(t *testing.T) {
	repo := newPublicationIntegrationRepo(t)

	pub := &model.NewsletterPublication{
		ProjectUID:     "project-1",
		Slug:           "test-pub",
		Name:           "Test Publication",
		WrapperContent: []byte("{}"),
		CreatedBy:      "test-user",
	}
	if err := repo.Create(context.Background(), pub); err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err := repo.Get(context.Background(), "project-2", pub.ID)
	if err == nil {
		t.Error("Get with wrong project should have returned error")
	}
	if err != domain.ErrNotFound {
		t.Errorf("Get wrong project error: got %v, want ErrNotFound", err)
	}
}

// TestPublicationList tests listing publications for a project.
func TestPublicationList(t *testing.T) {
	repo := newPublicationIntegrationRepo(t)

	pubs := []*model.NewsletterPublication{
		{ProjectUID: "project-1", Slug: "pub-1", Name: "Pub 1", WrapperContent: []byte("{}"), CreatedBy: "user"},
		{ProjectUID: "project-1", Slug: "pub-2", Name: "Pub 2", WrapperContent: []byte("{}"), CreatedBy: "user"},
		{ProjectUID: "project-2", Slug: "pub-3", Name: "Pub 3", WrapperContent: []byte("{}"), CreatedBy: "user"},
	}

	for _, pub := range pubs {
		if err := repo.Create(context.Background(), pub); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	page, err := repo.List(context.Background(), port.PublicationListFilters{ProjectUID: "project-1"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(page.Publications) != 2 {
		t.Errorf("List length: got %d, want 2", len(page.Publications))
	}
	if page.NextPageToken != "" {
		t.Errorf("NextPageToken should be empty when the page is not full, got %q", page.NextPageToken)
	}
}

// TestPublicationListPagination walks every page of a project's publications
// and proves the keyset cursor returns each row exactly once. A project's
// publication count is unbounded, so an unpaginated List would scan the whole
// table (including each row's wrapper_content JSONB).
func TestPublicationListPagination(t *testing.T) {
	repo := newPublicationIntegrationRepo(t)

	const total = 7
	const pageSize = 3
	for i := range total {
		pub := &model.NewsletterPublication{
			ProjectUID:     "project-page",
			Slug:           fmt.Sprintf("pub-%d", i),
			Name:           fmt.Sprintf("Pub %d", i),
			WrapperContent: []byte("{}"),
			CreatedBy:      "user",
		}
		if err := repo.Create(context.Background(), pub); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	seen := map[uuid.UUID]int{}
	token := ""
	pages := 0
	for {
		page, err := repo.List(context.Background(), port.PublicationListFilters{
			ProjectUID: "project-page",
			PageToken:  token,
			Limit:      pageSize,
		})
		if err != nil {
			t.Fatalf("List page %d: %v", pages, err)
		}
		pages++
		if len(page.Publications) > pageSize {
			t.Fatalf("page %d returned %d rows, over the %d limit", pages, len(page.Publications), pageSize)
		}
		for _, pub := range page.Publications {
			seen[pub.ID]++
		}
		if page.NextPageToken == "" {
			break
		}
		token = page.NextPageToken
		if pages > total {
			t.Fatal("pagination did not terminate — the cursor is not advancing")
		}
	}

	if len(seen) != total {
		t.Errorf("saw %d distinct publications across %d pages, want %d", len(seen), pages, total)
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("publication %s returned %d times, want exactly once", id, count)
		}
	}
}

// TestPublicationListRejectsMalformedPageToken proves a malformed cursor is a
// client error rather than a silent full-table scan from page one.
func TestPublicationListRejectsMalformedPageToken(t *testing.T) {
	repo := newPublicationIntegrationRepo(t)

	_, err := repo.List(context.Background(), port.PublicationListFilters{
		ProjectUID: "project-1",
		PageToken:  "not-a-valid-cursor!!",
	})
	if !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest for a malformed page token, got %v", err)
	}
}

// TestPublicationUpdate tests optimistic locking in Update.
func TestPublicationUpdate(t *testing.T) {
	repo := newPublicationIntegrationRepo(t)

	pub := &model.NewsletterPublication{
		ProjectUID:     "project-1",
		Slug:           "test-pub",
		Name:           "Test Publication",
		WrapperContent: []byte("{}"),
		CreatedBy:      "test-user",
	}
	if err := repo.Create(context.Background(), pub); err != nil {
		t.Fatalf("Create: %v", err)
	}

	oldVersion := pub.Version
	pub.Name = "Updated Name"

	if err := repo.Update(context.Background(), pub, oldVersion); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if pub.Version != oldVersion+1 {
		t.Errorf("Update version: got %d, want %d", pub.Version, oldVersion+1)
	}

	// Verify version mismatch is detected
	if err := repo.Update(context.Background(), pub, oldVersion); err != domain.ErrVersionMismatch {
		t.Errorf("Update with wrong version: got %v, want ErrVersionMismatch", err)
	}
}

// TestPublicationUpdatePersistsEveryMutableField re-reads the row after an
// update instead of trusting the in-memory model. Update assigns the model from
// its own SET list, so a field missing from that list still looks changed to the
// caller and to the response body while the stored row keeps the old value.
func TestPublicationUpdatePersistsEveryMutableField(t *testing.T) {
	repo := newPublicationIntegrationRepo(t)
	ctx := context.Background()

	pub := &model.NewsletterPublication{
		ProjectUID:     "project-1",
		Slug:           "field-coverage",
		Name:           "Original Name",
		EditorType:     model.EditorTypeClassic,
		WrapperContent: []byte("{}"),
		CreatedBy:      "test-user",
	}
	if err := repo.Create(ctx, pub); err != nil {
		t.Fatalf("Create: %v", err)
	}

	sender := "news@example.org"
	viewOnline := "https://example.org/archive"
	templateSet := "set-42"
	pub.Name = "Updated Name"
	pub.EditorType = model.EditorTypeBlocks
	pub.SenderEmail = &sender
	pub.ViewOnlineBase = &viewOnline
	pub.TemplateSetID = &templateSet
	pub.WrapperContent = []byte(`{"header":"hi"}`)
	if err := repo.Update(ctx, pub, pub.Version); err != nil {
		t.Fatalf("Update: %v", err)
	}

	stored, err := repo.Get(ctx, "project-1", pub.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Name != "Updated Name" {
		t.Errorf("stored name = %q, want %q", stored.Name, "Updated Name")
	}
	if stored.EditorType != model.EditorTypeBlocks {
		t.Errorf("stored editor_type = %q, want %q", stored.EditorType, model.EditorTypeBlocks)
	}
	if stored.SenderEmail == nil || *stored.SenderEmail != sender {
		t.Errorf("stored sender_email = %v, want %q", stored.SenderEmail, sender)
	}
	if stored.ViewOnlineBase == nil || *stored.ViewOnlineBase != viewOnline {
		t.Errorf("stored view_online_base = %v, want %q", stored.ViewOnlineBase, viewOnline)
	}
	if stored.TemplateSetID == nil || *stored.TemplateSetID != templateSet {
		t.Errorf("stored template_set_id = %v, want %q", stored.TemplateSetID, templateSet)
	}
	// Compare semantically, not byte-for-byte. wrapper_content is JSONB, and
	// Postgres reserializes it on the way out (canonical key order, a space
	// after each colon), so the bytes read back never match the bytes written
	// even when the value is identical.
	var storedWrapper, wantWrapper any
	if err := json.Unmarshal(stored.WrapperContent, &storedWrapper); err != nil {
		t.Fatalf("stored wrapper_content is not valid JSON (%s): %v", stored.WrapperContent, err)
	}
	if err := json.Unmarshal([]byte(`{"header":"hi"}`), &wantWrapper); err != nil {
		t.Fatalf("want wrapper_content is not valid JSON: %v", err)
	}
	if !reflect.DeepEqual(storedWrapper, wantWrapper) {
		t.Errorf("stored wrapper_content = %s, want %s", stored.WrapperContent, `{"header":"hi"}`)
	}
}

// TestPublicationGetDefault tests fetching the default publication.
func TestPublicationGetDefault(t *testing.T) {
	repo := newPublicationIntegrationRepo(t)

	pub := &model.NewsletterPublication{
		ProjectUID:     "project-1",
		Slug:           "default",
		Name:           "Default Publication",
		IsDefault:      true,
		WrapperContent: []byte("{}"),
		CreatedBy:      "test-user",
	}
	if err := repo.Create(context.Background(), pub); err != nil {
		t.Fatalf("Create: %v", err)
	}

	fetched, err := repo.GetDefault(context.Background(), "project-1")
	if err != nil {
		t.Fatalf("GetDefault: %v", err)
	}

	if fetched.ID != pub.ID {
		t.Errorf("GetDefault ID mismatch: got %s, want %s", fetched.ID, pub.ID)
	}
	if !fetched.IsDefault {
		t.Error("GetDefault returned non-default publication")
	}
}

// TestSchemaBackfill tests that the schema backfill creates default publications
// and links existing newsletters.
func TestSchemaBackfill(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("NEWSLETTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWSLETTER_TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	var dbName string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&dbName); err != nil {
		t.Fatalf("identify database: %v", err)
	}
	if !strings.Contains(dbName, "test") {
		t.Fatalf("refusing destructive tests against database %q", dbName)
	}

	// Create a fresh database state by truncating all tables
	if _, err := pool.Exec(ctx, "TRUNCATE newsletters, newsletter_publications CASCADE"); err != nil {
		t.Fatalf("truncate tables: %v", err)
	}

	// The backfill selects rows by publication_id_set, not by a global marker,
	// so a prior schema.Apply in this database does not stop it. The raw inserts
	// below leave publication_id_set at its default false, which is what a
	// pre-publication row looks like.
	//
	// Manually insert a newsletter before applying schema (simulate pre-publication rows)
	if _, err := pool.Exec(ctx, `
		INSERT INTO newsletters (project_uid, subject, body_html, ed_reply_email, created_by, status)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, "project-1", "Subject 1", "<p>Body</p>", "reply@example.com", "creator", "draft"); err != nil {
		t.Fatalf("insert newsletter: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO newsletters (project_uid, subject, body_html, ed_reply_email, created_by, status)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, "project-2", "Subject 2", "<p>Body</p>", "reply@example.com", "creator", "draft"); err != nil {
		t.Fatalf("insert newsletter: %v", err)
	}

	// Apply schema (which includes the backfill)
	if err := schema.Apply(ctx, pool); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	// Verify that default publications were created
	var count int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM newsletter_publications WHERE is_default = true").Scan(&count); err != nil {
		t.Fatalf("count publications: %v", err)
	}
	if count != 2 {
		t.Errorf("backfill created default publications: got %d, want 2", count)
	}

	// Verify that newsletters now have publication_id set
	var pubCount int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM newsletters WHERE publication_id IS NOT NULL").Scan(&pubCount); err != nil {
		t.Fatalf("count newsletters with publication: %v", err)
	}
	if pubCount != 2 {
		t.Errorf("backfill linked newsletters: got %d, want 2", pubCount)
	}

	// Verify that re-running schema is idempotent
	oldCountRows, err := pool.Query(ctx, "SELECT id, project_uid FROM newsletter_publications WHERE is_default = true")
	if err != nil {
		t.Fatalf("query old publications: %v", err)
	}
	oldIDs := make(map[string]uuid.UUID)
	for oldCountRows.Next() {
		var id uuid.UUID
		var projectUID string
		if err := oldCountRows.Scan(&id, &projectUID); err != nil {
			t.Fatalf("scan publication: %v", err)
		}
		oldIDs[projectUID] = id
	}
	oldCountRows.Close()

	// Re-apply schema
	if err := schema.Apply(ctx, pool); err != nil {
		t.Fatalf("re-apply schema: %v", err)
	}

	// Verify counts are unchanged
	var newCount int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM newsletter_publications WHERE is_default = true").Scan(&newCount); err != nil {
		t.Fatalf("count publications after re-apply: %v", err)
	}
	if newCount != 2 {
		t.Errorf("re-apply changed publication count: got %d, want 2", newCount)
	}

	// Verify IDs are unchanged (idempotency)
	newCountRows, err := pool.Query(ctx, "SELECT id, project_uid FROM newsletter_publications WHERE is_default = true")
	if err != nil {
		t.Fatalf("query new publications: %v", err)
	}
	for newCountRows.Next() {
		var id uuid.UUID
		var projectUID string
		if err := newCountRows.Scan(&id, &projectUID); err != nil {
			t.Fatalf("scan publication: %v", err)
		}
		if oldID, ok := oldIDs[projectUID]; !ok || oldID != id {
			t.Errorf("re-apply changed publication ID for %s", projectUID)
		}
	}
	newCountRows.Close()
}

// TestPublicationForeignKeyConstraint tests that the foreign key constraint
// prevents inserting newsletters with publications from different projects.
func TestPublicationForeignKeyConstraint(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("NEWSLETTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWSLETTER_TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	var dbName string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&dbName); err != nil {
		t.Fatalf("identify database: %v", err)
	}
	if !strings.Contains(dbName, "test") {
		t.Fatalf("refusing destructive tests against database %q", dbName)
	}

	if _, err := pool.Exec(ctx, "TRUNCATE newsletters, newsletter_publications CASCADE"); err != nil {
		t.Fatalf("truncate tables: %v", err)
	}

	if err := schema.Apply(ctx, pool); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	db := bun.NewDB(stdlib.OpenDBFromPool(pool), pgdialect.New())
	defer db.Close()

	// Create a publication for project-1
	pub := &model.NewsletterPublication{
		ProjectUID:     "project-1",
		Slug:           "test-pub",
		Name:           "Test Publication",
		WrapperContent: []byte("{}"),
		CreatedBy:      "test-user",
	}
	if _, err := db.NewInsert().Model(pub).Returning("*").Exec(ctx); err != nil {
		t.Fatalf("insert publication: %v", err)
	}

	// Try to insert a newsletter for project-2 with a publication from project-1
	// This should fail due to the composite FK on (project_uid, publication_id)
	newsletter := &model.Newsletter{
		ProjectUID:    "project-2",
		Subject:       "Test",
		BodyHTML:      "<p>Test</p>",
		EDReplyEmail:  "test@example.com",
		CreatedBy:     "test-user",
		PublicationID: &pub.ID,
	}

	_, err = db.NewInsert().Model(newsletter).Exec(ctx)
	if err == nil {
		t.Error("FK constraint should have prevented insertion of newsletter with publication from different project")
	}
}

// TestPublicationOneDefaultPerProject tests that only one default per project is allowed.
func TestPublicationOneDefaultPerProject(t *testing.T) {
	repo := newPublicationIntegrationRepo(t)

	pub1 := &model.NewsletterPublication{
		ProjectUID:     "project-1",
		Slug:           "default",
		Name:           "Default 1",
		IsDefault:      true,
		WrapperContent: []byte("{}"),
		CreatedBy:      "test-user",
	}
	if err := repo.Create(context.Background(), pub1); err != nil {
		t.Fatalf("Create first default: %v", err)
	}

	pub2 := &model.NewsletterPublication{
		ProjectUID:     "project-1",
		Slug:           "default-2",
		Name:           "Default 2",
		IsDefault:      true,
		WrapperContent: []byte("{}"),
		CreatedBy:      "test-user",
	}
	// This should fail due to the unique index on (project_uid) WHERE is_default
	err := repo.Create(context.Background(), pub2)
	if err == nil {
		t.Error("Should not allow two default publications for the same project")
	}
}

// TestBackfillDoesNotReattachUnfiledEditionsOnRestart is the regression test for
// the backfill filing legacy rows only, rather than running as a reconciliation
// loop over every null publication_id. schema.sql is applied on every pod start,
// and publication_id IS NULL is a legitimate resting state, so a re-run that
// re-files editions would silently undo a user deliberately unfiling one.
func TestBackfillDoesNotReattachUnfiledEditionsOnRestart(t *testing.T) {
	repo := newPublicationIntegrationRepo(t)
	ctx := context.Background()
	db := repo.db

	// Clear any prior state so this test drives the migration itself rather than
	// inheriting the harness's earlier apply.
	for _, stmt := range []string{
		"TRUNCATE newsletters CASCADE",
		"TRUNCATE newsletter_publications CASCADE",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("reset (%s): %v", stmt, err)
		}
	}

	// A pre-existing edition with no publication — the legacy case the backfill
	// is meant to file.
	var legacyID uuid.UUID
	if err := db.QueryRowContext(ctx, `
        INSERT INTO newsletters (project_uid, subject, body_html, ed_reply_email, created_by, status)
        VALUES ('project-backfill', 'Legacy', '<p>x</p>', 'ed@example.org', 'user', 'draft')
        RETURNING id`).Scan(&legacyID); err != nil {
		t.Fatalf("seed legacy newsletter: %v", err)
	}

	pool, err := pgxpool.New(ctx, os.Getenv("NEWSLETTER_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// First start: the migration runs and files the legacy edition.
	if err := schema.Apply(ctx, pool); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	var filedTo *uuid.UUID
	if err := db.QueryRowContext(ctx, "SELECT publication_id FROM newsletters WHERE id = ?", legacyID).Scan(&filedTo); err != nil {
		t.Fatalf("read legacy publication_id: %v", err)
	}
	if filedTo == nil {
		t.Fatal("backfill did not file the pre-existing edition")
	}

	// The user then deliberately unfiles it. Both columns are written, which is
	// what PostgresNewsletterRepo.Update does on every draft update.
	if _, err := db.ExecContext(ctx,
		"UPDATE newsletters SET publication_id = NULL, publication_id_set = true WHERE id = ?", legacyID); err != nil {
		t.Fatalf("unfile: %v", err)
	}

	// Second start: the migration must not run again.
	if err := schema.Apply(ctx, pool); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	var afterRestart *uuid.UUID
	if err := db.QueryRowContext(ctx, "SELECT publication_id FROM newsletters WHERE id = ?", legacyID).Scan(&afterRestart); err != nil {
		t.Fatalf("re-read legacy publication_id: %v", err)
	}
	if afterRestart != nil {
		t.Errorf("restart re-filed a deliberately unfiled edition into %v — the backfill is running as a reconciliation loop, not a one-time migration", afterRestart)
	}
}

// TestBackfillSkipsProjectsWithNoEditions proves a project that never had a
// newsletter is not handed a publication it did not ask for.
func TestBackfillSkipsProjectsWithNoEditions(t *testing.T) {
	repo := newPublicationIntegrationRepo(t)
	ctx := context.Background()
	db := repo.db

	for _, stmt := range []string{
		"TRUNCATE newsletters CASCADE",
		"TRUNCATE newsletter_publications CASCADE",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("reset (%s): %v", stmt, err)
		}
	}

	pool, err := pgxpool.New(ctx, os.Getenv("NEWSLETTER_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	if err := schema.Apply(ctx, pool); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM newsletter_publications").Scan(&count); err != nil {
		t.Fatalf("count publications: %v", err)
	}
	if count != 0 {
		t.Errorf("backfill created %d publication(s) with no editions to file; publications are created explicitly", count)
	}
}

// TestBackfillFilesLegacyRowWrittenAfterAnEarlierApply covers the rolling-deploy
// window. schema.sql is applied by every pod that starts, and while the rollout
// is in progress the old pods keep serving writes. A row one of them commits
// after a new pod has already applied the schema still has no publication_id and
// no publication_id_set, so the next startup must file it. A completion marker
// written by the first new pod would have skipped this row forever.
func TestBackfillFilesLegacyRowWrittenAfterAnEarlierApply(t *testing.T) {
	repo := newPublicationIntegrationRepo(t)
	ctx := context.Background()
	db := repo.db

	for _, stmt := range []string{
		"TRUNCATE newsletters CASCADE",
		"TRUNCATE newsletter_publications CASCADE",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("reset (%s): %v", stmt, err)
		}
	}

	pool, err := pgxpool.New(ctx, os.Getenv("NEWSLETTER_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// A new pod starts and applies the schema. There is nothing to file yet.
	if err := schema.Apply(ctx, pool); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	// An old pod, still serving during the rollout, writes an edition. It does
	// not know about publications, so it sets neither column.
	var lateID uuid.UUID
	if err := db.QueryRowContext(ctx, `
        INSERT INTO newsletters (project_uid, subject, body_html, ed_reply_email, created_by, status)
        VALUES ('project-rollout', 'Written by an old pod', '<p>x</p>', 'ed@example.org', 'user', 'draft')
        RETURNING id`).Scan(&lateID); err != nil {
		t.Fatalf("seed late legacy newsletter: %v", err)
	}

	// The next pod start files it.
	if err := schema.Apply(ctx, pool); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	var filedTo *uuid.UUID
	var flag bool
	if err := db.QueryRowContext(ctx,
		"SELECT publication_id, publication_id_set FROM newsletters WHERE id = ?", lateID).Scan(&filedTo, &flag); err != nil {
		t.Fatalf("read late row: %v", err)
	}
	if filedTo == nil {
		t.Error("a legacy row written after an earlier schema apply was never filed")
	}
	if !flag {
		t.Error("the backfill filed the row without stamping publication_id_set, so a later unfile would be undone on restart")
	}
}

// TestUpdatePreservesPublicationIDSetWhenOmitted is the regression test for the
// flag being stamped unconditionally. A legacy row carries
// publication_id_set=false, which is what marks it as still needing the
// backfill. Editing any unrelated field must not flip that to true, or the row
// is recorded as "a writer decided this is unfiled" and the backfill skips it
// forever while publication_id stays null.
func TestUpdatePreservesPublicationIDSetWhenOmitted(t *testing.T) {
	repo := newPublicationIntegrationRepo(t)
	ctx := context.Background()
	db := repo.db

	if _, err := db.ExecContext(ctx, "TRUNCATE newsletters CASCADE"); err != nil {
		t.Fatalf("truncate newsletters: %v", err)
	}

	// A legacy row: no publication, never decided by a publication-aware writer.
	var id uuid.UUID
	if err := db.QueryRowContext(ctx, `
        INSERT INTO newsletters (project_uid, subject, body_html, ed_reply_email, created_by, status, publication_id_set)
        VALUES ('project-flag', 'Legacy', '<p>x</p>', 'ed@example.org', 'user', 'draft', false)
        RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("seed legacy newsletter: %v", err)
	}

	nlRepo := NewPostgresNewsletterRepo(db)
	existing, err := nlRepo.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// Edit an unrelated field, exactly as the service does when the caller
	// omitted publication_id: PublicationID and PublicationIDSet are carried
	// through from the loaded row untouched.
	existing.Subject = "Renamed"
	if _, err := nlRepo.Update(ctx, existing, existing.Version); err != nil {
		t.Fatalf("update: %v", err)
	}

	var flag bool
	var pubID *uuid.UUID
	if err := db.QueryRowContext(ctx,
		"SELECT publication_id_set, publication_id FROM newsletters WHERE id = ?", id).Scan(&flag, &pubID); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if flag {
		t.Error("editing an unrelated field flipped publication_id_set to true; the legacy row is now permanently hidden from the backfill")
	}
	if pubID != nil {
		t.Errorf("publication_id changed to %v, want it left null", pubID)
	}
}
