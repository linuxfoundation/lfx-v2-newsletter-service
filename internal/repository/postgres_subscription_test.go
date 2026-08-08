//go:build integration

// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/schema"
)

// newIntegrationSubscriptionRepo connects, applies the embedded schema, and
// returns a repository over a clean newsletter_subscriptions table. Mirrors
// newIntegrationRepo in postgres_integration_test.go, including its
// current_database() safety guard, but truncates newsletter_subscriptions
// instead of newsletters.
func newIntegrationSubscriptionRepo(t *testing.T) *PostgresSubscriptionRepo {
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

	if _, err := db.ExecContext(ctx, "TRUNCATE newsletter_subscriptions"); err != nil {
		t.Fatalf("truncate newsletter_subscriptions: %v", err)
	}
	return NewPostgresSubscriptionRepo(db)
}

func TestPostgresSubscriptionRepo_ImportBatch_InsertsNewRows(t *testing.T) {
	repo := newIntegrationSubscriptionRepo(t)
	ctx := context.Background()

	rows := []model.Subscription{
		{Email: "a@example.com", Subscribed: true, Source: model.SubscriptionSourceImport},
		{Email: "b@example.com", Subscribed: false, Source: model.SubscriptionSourceImport},
	}

	inserted, err := repo.ImportBatch(ctx, model.ListAAIFUserCommunity, rows)
	if err != nil {
		t.Fatalf("ImportBatch: %v", err)
	}
	if inserted != 2 {
		t.Fatalf("inserted = %d, want 2", inserted)
	}
}

func TestPostgresSubscriptionRepo_ImportBatch_ConflictDoesNotOverwrite(t *testing.T) {
	repo := newIntegrationSubscriptionRepo(t)
	ctx := context.Background()

	first := []model.Subscription{
		{Email: "c@example.com", Subscribed: false, Source: model.SubscriptionSourceImport},
	}
	if _, err := repo.ImportBatch(ctx, model.ListAAIFUserCommunity, first); err != nil {
		t.Fatalf("first ImportBatch: %v", err)
	}

	// A second import with the same (list_id, email) but a different
	// subscribed value must not flip the already-stored state — the unique
	// index on (list_id, email) plus ON CONFLICT DO NOTHING is the only thing
	// standing between this importer and silently resubscribing someone who
	// already opted out upstream.
	second := []model.Subscription{
		{Email: "c@example.com", Subscribed: true, Source: model.SubscriptionSourceImport},
	}
	inserted, err := repo.ImportBatch(ctx, model.ListAAIFUserCommunity, second)
	if err != nil {
		t.Fatalf("second ImportBatch: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("inserted = %d, want 0 (conflict must be skipped)", inserted)
	}

	var subscribed bool
	if err := repo.db.NewSelect().
		Model((*model.Subscription)(nil)).
		Column("subscribed").
		Where("list_id = ?", model.ListAAIFUserCommunity).
		Where("email = ?", "c@example.com").
		Scan(ctx, &subscribed); err != nil {
		t.Fatalf("select subscribed: %v", err)
	}
	if subscribed {
		t.Fatalf("subscribed = true, want false (original row must survive the conflicting re-import)")
	}
}

func TestPostgresSubscriptionRepo_ImportBatch_Batching(t *testing.T) {
	repo := newIntegrationSubscriptionRepo(t)
	ctx := context.Background()

	const batchSize = 500
	rows := make([]model.Subscription, 0, batchSize)
	for i := 0; i < batchSize; i++ {
		rows = append(rows, model.Subscription{
			Email:      fmt.Sprintf("user%03d@example.com", i),
			Subscribed: true,
			Source:     model.SubscriptionSourceImport,
		})
	}

	inserted, err := repo.ImportBatch(ctx, model.ListAAIFUserCommunity, rows)
	if err != nil {
		t.Fatalf("ImportBatch: %v", err)
	}
	if inserted != batchSize {
		t.Fatalf("inserted = %d, want %d", inserted, batchSize)
	}

	count, err := repo.db.NewSelect().
		Model((*model.Subscription)(nil)).
		Where("list_id = ?", model.ListAAIFUserCommunity).
		Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != batchSize {
		t.Fatalf("count = %d, want %d", count, batchSize)
	}
}
