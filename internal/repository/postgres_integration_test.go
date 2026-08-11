// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
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

	// Now revert the scheduled row (armed) back to draft
	_, err = repo.RevertScheduled(ctx, scheduledID, n.Version)
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
