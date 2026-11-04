// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/schema"
)

// Postgres-backed repository tests. The production SQL (status predicate,
// array containment, tuple keyset) is the sole enforcement of the sent-only
// member-visibility invariant, so it gets exercised against a real database
// rather than only through fakes. Gated on NEWSLETTER_TEST_DATABASE_URL: unset
// (e.g. a plain local `go test ./...`) skips; CI provides a postgres service
// and sets it (see .github/workflows/newsletter-api-build.yml).
//
// Each helper call TRUNCATEs the newsletters table, so these tests must not
// run against a database holding data anyone cares about.

// newIntegrationRepo connects, applies the embedded schema, and returns a
// repository over a clean newsletters table.
func newIntegrationRepo(t *testing.T) *PostgresNewsletterRepo {
	t.Helper()
	dsn := os.Getenv("NEWSLETTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWSLETTER_TEST_DATABASE_URL not set — skipping Postgres-backed repository tests")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Runtime guard, not just a doc warning: this helper TRUNCATEs the
	// newsletters table, so refuse to run unless the connected database
	// positively identifies as test-only. A mispointed env var (shared dev,
	// staging, prod) fails loudly here before any destructive statement runs.
	var dbName string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&dbName); err != nil {
		pool.Close()
		t.Fatalf("identify database: %v", err)
	}
	if !strings.Contains(dbName, "test") {
		pool.Close()
		t.Fatalf("refusing destructive repository tests against database %q — NEWSLETTER_TEST_DATABASE_URL must point at a dedicated test database whose name contains \"test\"", dbName)
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

	if _, err := db.ExecContext(ctx, "TRUNCATE newsletters CASCADE"); err != nil {
		t.Fatalf("truncate newsletters: %v", err)
	}
	return NewPostgresNewsletterRepo(db)
}

// seed inserts a newsletter row directly with the given status/audience;
// sentAt may be nil for drafts.
func seed(t *testing.T, repo *PostgresNewsletterRepo, status model.Status, committeeUIDs []string, sentAt *time.Time) uuid.UUID {
	t.Helper()
	n := &model.Newsletter{
		ProjectUID:    "project-1",
		Subject:       fmt.Sprintf("Subject %s", uuid.New()),
		BodyHTML:      "<p>body</p>",
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: committeeUIDs,
		Status:        status,
		SentAt:        sentAt,
		CreatedBy:     "seeder",
	}
	// The newsletters_sent_requires_group_id CHECK forbids sent rows without a
	// group_id (normally minted by MarkSending), and
	// newsletters_group_id_uuid_format requires it to be a UUID.
	if status == model.StatusSent {
		groupID := uuid.NewString()
		n.GroupID = &groupID
	}
	if err := repo.Create(context.Background(), n); err != nil {
		t.Fatalf("seed newsletter: %v", err)
	}
	return n.ID
}

func ts(minutesAgo int) *time.Time {
	v := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Duration(minutesAgo) * time.Minute)
	return &v
}

func TestListSentByCommittee_ExcludesDraftsAndSending(t *testing.T) {
	repo := newIntegrationRepo(t)

	sentID := seed(t, repo, model.StatusSent, []string{"committee-a"}, ts(10))
	seed(t, repo, model.StatusDraft, []string{"committee-a"}, nil)
	seed(t, repo, model.StatusSending, []string{"committee-a"}, ts(5))

	page, err := repo.ListSentByCommittee(context.Background(), "committee-a", "")
	if err != nil {
		t.Fatalf("ListSentByCommittee: %v", err)
	}
	if len(page.Newsletters) != 1 || page.Newsletters[0].ID != sentID {
		t.Fatalf("got %d rows, want only the sent row %s", len(page.Newsletters), sentID)
	}
}

func TestListSentByCommittee_ArrayContainment(t *testing.T) {
	repo := newIntegrationRepo(t)

	multi := seed(t, repo, model.StatusSent, []string{"committee-a", "committee-b"}, ts(10))
	bOnly := seed(t, repo, model.StatusSent, []string{"committee-b"}, ts(20))
	seed(t, repo, model.StatusSent, []string{"committee-c"}, ts(30))

	pageA, err := repo.ListSentByCommittee(context.Background(), "committee-a", "")
	if err != nil {
		t.Fatalf("committee-a: %v", err)
	}
	if len(pageA.Newsletters) != 1 || pageA.Newsletters[0].ID != multi {
		t.Fatalf("committee-a: got %d rows, want only %s", len(pageA.Newsletters), multi)
	}

	pageB, err := repo.ListSentByCommittee(context.Background(), "committee-b", "")
	if err != nil {
		t.Fatalf("committee-b: %v", err)
	}
	if len(pageB.Newsletters) != 2 || pageB.Newsletters[0].ID != multi || pageB.Newsletters[1].ID != bOnly {
		t.Fatalf("committee-b: got %v, want [%s %s] (sent_at desc)", ids(pageB.Newsletters), multi, bOnly)
	}
}

func TestListSentByCommittee_PaginatesAcrossEqualSentAt(t *testing.T) {
	repo := newIntegrationRepo(t)

	// defaultListLimit+5 rows sharing ONE sent_at: ordering degenerates to the
	// id tiebreak, so this exercises the tuple cursor exactly where a naive
	// single-column cursor would skip or duplicate rows.
	same := ts(60)
	total := defaultListLimit + 5
	want := make(map[uuid.UUID]bool, total)
	for i := 0; i < total; i++ {
		want[seed(t, repo, model.StatusSent, []string{"committee-a"}, same)] = true
	}

	first, err := repo.ListSentByCommittee(context.Background(), "committee-a", "")
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(first.Newsletters) != defaultListLimit || first.NextPageToken == "" {
		t.Fatalf("page 1: got %d rows (token %q), want %d rows with token", len(first.Newsletters), first.NextPageToken, defaultListLimit)
	}

	second, err := repo.ListSentByCommittee(context.Background(), "committee-a", first.NextPageToken)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(second.Newsletters) != total-defaultListLimit || second.NextPageToken != "" {
		t.Fatalf("page 2: got %d rows (token %q), want %d rows and no token", len(second.Newsletters), second.NextPageToken, total-defaultListLimit)
	}

	seen := make(map[uuid.UUID]bool, total)
	for _, n := range append(first.Newsletters, second.Newsletters...) {
		if seen[n.ID] {
			t.Fatalf("row %s appeared on both pages", n.ID)
		}
		seen[n.ID] = true
		if !want[n.ID] {
			t.Fatalf("unexpected row %s", n.ID)
		}
	}
	if len(seen) != total {
		t.Fatalf("pages covered %d of %d rows", len(seen), total)
	}
}

func TestListSentByCommittee_RejectsMalformedToken(t *testing.T) {
	repo := newIntegrationRepo(t)

	for _, token := range []string{"not-base64!", "e30"} {
		_, err := repo.ListSentByCommittee(context.Background(), "committee-a", token)
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Errorf("token %q: got %v, want ErrInvalidRequest", token, err)
		}
	}
}

func ids(rows []*model.Newsletter) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(rows))
	for _, n := range rows {
		out = append(out, n.ID)
	}
	return out
}

func TestArmedMutations_AllMethodsSucceed(t *testing.T) {
	repo := newIntegrationRepo(t)

	// Create a draft newsletter to be armed
	draftID := seed(t, repo, model.StatusDraft, []string{"committee-a"}, nil)

	ctx := context.Background()

	// Transition to sending with batch_id armed (scheduled send)
	scheduledAt := time.Now().UTC().Add(1 * time.Hour)
	_, err := repo.MarkSending(ctx, draftID, uuid.NewString(), "sendgrid", 100, 1, &scheduledAt, "batch-123")
	if err != nil {
		t.Fatalf("MarkSending with batch_id: %v", err)
	}

	// Get the newsletter to find the new version after MarkSending
	n, err := repo.Get(ctx, draftID)
	if err != nil {
		t.Fatalf("Get after MarkSending: %v", err)
	}
	if n.Status != model.StatusSending {
		t.Fatalf("Status: got %v, want StatusSending", n.Status)
	}
	if n.BatchID == nil || *n.BatchID == "" {
		t.Fatalf("BatchID: got nil or empty, want 'batch-123'")
	}

	// Test MarkScheduled: armed row -> scheduled
	_, err = repo.MarkScheduled(ctx, draftID, n.Version)
	if err != nil {
		t.Fatalf("MarkScheduled on armed row: %v", err)
	}

	// Get the newsletter to find the new version after MarkScheduled
	n, err = repo.Get(ctx, draftID)
	if err != nil {
		t.Fatalf("Get after MarkScheduled: %v", err)
	}
	if n.Status != model.StatusScheduled {
		t.Fatalf("Status after MarkScheduled: got %v, want StatusScheduled", n.Status)
	}

	// Test SettleDueScheduled: scheduled with past scheduled_at -> sent
	settled, err := repo.SettleDueScheduled(ctx, time.Now().UTC().Add(2*time.Hour))
	if err != nil {
		t.Fatalf("SettleDueScheduled: %v", err)
	}
	if settled != 1 {
		t.Fatalf("SettleDueScheduled rows affected: got %d, want 1", settled)
	}

	// Verify row is now sent
	n, err = repo.Get(ctx, draftID)
	if err != nil {
		t.Fatalf("Get after SettleDueScheduled: %v", err)
	}
	if n.Status != model.StatusSent {
		t.Fatalf("Status after SettleDueScheduled: got %v, want StatusSent", n.Status)
	}

	// Test RevertScheduled: revert a scheduled row back to draft
	scheduledID := seed(t, repo, model.StatusDraft, []string{"committee-b"}, nil)
	_, err = repo.MarkSending(ctx, scheduledID, uuid.NewString(), "sendgrid", 50, 1, &scheduledAt, "batch-456")
	if err != nil {
		t.Fatalf("MarkSending for RevertScheduled test: %v", err)
	}

	n, err = repo.Get(ctx, scheduledID)
	if err != nil {
		t.Fatalf("Get for RevertScheduled setup: %v", err)
	}

	_, err = repo.MarkScheduled(ctx, scheduledID, n.Version)
	if err != nil {
		t.Fatalf("MarkScheduled for RevertScheduled test: %v", err)
	}

	n, err = repo.Get(ctx, scheduledID)
	if err != nil {
		t.Fatalf("Get after MarkScheduled for revert: %v", err)
	}

	// Now revert the scheduled row (armed) back to draft, passing the batch_id
	// that was armed so a delayed duplicate cancel won't revert a newer send
	_, err = repo.RevertScheduled(ctx, scheduledID, n.Version, n.BatchID)
	if err != nil {
		t.Fatalf("RevertScheduled on armed row: %v", err)
	}

	n, err = repo.Get(ctx, scheduledID)
	if err != nil {
		t.Fatalf("Get after RevertScheduled: %v", err)
	}
	if n.Status != model.StatusDraft {
		t.Fatalf("Status after RevertScheduled: got %v, want StatusDraft", n.Status)
	}

	// Test RevertSending: revert a sending row (with batch_id) back to draft
	sendingID := seed(t, repo, model.StatusDraft, []string{"committee-c"}, nil)
	_, err = repo.MarkSending(ctx, sendingID, uuid.NewString(), "sendgrid", 75, 1, &scheduledAt, "batch-789")
	if err != nil {
		t.Fatalf("MarkSending for RevertSending test: %v", err)
	}

	n, err = repo.Get(ctx, sendingID)
	if err != nil {
		t.Fatalf("Get for RevertSending setup: %v", err)
	}
	if n.Status != model.StatusSending {
		t.Fatalf("Status before RevertSending: got %v, want StatusSending", n.Status)
	}

	// Revert the sending row (armed) back to draft
	err = repo.RevertSending(ctx, sendingID)
	if err != nil {
		t.Fatalf("RevertSending on armed row: %v", err)
	}

	n, err = repo.Get(ctx, sendingID)
	if err != nil {
		t.Fatalf("Get after RevertSending: %v", err)
	}
	if n.Status != model.StatusDraft {
		t.Fatalf("Status after RevertSending: got %v, want StatusDraft", n.Status)
	}
	if n.BatchID != nil && *n.BatchID != "" {
		t.Fatalf("BatchID after RevertSending: got %q, want nil/empty", *n.BatchID)
	}
}

// TestLayoutSend_TriggerRejectsLegacyClaim covers the rolling-deploy / rollback
// guard for block-layout newsletters. A legacy pod (one that predates the
// feature and so never sets app.allow_layout_send) must not be able to claim a
// body_layout row for dispatch: it ignores body_layout and would wrap the
// stored body_html in the legacy email_chrome a second time. The
// reject_legacy_layout_send trigger blocks the draft -> sending edge unless the
// layout-aware GUC is set. The new binary's MarkSending sets it, so a normal
// claim still succeeds. Mirrors TestArmedMutations_TriggerRejectsUnauthorized.
func TestLayoutSend_TriggerRejectsLegacyClaim(t *testing.T) {
	repo := newIntegrationRepo(t)
	ctx := context.Background()

	draftID := seed(t, repo, model.StatusDraft, []string{"committee-a"}, nil)

	// Mark the row as a layout newsletter. A plain draft UPDATE does not touch
	// the draft -> sending edge, so the guard lets body_layout be set.
	if _, err := repo.db.NewRaw(
		`UPDATE newsletters SET body_layout = ?::jsonb WHERE id = ?`,
		`{"wrapper_key":"default","blocks":[]}`, draftID,
	).Exec(ctx); err != nil {
		t.Fatalf("set body_layout: %v", err)
	}

	// A legacy pod's claim is a raw draft -> sending UPDATE with no layout GUC;
	// the trigger must reject it.
	_, err := repo.db.NewRaw(
		`UPDATE newsletters SET status = ?, group_id = ?, version = version + 1 WHERE id = ? AND status = ?`,
		model.StatusSending, uuid.NewString(), draftID, model.StatusDraft,
	).Exec(ctx)
	if err == nil {
		t.Fatalf("legacy layout claim should have been rejected by the trigger but succeeded")
	}
	if !strings.Contains(err.Error(), "cannot send layout newsletter") && !strings.Contains(err.Error(), "service version mismatch") {
		t.Fatalf("unexpected rejection message: %v", err)
	}

	// The row is untouched: still a draft (the rejected UPDATE rolled back).
	n, err := repo.Get(ctx, draftID)
	if err != nil {
		t.Fatalf("Get after rejected claim: %v", err)
	}
	if n.Status != model.StatusDraft {
		t.Fatalf("Status should be unchanged StatusDraft, got %v", n.Status)
	}

	// The new binary's MarkSending sets app.allow_layout_send, so a real claim
	// of the same layout row succeeds.
	sending, err := repo.MarkSending(ctx, draftID, uuid.NewString(), "sendgrid", 100, 1, nil, "")
	if err != nil {
		t.Fatalf("layout-aware MarkSending should succeed: %v", err)
	}
	if sending.Status != model.StatusSending {
		t.Fatalf("Status after MarkSending: got %v, want StatusSending", sending.Status)
	}
}

// TestLayoutUpdate_TriggerRejectsLegacyBodyHTMLEdit covers the second edge of
// reject_legacy_layout_send: a legacy pod editing a layout row's body_html. On a
// layout row, body_html is DERIVED from body_layout, so a legacy pod (which
// ignores body_layout) that rewrites only body_html leaves the two inconsistent;
// the next layout-aware send re-renders the stale body_layout and silently
// discards the edit. The trigger blocks that body_html mutation unless the caller
// sets app.allow_layout_update. A raw legacy UPDATE never does and is rejected;
// the layout-aware repo.Update sets it (rewriting body_html and body_layout
// together, including explicit layout removal) and succeeds.
func TestLayoutUpdate_TriggerRejectsLegacyBodyHTMLEdit(t *testing.T) {
	repo := newIntegrationRepo(t)
	ctx := context.Background()

	draftID := seed(t, repo, model.StatusDraft, []string{"committee-a"}, nil)

	// Mark the row as a layout newsletter. OLD.body_layout is NULL on this first
	// write, so the body_html guard does not fire (and body_html is untouched).
	if _, err := repo.db.NewRaw(
		`UPDATE newsletters SET body_layout = ?::jsonb WHERE id = ?`,
		`{"wrapper_key":"default","blocks":[]}`, draftID,
	).Exec(ctx); err != nil {
		t.Fatalf("set body_layout: %v", err)
	}

	// A legacy pod's HTML-only edit is a raw body_html UPDATE with no layout-update
	// GUC; the trigger must reject it because body_layout would be left stale.
	_, err := repo.db.NewRaw(
		`UPDATE newsletters SET body_html = ?, version = version + 1 WHERE id = ?`,
		"<p>legacy html-only edit</p>", draftID,
	).Exec(ctx)
	if err == nil {
		t.Fatalf("legacy body_html edit of a layout row should have been rejected by the trigger but succeeded")
	}
	if !strings.Contains(err.Error(), "cannot edit layout newsletter body_html") && !strings.Contains(err.Error(), "service version mismatch") {
		t.Fatalf("unexpected rejection message: %v", err)
	}

	// The row is untouched: body_html is still the seeded value (rejected UPDATE
	// rolled back).
	n, err := repo.Get(ctx, draftID)
	if err != nil {
		t.Fatalf("Get after rejected edit: %v", err)
	}
	if n.BodyHTML != "<p>body</p>" {
		t.Fatalf("body_html should be unchanged after rejected legacy edit, got %q", n.BodyHTML)
	}

	// The layout-aware Update path sets app.allow_layout_update, so editing the
	// same layout row's body_html (re-derived alongside body_layout) succeeds.
	n.BodyHTML = "<p>layout-aware re-render</p>"
	updated, err := repo.Update(ctx, n, n.Version)
	if err != nil {
		t.Fatalf("layout-aware Update should succeed: %v", err)
	}
	if updated.BodyHTML != "<p>layout-aware re-render</p>" {
		t.Fatalf("body_html after layout-aware Update: got %q", updated.BodyHTML)
	}

	// Explicit layout removal is also part of the layout-aware path: dropping
	// body_layout to NULL while rewriting body_html must be allowed (the guard
	// keys on OLD.body_layout, and Update sets the GUC).
	updated.BodyLayout = nil
	updated.BodyHTML = "<p>layout removed</p>"
	removed, err := repo.Update(ctx, updated, updated.Version)
	if err != nil {
		t.Fatalf("layout-aware Update removing the layout should succeed: %v", err)
	}
	if removed.BodyLayout != nil {
		t.Fatalf("body_layout should be cleared after explicit removal, got %q", string(removed.BodyLayout))
	}
}

func TestLayoutUpdate_TriggerAllowsMetadataOnlyEdit(t *testing.T) {
	repo := newIntegrationRepo(t)
	ctx := context.Background()

	draftID := seed(t, repo, model.StatusDraft, []string{"committee-a"}, nil)

	// Make it a layout row.
	if _, err := repo.db.NewRaw(
		`UPDATE newsletters SET body_layout = ?::jsonb WHERE id = ?`,
		`{"wrapper_key":"default","blocks":[]}`, draftID,
	).Exec(ctx); err != nil {
		t.Fatalf("set body_layout: %v", err)
	}

	// A metadata-only edit that leaves body_html unchanged must pass Guard 2 even
	// without app.allow_layout_update: the guard is scoped to body_html content
	// changes (NEW.body_html IS DISTINCT FROM OLD.body_html), so it must not
	// over-fire on status/metadata updates like MarkSending.
	if _, err := repo.db.NewRaw(
		`UPDATE newsletters SET subject = ?, version = version + 1 WHERE id = ?`,
		"metadata-only edit", draftID,
	).Exec(ctx); err != nil {
		t.Fatalf("metadata-only edit of a layout row should be allowed by the trigger: %v", err)
	}

	n, err := repo.Get(ctx, draftID)
	if err != nil {
		t.Fatalf("Get after metadata-only edit: %v", err)
	}
	if n.Subject != "metadata-only edit" {
		t.Fatalf("subject should be updated, got %q", n.Subject)
	}
	if n.BodyHTML != "<p>body</p>" {
		t.Fatalf("body_html should be unchanged after a metadata-only edit, got %q", n.BodyHTML)
	}
}

func TestArmedMutations_TriggerRejectsUnauthorized(t *testing.T) {
	repo := newIntegrationRepo(t)

	ctx := context.Background()
	draftID := seed(t, repo, model.StatusDraft, []string{"committee-a"}, nil)

	// Arm the row by transitioning to sending with batch_id
	scheduledAt := time.Now().UTC().Add(1 * time.Hour)
	_, err := repo.MarkSending(ctx, draftID, uuid.NewString(), "sendgrid", 100, 1, &scheduledAt, "batch-123")
	if err != nil {
		t.Fatalf("MarkSending to arm row: %v", err)
	}

	// Verify the row is armed
	n, err := repo.Get(ctx, draftID)
	if err != nil {
		t.Fatalf("Get armed row: %v", err)
	}
	if n.BatchID == nil || *n.BatchID == "" {
		t.Fatalf("Row should be armed but BatchID is nil/empty")
	}

	// Get the underlying bun.DB to run raw SQL that bypasses the GUC flag
	db := repo.db

	// Test 1: Raw UPDATE without GUC flag should be rejected by trigger
	// Attempt to update status without setting app.allow_armed_mutation
	_, err = db.NewRaw("UPDATE newsletters SET status = ? WHERE id = ?", "draft", draftID).Exec(ctx)
	if err == nil {
		t.Fatalf("Raw UPDATE should have been rejected by trigger but succeeded")
	}
	if !strings.Contains(err.Error(), "cannot mutate armed scheduled row") && !strings.Contains(err.Error(), "service version mismatch") {
		t.Fatalf("UPDATE error message unexpected: %v", err)
	}

	// Test 2: Raw DELETE without GUC flag should be rejected by trigger
	_, err = db.NewRaw("DELETE FROM newsletters WHERE id = ?", draftID).Exec(ctx)
	if err == nil {
		t.Fatalf("Raw DELETE should have been rejected by trigger but succeeded")
	}
	if !strings.Contains(err.Error(), "cannot mutate armed scheduled row") && !strings.Contains(err.Error(), "service version mismatch") {
		t.Fatalf("DELETE error message unexpected: %v", err)
	}

	// Verify the row still exists and is still armed (mutations were rejected)
	n, err = repo.Get(ctx, draftID)
	if err != nil {
		t.Fatalf("Get after failed mutations: %v", err)
	}
	if n.Status != model.StatusSending {
		t.Fatalf("Status should be unchanged to StatusSending, got %v", n.Status)
	}
	if n.BatchID == nil || *n.BatchID == "" {
		t.Fatalf("BatchID should still be armed")
	}
}

// TestMarkSent_ArmedRow covers a zero-recipient scheduled send: MarkSending
// arms the row with a non-nil batch_id, and the zero-recipient branch of
// SendNewsletter settles straight to sent via MarkSent without ever calling
// MarkScheduled. MarkSent must set the GUC itself or the trigger rejects the
// update, stranding the row in 'sending' (LFXV2-2685 follow-up finding).
func TestMarkSent_ArmedRow(t *testing.T) {
	repo := newIntegrationRepo(t)
	ctx := context.Background()

	draftID := seed(t, repo, model.StatusDraft, []string{"committee-a"}, nil)

	scheduledAt := time.Now().UTC().Add(1 * time.Hour)
	sending, err := repo.MarkSending(ctx, draftID, uuid.NewString(), "sendgrid", 0, 1, &scheduledAt, "batch-zero-recipient")
	if err != nil {
		t.Fatalf("MarkSending with batch_id: %v", err)
	}
	if sending.BatchID == nil || *sending.BatchID == "" {
		t.Fatalf("BatchID should be armed before MarkSent")
	}

	sent, err := repo.MarkSent(ctx, draftID, time.Now().UTC(), sending.Version)
	if err != nil {
		t.Fatalf("MarkSent on armed row: %v", err)
	}
	if sent.Status != model.StatusSent {
		t.Fatalf("Status after MarkSent: got %v, want StatusSent", sent.Status)
	}
}

// TestRecoverStuckSending_RecoversArmedRow covers crash recovery for a
// scheduled fan-out: a 'sending' row whose batch_id is non-nil (armed) but
// whose updated_at is stale must be recoverable to 'scheduled' by the sweep.
// The recovery UPDATE touches an armed row, so it must set the GUC in the
// same transaction or the trigger rejects it, stranding the row in 'sending'
// forever (LFXV2-2685 follow-up finding).
func TestRecoverStuckSending_RecoversArmedRow(t *testing.T) {
	repo := newIntegrationRepo(t)
	ctx := context.Background()

	draftID := seed(t, repo, model.StatusDraft, []string{"committee-a"}, nil)

	scheduledAt := time.Now().UTC().Add(1 * time.Hour)
	_, err := repo.MarkSending(ctx, draftID, uuid.NewString(), "sendgrid", 50, 1, &scheduledAt, "batch-stuck")
	if err != nil {
		t.Fatalf("MarkSending to arm row: %v", err)
	}

	// Backdate updated_at directly so the row looks stuck past the recovery
	// threshold; MarkSending itself always sets updated_at = now(). This row
	// is armed (batch_id set), so this raw UPDATE must set the GUC in the
	// same transaction or the trigger rejects it just like any other armed
	// mutation.
	err = repo.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewRaw("SET LOCAL app.allow_armed_mutation = 'true'").Exec(ctx); err != nil {
			return err
		}
		_, err := tx.NewRaw(
			"UPDATE newsletters SET updated_at = ? WHERE id = ?",
			time.Now().UTC().Add(-1*time.Hour), draftID,
		).Exec(ctx)
		return err
	})
	if err != nil {
		t.Fatalf("backdate updated_at: %v", err)
	}

	recovered, err := repo.RecoverStuckSending(ctx, 30*time.Minute)
	if err != nil {
		t.Fatalf("RecoverStuckSending: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("RecoverStuckSending rows affected: got %d, want 1", recovered)
	}

	n, err := repo.Get(ctx, draftID)
	if err != nil {
		t.Fatalf("Get after RecoverStuckSending: %v", err)
	}
	if n.Status != model.StatusScheduled {
		t.Fatalf("Status after RecoverStuckSending: got %v, want StatusScheduled", n.Status)
	}
}

// TestDelete_RejectsSendingImmediateRow covers the TOCTOU race the atomic
// Delete predicate must close for an *immediate* send: batch_id stays NULL
// for an immediate send, so a predicate gated on batch_id IS NULL alone would
// still let a caller delete the row out from under an in-flight fan-out once
// MarkSending has claimed it. The predicate must also require status=draft
// (LFXV2-2685 follow-up finding).
func TestDelete_RejectsSendingImmediateRow(t *testing.T) {
	repo := newIntegrationRepo(t)
	ctx := context.Background()

	draftID := seed(t, repo, model.StatusDraft, []string{"committee-a"}, nil)

	sending, err := repo.MarkSending(ctx, draftID, uuid.NewString(), "sendgrid", 1, 1, nil, "")
	if err != nil {
		t.Fatalf("MarkSending (immediate): %v", err)
	}
	if sending.BatchID != nil {
		t.Fatalf("BatchID should stay nil for an immediate send, got %v", *sending.BatchID)
	}

	err = repo.Delete(ctx, draftID)
	if !errors.Is(err, domain.ErrSendInProgress) {
		t.Fatalf("Delete on sending immediate row: got %v, want ErrSendInProgress", err)
	}

	// The row must still exist, untouched.
	n, err := repo.Get(ctx, draftID)
	if err != nil {
		t.Fatalf("Get after rejected delete: %v", err)
	}
	if n.Status != model.StatusSending {
		t.Fatalf("Status after rejected delete: got %v, want StatusSending", n.Status)
	}
}

// TestRevertScheduled_RejectsStaleBatchID covers the interleaving from the
// LFXV2-2685 follow-up finding: two cancel requests can both read the row
// while it is armed with batch A. After the first request reverts it and a
// new send arms a fresh batch B on the same row, a delayed second request for
// batch A must not match — it is no longer the batch that request actually
// cancelled — and must leave the newer send untouched.
func TestRevertScheduled_RejectsStaleBatchID(t *testing.T) {
	repo := newIntegrationRepo(t)
	ctx := context.Background()

	draftID := seed(t, repo, model.StatusDraft, []string{"committee-a"}, nil)

	scheduledAt := time.Now().UTC().Add(1 * time.Hour)
	sendingA, err := repo.MarkSending(ctx, draftID, uuid.NewString(), "sendgrid", 10, 1, &scheduledAt, "batch-A")
	if err != nil {
		t.Fatalf("MarkSending (batch A): %v", err)
	}
	if _, err := repo.MarkScheduled(ctx, draftID, sendingA.Version); err != nil {
		t.Fatalf("MarkScheduled (batch A): %v", err)
	}
	armedA, err := repo.Get(ctx, draftID)
	if err != nil {
		t.Fatalf("Get after MarkScheduled (batch A): %v", err)
	}
	batchA := "batch-A"

	// First cancel request: reads the row armed with batch A and reverts it.
	// This is the "already processed" request in the race.
	reverted, err := repo.RevertScheduled(ctx, draftID, armedA.Version, &batchA)
	if err != nil {
		t.Fatalf("RevertScheduled (batch A, first request): %v", err)
	}
	if reverted.Status != model.StatusDraft {
		t.Fatalf("Status after first revert: got %v, want StatusDraft", reverted.Status)
	}

	// A new send arms a fresh batch B on the same row. draft.Version has moved
	// on past the literal 1 used to arm batch A: the revert above bumped it.
	sendingB, err := repo.MarkSending(ctx, draftID, uuid.NewString(), "sendgrid", 10, reverted.Version, &scheduledAt, "batch-B")
	if err != nil {
		t.Fatalf("MarkSending (batch B): %v", err)
	}
	if _, err := repo.MarkScheduled(ctx, draftID, sendingB.Version); err != nil {
		t.Fatalf("MarkScheduled (batch B): %v", err)
	}
	armedB, err := repo.Get(ctx, draftID)
	if err != nil {
		t.Fatalf("Get after MarkScheduled (batch B): %v", err)
	}

	// Delayed second cancel request: still carries the stale batch A ID (and
	// the version it read before the first revert). It must not match batch
	// B's row, even though the row's status (scheduled) is otherwise eligible.
	_, err = repo.RevertScheduled(ctx, draftID, armedA.Version, &batchA)
	if !errors.Is(err, domain.ErrScheduled) {
		t.Fatalf("RevertScheduled (stale batch A, second request): got %v, want ErrScheduled", err)
	}

	// The newer send (batch B) must be untouched.
	n, err := repo.Get(ctx, draftID)
	if err != nil {
		t.Fatalf("Get after stale revert attempt: %v", err)
	}
	if n.Status != model.StatusScheduled {
		t.Fatalf("Status after stale revert attempt: got %v, want StatusScheduled", n.Status)
	}
	if n.BatchID == nil || *n.BatchID != "batch-B" {
		t.Fatalf("BatchID after stale revert attempt: got %v, want batch-B", n.BatchID)
	}
	if n.Version != armedB.Version {
		t.Fatalf("Version after stale revert attempt: got %d, want unchanged %d", n.Version, armedB.Version)
	}
}

// TestGetMeta_ExcludesBodyLayout pins the hot-path read: GetMeta returns the
// row without its body_layout (so the open-pixel / analytics routes never
// decode the layout JSON), while Get still materializes it.
func TestGetMeta_ExcludesBodyLayout(t *testing.T) {
	repo := newIntegrationRepo(t)
	ctx := context.Background()

	id := seed(t, repo, model.StatusDraft, []string{"committee-a"}, nil)
	if _, err := repo.db.NewRaw(
		`UPDATE newsletters SET body_layout = ?::jsonb WHERE id = ?`,
		`{"wrapper_key":"default","blocks":[]}`, id,
	).Exec(ctx); err != nil {
		t.Fatalf("set body_layout: %v", err)
	}

	full, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(full.BodyLayout) == 0 {
		t.Fatalf("Get should materialize body_layout")
	}

	meta, err := repo.GetMeta(ctx, id)
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if len(meta.BodyLayout) != 0 {
		t.Errorf("GetMeta must not materialize body_layout, got %s", meta.BodyLayout)
	}
	if meta.ProjectUID != full.ProjectUID {
		t.Errorf("GetMeta should still return project_uid: got %q want %q", meta.ProjectUID, full.ProjectUID)
	}
}

// Postgres-backed tests of the audience repository. Like the other integration
// tests they run only when NEWSLETTER_TEST_DATABASE_URL points at a database
// whose name contains "test" (see newIntegrationRepo).

func newIntegrationAudienceRepo(t *testing.T) (*PostgresAudienceRepo, *PostgresNewsletterRepo) {
	t.Helper()
	repo := newIntegrationRepo(t)
	return NewPostgresAudienceRepo(repo.db), repo
}

func testMembers(emails ...string) []model.CommitteeMember {
	out := make([]model.CommitteeMember, 0, len(emails))
	for _, e := range emails {
		out = append(out, model.CommitteeMember{Email: e, FirstName: "First", LastName: "Last"})
	}
	return out
}

func memberEmails(members []model.CommitteeMember) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, m.Email)
	}
	return out
}

func TestBeginResolve_ClaimsOnceWhileProcessing(t *testing.T) {
	ctx := context.Background()
	audiences, repo := newIntegrationAudienceRepo(t)
	id := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)

	claimed, err := audiences.BeginResolve(ctx, id, "project-1", []string{"c1"})
	require.NoError(t, err)
	assert.True(t, claimed)

	claimed, err = audiences.BeginResolve(ctx, id, "project-1", []string{"c1", "c2"})
	require.NoError(t, err)
	assert.False(t, claimed, "a second claim while processing must not succeed")

	a, err := audiences.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, model.AudienceProcessing, a.Status)
	assert.Equal(t, []string{"c1"}, a.CommitteeUIDs, "the losing claim did not change the in-flight row")
	assert.Equal(t, "project-1", a.ProjectUID)
	assert.Nil(t, a.ResolvedAt)
}

func TestBeginResolve_ConcurrentClaimsYieldExactlyOne(t *testing.T) {
	ctx := context.Background()
	audiences, repo := newIntegrationAudienceRepo(t)
	id := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)

	var claimedCount atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := audiences.BeginResolve(ctx, id, "project-1", []string{"c1"})
			assert.NoError(t, err)
			if claimed {
				claimedCount.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, claimedCount.Load())
}

func TestBeginResolve_ReclaimsReadyAndFailedRows(t *testing.T) {
	ctx := context.Background()
	audiences, repo := newIntegrationAudienceRepo(t)
	id := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)

	claimed, err := audiences.BeginResolve(ctx, id, "project-1", []string{"c1"})
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, audiences.ReplaceMembers(ctx, id, testMembers("a@example.com")))
	require.NoError(t, audiences.MarkReady(ctx, id, 1, 4))

	// From ready: the row is reset to a clean processing row.
	claimed, err = audiences.BeginResolve(ctx, id, "project-1", []string{"c1", "c2"})
	require.NoError(t, err)
	assert.True(t, claimed)
	a, err := audiences.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, model.AudienceProcessing, a.Status)
	assert.Equal(t, []string{"c1", "c2"}, a.CommitteeUIDs)
	assert.Zero(t, a.RecipientCount)
	assert.Zero(t, a.SkippedNoEmail)
	assert.Empty(t, a.Error)
	assert.Nil(t, a.ResolvedAt)

	// From failed.
	require.NoError(t, audiences.MarkFailed(ctx, id, "boom"))
	claimed, err = audiences.BeginResolve(ctx, id, "project-1", []string{"c1"})
	require.NoError(t, err)
	assert.True(t, claimed)
	a, err = audiences.Get(ctx, id)
	require.NoError(t, err)
	assert.Empty(t, a.Error)
}

func TestReplaceMembers_ReplacesInsteadOfAppending(t *testing.T) {
	ctx := context.Background()
	audiences, repo := newIntegrationAudienceRepo(t)
	id := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)
	_, err := audiences.BeginResolve(ctx, id, "project-1", []string{"c1"})
	require.NoError(t, err)

	require.NoError(t, audiences.ReplaceMembers(ctx, id, testMembers("a@example.com", "b@example.com")))
	got, err := audiences.ListMembers(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, []string{"a@example.com", "b@example.com"}, memberEmails(got))

	require.NoError(t, audiences.ReplaceMembers(ctx, id, testMembers("c@example.com", "b@example.com")))
	got, err = audiences.ListMembers(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, []string{"b@example.com", "c@example.com"}, memberEmails(got), "the earlier members are gone")
	assert.Equal(t, "Last", got[0].LastName)

	require.NoError(t, audiences.ReplaceMembers(ctx, id, nil))
	got, err = audiences.ListMembers(ctx, id)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestReplaceMembers_OtherNewslettersAreUntouched(t *testing.T) {
	ctx := context.Background()
	audiences, repo := newIntegrationAudienceRepo(t)
	one := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)
	two := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)
	for _, id := range []uuid.UUID{one, two} {
		_, err := audiences.BeginResolve(ctx, id, "project-1", []string{"c1"})
		require.NoError(t, err)
	}
	require.NoError(t, audiences.ReplaceMembers(ctx, one, testMembers("a@example.com")))
	require.NoError(t, audiences.ReplaceMembers(ctx, two, testMembers("same@example.com", "a@example.com")))
	require.NoError(t, audiences.ReplaceMembers(ctx, two, testMembers("z@example.com")))

	got, err := audiences.ListMembers(ctx, one)
	require.NoError(t, err)
	assert.Equal(t, []string{"a@example.com"}, memberEmails(got))
}

func TestReplaceMembers_InsertsMoreThanOneBatch(t *testing.T) {
	ctx := context.Background()
	audiences, repo := newIntegrationAudienceRepo(t)
	id := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)
	_, err := audiences.BeginResolve(ctx, id, "project-1", []string{"c1"})
	require.NoError(t, err)

	total := memberInsertBatch*2 + 17
	members := make([]model.CommitteeMember, 0, total)
	for i := range total {
		members = append(members, model.CommitteeMember{Email: fmt.Sprintf("user%06d@example.com", i)})
	}
	require.NoError(t, audiences.ReplaceMembers(ctx, id, members))

	got, err := audiences.ListMembers(ctx, id)
	require.NoError(t, err)
	assert.Len(t, got, total)
}

func TestReplaceMembers_RequiresProcessingAudience(t *testing.T) {
	ctx := context.Background()
	audiences, repo := newIntegrationAudienceRepo(t)
	id := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)

	err := audiences.ReplaceMembers(ctx, id, testMembers("a@example.com"))
	assert.ErrorIs(t, err, domain.ErrNotFound, "no audience row")

	_, err = audiences.BeginResolve(ctx, id, "project-1", []string{"c1"})
	require.NoError(t, err)
	require.NoError(t, audiences.ReplaceMembers(ctx, id, testMembers("a@example.com")))
	require.NoError(t, audiences.MarkFailed(ctx, id, "boom"))

	err = audiences.ReplaceMembers(ctx, id, testMembers("late@example.com"))
	assert.ErrorIs(t, err, domain.ErrNotFound, "a failed audience cannot be written to")
	got, err := audiences.ListMembers(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, []string{"a@example.com"}, memberEmails(got), "the late write changed nothing")
}

func TestMarkReadyAndMarkFailed_OnlyMatchProcessingRows(t *testing.T) {
	ctx := context.Background()
	audiences, repo := newIntegrationAudienceRepo(t)
	id := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)

	assert.ErrorIs(t, audiences.MarkReady(ctx, id, 1, 0), domain.ErrNotFound, "no row")
	assert.ErrorIs(t, audiences.MarkFailed(ctx, id, "x"), domain.ErrNotFound, "no row")

	_, err := audiences.BeginResolve(ctx, id, "project-1", []string{"c1"})
	require.NoError(t, err)
	require.NoError(t, audiences.MarkReady(ctx, id, 7, 3))

	a, err := audiences.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, model.AudienceReady, a.Status)
	assert.Equal(t, 7, a.RecipientCount)
	assert.Equal(t, 3, a.SkippedNoEmail)
	require.NotNil(t, a.ResolvedAt)

	assert.ErrorIs(t, audiences.MarkFailed(ctx, id, "late"), domain.ErrNotFound, "a ready audience is not failed afterwards")
	assert.ErrorIs(t, audiences.MarkReady(ctx, id, 1, 0), domain.ErrNotFound)
	a, err = audiences.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, model.AudienceReady, a.Status)
}

func TestMarkFailed_StoresMessageAndTruncatesLongOnes(t *testing.T) {
	ctx := context.Background()
	audiences, repo := newIntegrationAudienceRepo(t)
	id := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)
	_, err := audiences.BeginResolve(ctx, id, "project-1", []string{"c1"})
	require.NoError(t, err)

	long := ""
	for range maxAudienceErrorLength + 50 {
		long += "é"
	}
	require.NoError(t, audiences.MarkFailed(ctx, id, long))

	a, err := audiences.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, model.AudienceFailed, a.Status)
	assert.Len(t, []rune(a.Error), maxAudienceErrorLength)
	assert.Nil(t, a.ResolvedAt)
}

func TestAudienceGet_NotFound(t *testing.T) {
	audiences, _ := newIntegrationAudienceRepo(t)
	_, err := audiences.Get(context.Background(), uuid.New())
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestFailStale_FailsOnlyOldProcessingRows(t *testing.T) {
	ctx := context.Background()
	audiences, repo := newIntegrationAudienceRepo(t)
	stale := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)
	fresh := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)
	ready := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)
	for _, id := range []uuid.UUID{stale, fresh, ready} {
		_, err := audiences.BeginResolve(ctx, id, "project-1", []string{"c1"})
		require.NoError(t, err)
	}
	require.NoError(t, audiences.MarkReady(ctx, ready, 0, 0))
	_, err := audiences.db.ExecContext(ctx,
		`UPDATE newsletter_audiences SET updated_at = now() - interval '2 hours' WHERE newsletter_id IN (?, ?)`, stale, ready)
	require.NoError(t, err)

	n, err := audiences.FailStale(ctx, 40*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	a, err := audiences.Get(ctx, stale)
	require.NoError(t, err)
	assert.Equal(t, model.AudienceFailed, a.Status)
	assert.NotEmpty(t, a.Error)
	a, err = audiences.Get(ctx, fresh)
	require.NoError(t, err)
	assert.Equal(t, model.AudienceProcessing, a.Status)
	a, err = audiences.Get(ctx, ready)
	require.NoError(t, err)
	assert.Equal(t, model.AudienceReady, a.Status, "an old ready row is not touched")

	n, err = audiences.FailStale(ctx, 40*time.Minute)
	require.NoError(t, err)
	assert.Zero(t, n, "a second sweep changes nothing")

	// A failed row can be claimed again, so the UI can offer a retry.
	claimed, err := audiences.BeginResolve(ctx, stale, "project-1", []string{"c1"})
	require.NoError(t, err)
	assert.True(t, claimed)
}

func TestReplaceMembers_KeepsAStaleSweepAway(t *testing.T) {
	ctx := context.Background()
	audiences, repo := newIntegrationAudienceRepo(t)
	id := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)
	_, err := audiences.BeginResolve(ctx, id, "project-1", []string{"c1"})
	require.NoError(t, err)
	_, err = audiences.db.ExecContext(ctx,
		`UPDATE newsletter_audiences SET updated_at = now() - interval '2 hours' WHERE newsletter_id = ?`, id)
	require.NoError(t, err)

	require.NoError(t, audiences.ReplaceMembers(ctx, id, testMembers("a@example.com")))

	n, err := audiences.FailStale(ctx, 40*time.Minute)
	require.NoError(t, err)
	assert.Zero(t, n, "ReplaceMembers moved updated_at forward")
}

func TestDeletingANewsletterDeletesItsAudienceAndMembers(t *testing.T) {
	ctx := context.Background()
	audiences, repo := newIntegrationAudienceRepo(t)
	id := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)
	other := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)
	for _, nid := range []uuid.UUID{id, other} {
		_, err := audiences.BeginResolve(ctx, nid, "project-1", []string{"c1"})
		require.NoError(t, err)
		require.NoError(t, audiences.ReplaceMembers(ctx, nid, testMembers("a@example.com")))
		require.NoError(t, audiences.MarkReady(ctx, nid, 1, 0))
	}

	require.NoError(t, repo.Delete(ctx, id))

	_, err := audiences.Get(ctx, id)
	assert.ErrorIs(t, err, domain.ErrNotFound, "the audience row is deleted with the newsletter")
	var members int
	require.NoError(t, audiences.db.QueryRowContext(ctx,
		`SELECT count(*) FROM newsletter_audience_members WHERE newsletter_id = ?`, id).Scan(&members))
	assert.Zero(t, members, "the members are deleted with the audience")

	_, err = audiences.Get(ctx, other)
	assert.NoError(t, err, "another newsletter's audience is kept")
	got, err := audiences.ListMembers(ctx, other)
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestAudienceConstraints(t *testing.T) {
	ctx := context.Background()
	audiences, repo := newIntegrationAudienceRepo(t)
	id := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)

	_, err := audiences.db.ExecContext(ctx,
		`INSERT INTO newsletter_audiences (newsletter_id, project_uid, committee_uids, status) VALUES (?, 'p', '[]', 'bogus')`, id)
	assert.Error(t, err, "an unknown status is rejected")

	_, err = audiences.db.ExecContext(ctx,
		`INSERT INTO newsletter_audiences (newsletter_id, project_uid, committee_uids, status) VALUES (?, 'p', '[]', 'ready')`, id)
	assert.Error(t, err, "a ready row needs resolved_at")

	_, err = audiences.db.ExecContext(ctx,
		`INSERT INTO newsletter_audiences (newsletter_id, project_uid, committee_uids, status) VALUES (?, 'p', '[]', 'processing')`, uuid.New())
	assert.Error(t, err, "the newsletter must exist")
}

func TestAudienceGet_RoundTripsCommitteeUIDs(t *testing.T) {
	ctx := context.Background()
	audiences, repo := newIntegrationAudienceRepo(t)
	id := seed(t, repo, model.StatusDraft, []string{"c1"}, nil)

	_, err := audiences.BeginResolve(ctx, id, "project-1", []string{"a", "b\"quoted", "ü"})
	require.NoError(t, err)
	a, err := audiences.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b\"quoted", "ü"}, a.CommitteeUIDs)
	assert.Equal(t, id, a.NewsletterID)
	assert.False(t, a.RequestedAt.IsZero())
	assert.False(t, a.UpdatedAt.IsZero())
}

// TestSchemaApplyIsIdempotent applies the embedded schema twice more against a
// database that already has it, which is what every pod restart does.
func TestSchemaApplyIsIdempotent(t *testing.T) {
	newIntegrationRepo(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("NEWSLETTER_TEST_DATABASE_URL"))
	require.NoError(t, err)
	defer pool.Close()

	require.NoError(t, schema.Apply(ctx, pool))
	require.NoError(t, schema.Apply(ctx, pool))

	var tables int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_name IN ('newsletter_audiences', 'newsletter_audience_members')`).Scan(&tables))
	assert.Equal(t, 2, tables)

	var constraints int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_constraint WHERE conname IN ('newsletter_audiences_status_check', 'newsletter_audiences_ready_requires_resolved')`).Scan(&constraints))
	assert.Equal(t, 2, constraints, "each constraint exists once")
}
