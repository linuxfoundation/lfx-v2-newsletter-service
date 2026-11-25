// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/repository"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/schema"
)

// TestNewBunDB_ToleratesUnknownColumn pins the rollout compatibility of the
// production bun handle: with a column this build does not model added to
// newsletters (as a newer release adds one, e.g. body_layout), create, update
// and get still work. The column name is one no release models, so the test
// stays meaningful after body_layout itself is in the model.
//
// It runs against a throwaway DATABASE, never the shared one, so the extra
// column never reaches the tables the strict repository tests use. A separate
// database (rather than a schema) gives schema.Apply a pristine public schema
// and avoids its conname-only constraint idempotency matching a same-named
// constraint in another schema, as in TestSchemaMigration_SendProviderUpgradePath.
func TestNewBunDB_ToleratesUnknownColumn(t *testing.T) {
	dsn := os.Getenv("NEWSLETTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWSLETTER_TEST_DATABASE_URL not set — skipping Postgres-backed rollout test")
	}
	ctx := context.Background()

	// admin connects to the database named in the DSN and owns the throwaway
	// database's lifecycle.
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool (admin): %v", err)
	}
	t.Cleanup(admin.Close) // registered first -> runs last
	var sharedName string
	if err := admin.QueryRow(ctx, "SELECT current_database()").Scan(&sharedName); err != nil {
		t.Fatalf("identify database: %v", err)
	}
	if !strings.Contains(sharedName, "test") {
		t.Fatalf("refusing to run against database %q — NEWSLETTER_TEST_DATABASE_URL must point at a test database whose name contains \"test\"", sharedName)
	}

	// dbName is lowercase hex behind a fixed prefix, so it is a safe identifier
	// to interpolate into the CREATE/DROP DATABASE statements.
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("random database name: %v", err)
	}
	dbName := "rollout_test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		// A local role without CREATEDB skips rather than failing make test; CI
		// connects as the postgres superuser, so the test always runs there.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42501" {
			t.Skipf("role cannot CREATE DATABASE (needs CREATEDB): %v", err)
		}
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		// WITH (FORCE) terminates any lingering connection so the drop cannot
		// hang; the working pool is already closed by the cleanup below.
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database: %v", err)
		}
	})

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.ConnConfig.Database = dbName
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgxpool (isolated): %v", err)
	}
	t.Cleanup(pool.Close) // runs before the database drop

	if err := schema.Apply(ctx, pool); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := pool.Exec(ctx, "ALTER TABLE newsletters ADD COLUMN rollout_probe_unmodelled TEXT"); err != nil {
		t.Fatalf("add unmodelled column: %v", err)
	}

	db := newBunDB(stdlib.OpenDBFromPool(pool))
	t.Cleanup(func() { _ = db.Close() })
	repo := repository.NewPostgresNewsletterRepo(db)

	n := &model.Newsletter{
		ProjectUID:    "project-1",
		Subject:       "Rollout",
		BodyHTML:      "<p>body</p>",
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"committee-a"},
		Status:        model.StatusDraft,
		CreatedBy:     "tester",
	}
	if err := repo.Create(ctx, n); err != nil {
		t.Fatalf("Create with an unmodelled column: %v", err)
	}
	n.Subject = "Rollout, edited"
	updated, err := repo.Update(ctx, n, n.Version)
	if err != nil {
		t.Fatalf("Update with an unmodelled column: %v", err)
	}
	got, err := repo.Get(ctx, n.ID)
	if err != nil {
		t.Fatalf("Get with an unmodelled column: %v", err)
	}
	if got.Subject != "Rollout, edited" || got.Version != updated.Version {
		t.Errorf("Get = subject %q version %d, want %q version %d", got.Subject, got.Version, "Rollout, edited", updated.Version)
	}
}
