// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

//go:build integration

package repository_test

import (
	"context"
	"os"
	"strings"
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
	// Register the closes as cleanups (not defers) so they run AFTER the row
	// cleanups below: t.Cleanup is LIFO, and the closes registered first run last,
	// keeping the handles open while the row deletes execute.
	t.Cleanup(pool.Close)
	if err := schema.Apply(ctx, pool); err != nil {
		t.Fatalf("schema apply: %v", err)
	}
	db := bun.NewDB(stdlib.OpenDBFromPool(pool), pgdialect.New())
	t.Cleanup(func() { _ = db.Close() })

	store := repository.NewSendGridEngagementStore(db)
	// Everything is namespaced under a per-run group so the test is hermetic —
	// no fixed keys that could collide with a prior run's leftover rows.
	group := "grp-" + time.Now().UTC().Format("150405.000000000")
	m1, m2 := group+"-m1", group+"-m2"
	ev1, ev2 := group+"-ev1", group+"-ev2"
	t.Cleanup(func() {
		if _, err := db.NewRaw("DELETE FROM sendgrid_open_events WHERE email_id IN (?, ?)", m1, m2).Exec(ctx); err != nil {
			t.Errorf("cleanup open_events: %v", err)
		}
		if _, err := db.NewRaw("DELETE FROM sendgrid_recipient_engagement WHERE group_id = ?", group).Exec(ctx); err != nil {
			t.Errorf("cleanup engagement: %v", err)
		}
		if _, err := db.NewRaw("DELETE FROM sendgrid_reverted_groups WHERE group_id = ?", group).Exec(ctx); err != nil {
			t.Errorf("cleanup tombstone: %v", err)
		}
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

	// GroupDailyOpens aggregates the group's opens in SQL: m1 opened twice (ev1 at
	// now, ev2 a minute later), so total opens across days is 2 and each day has a
	// single unique recipient. Summing across days keeps the assertion stable even
	// when the minute gap straddles a UTC midnight.
	daily, lastEvent, err := store.GroupDailyOpens(ctx, group)
	if err != nil {
		t.Fatalf("GroupDailyOpens: %v", err)
	}
	totalOpens := 0
	for _, d := range daily {
		totalOpens += d.Opens
		if d.UniqueOpens != 1 {
			t.Errorf("GroupDailyOpens day %v UniqueOpens = %d; want 1 (only m1)", d.Date, d.UniqueOpens)
		}
	}
	if totalOpens != 2 {
		t.Errorf("GroupDailyOpens total opens = %d; want 2", totalOpens)
	}
	if lastEvent == nil {
		t.Error("GroupDailyOpens lastEvent is nil; want the last open instant")
	} else if diff := lastEvent.Sub(now.Add(time.Minute)); diff > time.Second || diff < -time.Second {
		t.Errorf("GroupDailyOpens lastEvent = %v; want ~%v", lastEvent, now.Add(time.Minute))
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
		if _, err := db.NewRaw("DELETE FROM sendgrid_open_events WHERE email_id = ?", m3).Exec(ctx); err != nil {
			t.Errorf("cleanup heal open_events: %v", err)
		}
		if _, err := db.NewRaw("DELETE FROM sendgrid_recipient_engagement WHERE group_id = ?", healGroup).Exec(ctx); err != nil {
			t.Errorf("cleanup heal engagement: %v", err)
		}
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

	// RevertGroup tombstones the group and purges its engagement rows and open
	// events (cleanup after a fully-failed send reverts), leaving other groups
	// intact.
	must(t, store.RevertGroup(ctx, group))
	recs, err = store.RecipientsByGroupID(ctx, group)
	if err != nil {
		t.Fatalf("RecipientsByGroupID after revert: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("group records after revert = %d; want 0", len(recs))
	}
	if opens, err := store.RecipientByEmailID(ctx, m1); err == nil {
		t.Errorf("m1 should be gone after group revert, got %+v", opens)
	}
	if rec3b, err := store.RecipientByEmailID(ctx, m3); err != nil || rec3b == nil {
		t.Errorf("the other group (healGroup) must survive the revert: %v", err)
	}

	// Tombstone durability: a delayed webhook for the reverted group must NOT
	// recreate a row (its self-healing upsert is rejected by the BEFORE INSERT
	// trigger), so purged recipient PII stays purged.
	must(t, store.ApplyDelivered(ctx, m1, group, "a@x.io", now))
	must(t, store.ApplyOpen(ctx, group+"-late-ev", m1, group, "a@x.io", now))
	must(t, store.ApplyFailed(ctx, m2, group, "b@x.io", now))
	if recs, err := store.RecipientsByGroupID(ctx, group); err != nil {
		t.Fatalf("RecipientsByGroupID after late webhooks: %v", err)
	} else if len(recs) != 0 {
		t.Errorf("late webhooks recreated %d row(s) for a reverted group; want 0 (tombstone must reject them)", len(recs))
	}
	// The late open must also leave no orphan open-event row (ApplyOpen drops it
	// for a tombstoned group before inserting).
	var lateOpens int
	must(t, db.NewRaw("SELECT COUNT(*) FROM sendgrid_open_events WHERE sg_event_id = ?", group+"-late-ev").Scan(ctx, &lateOpens))
	if lateOpens != 0 {
		t.Errorf("late open for a reverted group left %d orphan open-event row(s); want 0", lateOpens)
	}

	// A brand-new (non-reverted) group is unaffected by the tombstone.
	liveGroup := group + "-live"
	mLive := liveGroup + "-m"
	t.Cleanup(func() {
		if _, err := db.NewRaw("DELETE FROM sendgrid_recipient_engagement WHERE group_id = ?", liveGroup).Exec(ctx); err != nil {
			t.Errorf("cleanup live engagement: %v", err)
		}
	})
	must(t, store.ApplyDelivered(ctx, mLive, liveGroup, "d@x.io", now))
	if rec, err := store.RecipientByEmailID(ctx, mLive); err != nil || rec == nil || !rec.Delivered {
		t.Errorf("a live group's webhook must still self-heal a row: %+v, %v", rec, err)
	}
}

// TestSchemaMigration_SendProviderUpgradePath verifies the send_provider
// migration repairs an EXISTING database, not just a fresh one: a column left
// nullable with a NULL row by a prior partial run must be backfilled and made
// NOT NULL with a default, idempotently.
//
// The simulation drops constraints/defaults on the newsletters table, so it runs
// against a throwaway DATABASE created for this test alone, never the shared one
// named in DATABASE_URL. A separate database (rather than a schema) gives
// schema.Apply a pristine public schema, matching how it runs in production and
// avoiding its conname-only constraint idempotency matching a same-named
// constraint in another schema. The database is dropped in cleanup even if the
// test aborts mid-way.
func TestSchemaMigration_SendProviderUpgradePath(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()

	// dbName is digits + an underscore prefix (from the clock), so it is a safe
	// identifier to interpolate into the CREATE/DROP DATABASE statements.
	dbName := "mig_test_" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")

	// admin pool connects to the database named in DATABASE_URL and owns the
	// throwaway database's lifecycle (CREATE/DROP DATABASE cannot run in a tx, so
	// these single Execs run in autocommit).
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool (admin): %v", err)
	}
	t.Cleanup(admin.Close) // registered first -> runs last
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		// WITH (FORCE) terminates any lingering connection so the drop cannot hang
		// (Postgres 13+); the working pool is already closed by the cleanup below.
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database: %v", err)
		}
	})

	// The working pool targets the throwaway database. schema.Apply builds a fresh
	// schema there and the migration's ALTERs hit that isolated copy.
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.ConnConfig.Database = dbName
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgxpool (isolated): %v", err)
	}
	t.Cleanup(pool.Close) // runs before the database drop, after the row work
	must(t, schema.Apply(ctx, pool))
	db := bun.NewDB(stdlib.OpenDBFromPool(pool), pgdialect.New())
	t.Cleanup(func() { _ = db.Close() })

	id := "mig-row"

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
	// The CHECK constraint rejects an unknown provider value, so a bad
	// EMAIL_PROVIDER can never be stamped onto a row.
	if err := exec(ctx, db, `INSERT INTO newsletters (project_uid, subject, body_html, ed_reply_email, created_by, send_provider) VALUES ('p','s','b','e@x',?,'bogus-provider')`, id); err == nil {
		t.Error("expected a CHECK violation inserting an invalid send_provider value")
	}
	// A third apply is a no-op (idempotent).
	must(t, schema.Apply(ctx, pool))
}

// TestSendGridRevertConcurrency_Integration exercises both commit orderings of a
// revert racing a webhook, to prove the shared per-group advisory lock (taken by
// RevertGroup and by the engagement-insert trigger / ApplyOpen) leaves no row
// behind either way — the guarantee the lock exists for, not just the sequential
// after-commit case the main test covers.
func TestSendGridRevertConcurrency_Integration(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)
	must(t, schema.Apply(ctx, pool))
	db := bun.NewDB(stdlib.OpenDBFromPool(pool), pgdialect.New())
	t.Cleanup(func() { _ = db.Close() })
	store := repository.NewSendGridEngagementStore(db)

	run := time.Now().UTC().Format("150405.000000000")
	assertPurged := func(what, group, emailID string) {
		t.Helper()
		var eng, opens int
		must(t, db.NewRaw("SELECT COUNT(*) FROM sendgrid_recipient_engagement WHERE group_id = ?", group).Scan(ctx, &eng))
		must(t, db.NewRaw("SELECT COUNT(*) FROM sendgrid_open_events WHERE email_id = ?", emailID).Scan(ctx, &opens))
		if eng != 0 || opens != 0 {
			t.Errorf("%s: survived a revert with engagement=%d open_events=%d; want 0/0", what, eng, opens)
		}
	}

	// Ordering 1 — revert commits before the webhook. The tombstone is visible, so
	// the engagement insert (trigger) and the open (ApplyOpen check) are both
	// rejected: no row is created.
	g1, m1 := "conc1-"+run, "conc1-"+run+"-m"
	t.Cleanup(func() { _, _ = db.NewRaw("DELETE FROM sendgrid_reverted_groups WHERE group_id = ?", g1).Exec(ctx) })
	must(t, store.RevertGroup(ctx, g1))
	must(t, store.ApplyDelivered(ctx, m1, g1, "a@x.io", time.Now().UTC()))
	must(t, store.ApplyOpen(ctx, g1+"-ev", m1, g1, "a@x.io", time.Now().UTC()))
	assertPurged("revert-before-webhook", g1, m1)

	// Ordering 2 — the webhook commits before the revert, concurrently. A webhook
	// transaction inserts its open event + engagement row (the engagement insert
	// takes the per-group advisory lock) but does NOT commit. A concurrent
	// RevertGroup then blocks on that lock. Once the webhook commits, the revert
	// proceeds and must delete the just-committed rows.
	g2, m2 := "conc2-"+run, "conc2-"+run+"-m"
	t.Cleanup(func() {
		_, _ = db.NewRaw("DELETE FROM sendgrid_open_events WHERE email_id = ?", m2).Exec(ctx)
		_, _ = db.NewRaw("DELETE FROM sendgrid_recipient_engagement WHERE group_id = ?", g2).Exec(ctx)
		_, _ = db.NewRaw("DELETE FROM sendgrid_reverted_groups WHERE group_id = ?", g2).Exec(ctx)
	})

	tx1, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin webhook tx: %v", err)
	}
	if _, err := tx1.NewRaw("INSERT INTO sendgrid_open_events (sg_event_id, email_id, opened_at) VALUES (?, ?, ?)", g2+"-ev", m2, time.Now().UTC()).Exec(ctx); err != nil {
		t.Fatalf("webhook open-event insert: %v", err)
	}
	// This engagement insert fires the trigger, which takes the per-group advisory
	// lock; tx1 holds it until it commits below.
	if _, err := tx1.NewRaw("INSERT INTO sendgrid_recipient_engagement (email_id, group_id, to_email, opened, open_count) VALUES (?, ?, ?, TRUE, 1)", m2, g2, "b@x.io").Exec(ctx); err != nil {
		t.Fatalf("webhook engagement insert: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- store.RevertGroup(ctx, g2) }()

	// Wait until the revert is genuinely blocked on the advisory lock (a not-yet-
	// granted advisory lock), so we commit tx1 only after the race is set up.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		must(t, db.NewRaw("SELECT COUNT(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted").Scan(ctx, &waiting))
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			_ = tx1.Rollback()
			t.Fatal("RevertGroup did not block on the advisory lock within 5s")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := tx1.Commit(); err != nil {
		t.Fatalf("commit webhook tx: %v", err)
	}
	select {
	case err := <-done:
		must(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("RevertGroup did not complete after the webhook committed")
	}
	assertPurged("webhook-before-revert", g2, m2)
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
