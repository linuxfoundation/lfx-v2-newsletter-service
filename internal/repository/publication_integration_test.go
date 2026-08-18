// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

//go:build integration

package repository

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
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

	list, err := repo.List(context.Background(), "project-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(list) != 2 {
		t.Errorf("List length: got %d, want 2", len(list))
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
