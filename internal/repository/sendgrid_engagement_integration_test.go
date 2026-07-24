// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

//go:build integration

package repository_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/repository"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/schema"
)

// TestSendGridEngagementStore_Integration exercises the SendGrid engagement
// store against a real Postgres (DATABASE_URL). It validates the schema DDL
// applies and the Bun read/write methods behave, including open-event dedup and
// the engagement rollup. Run with: go test -tags integration ./internal/repository/
func TestSendGridEngagementStore_Integration(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	defer pool.Close()
	if err := schema.Apply(ctx, pool); err != nil {
		t.Fatalf("schema apply: %v", err)
	}
	db := bun.NewDB(stdlib.OpenDBFromPool(pool), pgdialect.New())
	defer func() { _ = db.Close() }()

	store := repository.NewSendGridEngagementStore(db)
	// Everything is namespaced under a per-run group so the test is hermetic —
	// no fixed keys that could collide with a prior run's leftover rows.
	group := "grp-" + time.Now().UTC().Format("150405.000000000")
	m1, m2 := group+"-m1", group+"-m2"
	ev1, ev2 := group+"-ev1", group+"-ev2"
	t.Cleanup(func() {
		_, _ = db.NewRaw("DELETE FROM sendgrid_open_events WHERE email_id IN (?, ?)", m1, m2).Exec(ctx)
		_, _ = db.NewRaw("DELETE FROM sendgrid_recipient_engagement WHERE group_id = ?", group).Exec(ctx)
	})

	now := time.Now().UTC()
	must(t, store.RecordSent(ctx, m1, group, "a@x.io", now))
	must(t, store.RecordSent(ctx, m2, group, "b@x.io", now))
	must(t, store.RecordSent(ctx, m1, group, "a@x.io", now)) // idempotent on email_id

	must(t, store.ApplyDelivered(ctx, m1, group, "a@x.io", now))
	must(t, store.ApplyOpen(ctx, ev1, m1, group, "a@x.io", now))
	must(t, store.ApplyOpen(ctx, ev1, m1, group, "a@x.io", now)) // dedup on sg_event_id — no double count
	must(t, store.ApplyOpen(ctx, ev2, m1, group, "a@x.io", now.Add(time.Minute)))
	must(t, store.ApplyFailed(ctx, m2, group, "b@x.io", now))

	eng, err := store.Engagement(ctx, group)
	if err != nil {
		t.Fatalf("Engagement: %v", err)
	}
	// Opened is the raw open total (m1's open_count of 2: ev1 deduped, ev1+ev2
	// counted); UniqueOpens is the one recipient (m1) who opened.
	if eng.TotalSent != 2 || eng.Delivered != 1 || eng.Opened != 2 || eng.UniqueOpens != 1 || eng.Failed != 1 {
		t.Errorf("Engagement = %+v; want TotalSent=2 Delivered=1 Opened=2 UniqueOpens=1 Failed=1", eng)
	}

	rec, err := store.RecipientByEmailID(ctx, m1)
	if err != nil {
		t.Fatalf("RecipientByEmailID: %v", err)
	}
	if !rec.Delivered || !rec.Opened {
		t.Errorf("m1 record = %+v; want delivered+opened", rec)
	}
	if rec.OpenCount != 2 || len(rec.OpenedAtList) != 2 {
		t.Errorf("m1 opens = count %d / list %d; want 2 / 2 (ev1 deduped, ev1+ev2 counted)", rec.OpenCount, len(rec.OpenedAtList))
	}

	// Self-heal: an event for an email_id that RecordSent never persisted still
	// lands its engagement — the row is created from the event's group_id / to.
	// Uses its own group so it does not perturb the group-of-two assertions above.
	healGroup := group + "-heal"
	m3, ev3 := healGroup+"-m3", healGroup+"-ev3"
	t.Cleanup(func() {
		_, _ = db.NewRaw("DELETE FROM sendgrid_open_events WHERE email_id = ?", m3).Exec(ctx)
		_, _ = db.NewRaw("DELETE FROM sendgrid_recipient_engagement WHERE group_id = ?", healGroup).Exec(ctx)
	})
	must(t, store.ApplyOpen(ctx, ev3, m3, healGroup, "c@x.io", now))
	rec3, err := store.RecipientByEmailID(ctx, m3)
	if err != nil {
		t.Fatalf("RecipientByEmailID(self-healed m3): %v", err)
	}
	if !rec3.Opened || rec3.OpenCount != 1 || rec3.To != "c@x.io" || rec3.GroupID != healGroup {
		t.Errorf("self-healed m3 = %+v; want opened, count 1, to c@x.io, group %s", rec3, healGroup)
	}

	recs, err := store.RecipientsByGroupID(ctx, group)
	if err != nil {
		t.Fatalf("RecipientsByGroupID: %v", err)
	}
	if len(recs) != 2 {
		t.Errorf("group records = %d; want 2", len(recs))
	}

	// DeleteByGroupID removes the group's engagement rows and their open events
	// (cleanup after a fully-failed send reverts), leaving other groups intact.
	must(t, store.DeleteByGroupID(ctx, group))
	recs, err = store.RecipientsByGroupID(ctx, group)
	if err != nil {
		t.Fatalf("RecipientsByGroupID after delete: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("group records after delete = %d; want 0", len(recs))
	}
	if opens, err := store.RecipientByEmailID(ctx, m1); err == nil {
		t.Errorf("m1 should be gone after group delete, got %+v", opens)
	}
	if rec3b, err := store.RecipientByEmailID(ctx, m3); err != nil || rec3b == nil {
		t.Errorf("the other group (healGroup) must survive the delete: %v", err)
	}
}

// TestSchemaMigration_SendProviderUpgradePath verifies the send_provider
// migration repairs an EXISTING database, not just a fresh one: a column left
// nullable with a NULL row by a prior partial run must be backfilled and made
// NOT NULL with a default, idempotently.
func TestSchemaMigration_SendProviderUpgradePath(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	defer pool.Close()
	must(t, schema.Apply(ctx, pool))
	db := bun.NewDB(stdlib.OpenDBFromPool(pool), pgdialect.New())
	defer func() { _ = db.Close() }()

	id := "mig-" + time.Now().UTC().Format("150405.000000000")
	t.Cleanup(func() { _, _ = db.NewRaw("DELETE FROM newsletters WHERE created_by = ?", id).Exec(ctx) })

	// Simulate a prior partial run: column nullable, no default, no CHECK, plus a
	// legacy row with a NULL provider.
	must(t, exec(ctx, db, `ALTER TABLE newsletters DROP CONSTRAINT IF EXISTS newsletters_send_provider_check`))
	must(t, exec(ctx, db, `ALTER TABLE newsletters ALTER COLUMN send_provider DROP NOT NULL`))
	must(t, exec(ctx, db, `ALTER TABLE newsletters ALTER COLUMN send_provider DROP DEFAULT`))
	must(t, exec(ctx, db,
		`INSERT INTO newsletters (project_uid, subject, body_html, ed_reply_email, created_by, send_provider) VALUES ('p', 's', 'b', 'e@x', ?, NULL)`, id))

	// Re-apply: the upgrade path must backfill the NULL and re-assert NOT NULL/default.
	must(t, schema.Apply(ctx, pool))

	var got string
	must(t, db.NewRaw(`SELECT send_provider FROM newsletters WHERE created_by = ?`, id).Scan(ctx, &got))
	if got != "email-service" {
		t.Errorf("legacy NULL row send_provider = %q, want backfilled to email-service", got)
	}
	// NOT NULL is enforced now.
	if err := exec(ctx, db, `INSERT INTO newsletters (project_uid, subject, body_html, ed_reply_email, created_by, send_provider) VALUES ('p','s','b','e@x',?,NULL)`, id); err == nil {
		t.Error("expected a NOT NULL violation inserting a NULL send_provider after the migration")
	}
	// Default applies when omitted, and the CHECK is present.
	must(t, exec(ctx, db, `INSERT INTO newsletters (project_uid, subject, body_html, ed_reply_email, created_by) VALUES ('p','s','b','e@x',?)`, id))
	var def string
	must(t, db.NewRaw(`SELECT send_provider FROM newsletters WHERE created_by = ? AND send_provider IS NOT NULL ORDER BY created_at DESC LIMIT 1`, id).Scan(ctx, &def))
	if def != "email-service" {
		t.Errorf("default send_provider = %q, want email-service", def)
	}
	// A third apply is a no-op (idempotent).
	must(t, schema.Apply(ctx, pool))
}

func exec(ctx context.Context, db *bun.DB, q string, args ...any) error {
	_, err := db.NewRaw(q, args...).Exec(ctx)
	return err
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
