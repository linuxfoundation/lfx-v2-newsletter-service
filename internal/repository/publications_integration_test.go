// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/schema"
)

// rewindToPrePublications undoes the publications objects so a test can replay
// the real upgrade from a database that only ever ran the previous schema.
func rewindToPrePublications(t *testing.T, repo *PostgresNewsletterRepo) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		`ALTER TABLE newsletters DROP CONSTRAINT IF EXISTS newsletters_publication_fk`,
		`DROP TRIGGER IF EXISTS trg_assign_implicit_publication ON newsletters`,
		`DROP FUNCTION IF EXISTS assign_implicit_publication()`,
		`ALTER TABLE newsletters DROP COLUMN IF EXISTS publication_uid`,
		`TRUNCATE publications CASCADE`,
	} {
		if _, err := repo.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("rewind %q: %v", stmt, err)
		}
	}
}

// applySchema re-runs schema.Apply over the pool the integration DSN points at.
func applySchema(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, integrationDSN())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := schema.Apply(ctx, pool); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
}

// insertLegacy inserts a row the way a pre-publications binary would: naming
// only the columns that binary knows, so publication_uid isn't mentioned.
func insertLegacy(t *testing.T, repo *PostgresNewsletterRepo, sqlTail string, args ...any) uuid.UUID {
	t.Helper()
	id := uuid.New()
	q := `INSERT INTO newsletters (id, project_uid, body_html, ed_reply_email, committee_uids, created_by, subject, status, version, group_id, batch_id, scheduled_at, body_layout) VALUES (?, ?, '<p>body</p>', 'ed@example.com', '{c1}', 'seeder', ` + sqlTail + `)`
	if _, err := repo.db.ExecContext(context.Background(), q, append([]any{id, "project-migrate"}, args...)...); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	return id
}

func publicationUIDOf(t *testing.T, repo *PostgresNewsletterRepo, id uuid.UUID) uuid.UUID {
	t.Helper()
	var pubUID uuid.UUID
	if err := repo.db.QueryRowContext(context.Background(), `SELECT publication_uid FROM newsletters WHERE id = ?`, id).Scan(&pubUID); err != nil {
		t.Fatalf("read publication_uid: %v", err)
	}
	return pubUID
}

func countPublications(t *testing.T, repo *PostgresNewsletterRepo) int {
	t.Helper()
	var n int
	if err := repo.db.QueryRowContext(context.Background(), `SELECT count(*) FROM publications`).Scan(&n); err != nil {
		t.Fatalf("count publications: %v", err)
	}
	return n
}

// TestPublicationsBackfill replays the upgrade over pre-publication rows of
// every state the triggers care about: an armed scheduled row (the backfill
// would abort on the armed-mutation guard without the GUC), a released
// scheduled row, a layout row, and plain rows.
func TestPublicationsBackfill(t *testing.T) {
	repo := newIntegrationRepo(t)
	rewindToPrePublications(t, repo)

	groupA, groupB := uuid.NewString(), uuid.NewString()
	future := "now() + interval '1 day'"
	armed := insertLegacy(t, repo, `'Armed', 'scheduled', 7, ?, 'batch-armed', `+future+`, NULL`, groupA)
	sentBatch := insertLegacy(t, repo, `'Released', 'sent', 3, ?, 'batch-released', `+future+`, NULL`, groupB)
	layout := insertLegacy(t, repo, `'  Layout draft  ', 'draft', 1, NULL, NULL, NULL, '{"blocks":[]}'::jsonb`)
	plain := insertLegacy(t, repo, `'Plain draft', 'draft', 1, NULL, NULL, NULL, NULL`)
	blank := insertLegacy(t, repo, `'', 'draft', 1, NULL, NULL, NULL, NULL`)
	long := insertLegacy(t, repo, `?, 'draft', 1, NULL, NULL, NULL, NULL`, strings.Repeat("x", 300))
	legacyRows := []uuid.UUID{armed, sentBatch, layout, plain, blank, long}

	// Snapshot what must not move.
	type snap struct {
		version   int64
		updatedAt string
	}
	before := map[uuid.UUID]snap{}
	for _, id := range legacyRows {
		var s snap
		if err := repo.db.QueryRowContext(context.Background(), `SELECT version, updated_at::text FROM newsletters WHERE id = ?`, id).Scan(&s.version, &s.updatedAt); err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		before[id] = s
	}

	// The upgrade itself. Must not trip trg_reject_armed_scheduled_mutation.
	applySchema(t)

	if got := countPublications(t, repo); got != len(legacyRows) {
		t.Fatalf("publications = %d, want one per newsletter (%d)", got, len(legacyRows))
	}
	seen := map[uuid.UUID]bool{}
	names := map[uuid.UUID]string{}
	for _, id := range legacyRows {
		pubUID := publicationUIDOf(t, repo, id)
		if pubUID == uuid.Nil || seen[pubUID] {
			t.Fatalf("newsletter %s publication %s: want a distinct publication per newsletter", id, pubUID)
		}
		seen[pubUID] = true

		var name, projectUID string
		var implicit bool
		var templateKey, fromName, replyTo *string
		var committees int
		if err := repo.db.QueryRowContext(context.Background(),
			`SELECT name, project_uid, implicit, template_key, from_name, reply_to_email, cardinality(committee_uids) FROM publications WHERE id = ?`, pubUID).
			Scan(&name, &projectUID, &implicit, &templateKey, &fromName, &replyTo, &committees); err != nil {
			t.Fatalf("read publication: %v", err)
		}
		names[id] = name
		if !implicit || projectUID != "project-migrate" || templateKey != nil || fromName != nil || replyTo != nil || committees != 0 {
			t.Errorf("newsletter %s: publication = implicit:%v project:%q template:%v from:%v replyTo:%v committees:%v; want implicit, same project, no inherited settings",
				id, implicit, projectUID, templateKey, fromName, replyTo, committees)
		}

		var version int64
		var updatedAt string
		if err := repo.db.QueryRowContext(context.Background(), `SELECT version, updated_at::text FROM newsletters WHERE id = ?`, id).Scan(&version, &updatedAt); err != nil {
			t.Fatalf("read newsletter: %v", err)
		}
		if version != before[id].version || updatedAt != before[id].updatedAt {
			t.Errorf("newsletter %s changed: version %d→%d updated_at %s→%s", id, before[id].version, version, before[id].updatedAt, updatedAt)
		}
	}
	if names[layout] != "Layout draft" || names[blank] != "Untitled newsletter" || len(names[long]) != 200 {
		t.Errorf("names: trimmed=%q blank=%q long=%d", names[layout], names[blank], len(names[long]))
	}

	// Idempotent: a later boot neither adds publications nor fails.
	applySchema(t)
	if got := countPublications(t, repo); got != len(legacyRows) {
		t.Fatalf("after re-apply publications = %d, want %d", got, len(legacyRows))
	}

	// The backfill must not have left the armed guard relaxed.
	_, err := repo.db.ExecContext(context.Background(), `UPDATE newsletters SET subject = 'touched' WHERE id = ?`, armed)
	if err == nil || !strings.Contains(err.Error(), "service version mismatch") {
		t.Fatalf("armed guard after migration: err = %v, want the rolling-deploy rejection", err)
	}
}

// TestLegacyInsertGetsImplicitPublication covers the rolling-deploy window and
// a rollback: a binary that does not know publication_uid inserts a row and the
// trigger assigns it one, keeping NOT NULL and the one-publication-per-row
// invariant.
func TestLegacyInsertGetsImplicitPublication(t *testing.T) {
	repo := newIntegrationRepo(t)

	id := insertLegacy(t, repo, `'Old pod draft', 'draft', 1, NULL, NULL, NULL, NULL`)

	pubUID := publicationUIDOf(t, repo, id)
	var implicit bool
	var name string
	if err := repo.db.QueryRowContext(context.Background(), `SELECT implicit, name FROM publications WHERE id = ?`, pubUID).Scan(&implicit, &name); err != nil {
		t.Fatalf("read publication: %v", err)
	}
	if !implicit || name != "Old pod draft" {
		t.Fatalf("publication implicit=%v name=%q", implicit, name)
	}
}

func TestCreateWithoutPublicationReturnsImplicitUID(t *testing.T) {
	repo := newIntegrationRepo(t)
	n := &model.Newsletter{
		ProjectUID:    "project-create",
		Subject:       "No publication",
		BodyHTML:      "<p>b</p>",
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"c1"},
		Status:        model.StatusDraft,
		CreatedBy:     "seeder",
	}
	if err := repo.Create(context.Background(), n); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if n.PublicationUID == nil {
		t.Fatal("RETURNING did not populate publication_uid from the trigger")
	}
}

func TestPublicationCompositeFKRefusesOtherProject(t *testing.T) {
	repo := newIntegrationRepo(t)
	pubs := NewPostgresPublicationRepo(repo.db)
	ctx := context.Background()

	other := &model.Publication{ProjectUID: "project-other", Name: "Other", CommitteeUIDs: []string{}, CreatedBy: "seeder"}
	if err := pubs.CreatePublication(ctx, other); err != nil {
		t.Fatalf("CreatePublication: %v", err)
	}

	n := &model.Newsletter{
		ProjectUID:     "project-mine",
		PublicationUID: &other.ID,
		Subject:        "Cross project",
		BodyHTML:       "<p>b</p>",
		EDReplyEmail:   "ed@example.com",
		CommitteeUIDs:  []string{"c1"},
		Status:         model.StatusDraft,
		CreatedBy:      "seeder",
	}
	if err := repo.Create(ctx, n); err == nil {
		t.Fatal("expected a foreign-key error attaching a newsletter to another project's publication")
	}
}

func TestPublicationRepoCRUDAndList(t *testing.T) {
	repo := newIntegrationRepo(t)
	pubs := NewPostgresPublicationRepo(repo.db)
	ctx := context.Background()
	project := "project-" + uuid.NewString()

	// An implicit publication must not be listed.
	implicit := &model.Publication{ProjectUID: project, Name: "Implicit", CommitteeUIDs: []string{}, Implicit: true, CreatedBy: "seeder"}
	if err := pubs.CreatePublication(ctx, implicit); err != nil {
		t.Fatalf("create implicit: %v", err)
	}

	var ids []uuid.UUID
	for range 25 {
		p := &model.Publication{ProjectUID: project, Name: "Named", CommitteeUIDs: []string{"c1"}, CreatedBy: "seeder"}
		if err := pubs.CreatePublication(ctx, p); err != nil {
			t.Fatalf("create: %v", err)
		}
		ids = append(ids, p.ID)
	}

	page, err := pubs.ListPublications(ctx, project, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Publications) != defaultListLimit || page.NextPageToken == "" {
		t.Fatalf("first page = %d rows, next %q", len(page.Publications), page.NextPageToken)
	}
	second, err := pubs.ListPublications(ctx, project, page.NextPageToken)
	if err != nil {
		t.Fatalf("list page 2: %v", err)
	}
	if len(second.Publications) != 5 || second.NextPageToken != "" {
		t.Fatalf("second page = %d rows, next %q", len(second.Publications), second.NextPageToken)
	}
	for _, p := range append(page.Publications, second.Publications...) {
		if p.Implicit {
			t.Fatalf("implicit publication listed: %s", p.ID)
		}
	}

	if _, err := pubs.GetPublication(ctx, "project-elsewhere", ids[0]); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-project get: err = %v, want ErrNotFound", err)
	}

	got, err := pubs.GetPublication(ctx, project, ids[0])
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Version != 1 {
		t.Fatalf("version = %d, want 1", got.Version)
	}
	key := "aaif-member"
	got.Name = "Renamed"
	got.TemplateKey = &key
	got.CommitteeUIDs = []string{"c2"}
	updated, err := pubs.UpdatePublication(ctx, got, 1)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Version != 2 || updated.Name != "Renamed" || *updated.TemplateKey != key {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := pubs.UpdatePublication(ctx, got, 1); !errors.Is(err, domain.ErrVersionMismatch) {
		t.Fatalf("stale update: err = %v, want ErrVersionMismatch", err)
	}
	stranger := *got
	stranger.ID = uuid.New()
	if _, err := pubs.UpdatePublication(ctx, &stranger, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing update: err = %v, want ErrNotFound", err)
	}
}

func TestListAllFiltersByPublication(t *testing.T) {
	repo := newIntegrationRepo(t)
	pubs := NewPostgresPublicationRepo(repo.db)
	ctx := context.Background()
	project := "project-" + uuid.NewString()

	pub := &model.Publication{ProjectUID: project, Name: "Weekly", CommitteeUIDs: []string{}, CreatedBy: "seeder"}
	if err := pubs.CreatePublication(ctx, pub); err != nil {
		t.Fatalf("create publication: %v", err)
	}
	mk := func(subject string, publicationUID *uuid.UUID) *model.Newsletter {
		n := &model.Newsletter{ProjectUID: project, PublicationUID: publicationUID, Subject: subject, BodyHTML: "<p>b</p>", EDReplyEmail: "ed@example.com", CommitteeUIDs: []string{"c1"}, Status: model.StatusDraft, CreatedBy: "seeder"}
		if err := repo.Create(ctx, n); err != nil {
			t.Fatalf("create newsletter: %v", err)
		}
		return n
	}
	edition := mk("edition", &pub.ID)
	mk("other", nil)

	page, err := repo.ListAll(ctx, port.ListFilters{ProjectUID: project, PublicationUID: &pub.ID})
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(page.Newsletters) != 1 || page.Newsletters[0].ID != edition.ID {
		t.Fatalf("got %d rows", len(page.Newsletters))
	}
}
