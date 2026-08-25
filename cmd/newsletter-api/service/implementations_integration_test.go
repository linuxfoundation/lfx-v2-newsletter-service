// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/schema"
)

// legacyPublicationStruct mirrors only the columns a "legacy" struct (one
// written before some column existed) would know about — a small subset of
// newsletter_publications, which the real schema carries many more columns
// for. Scanning a Returning("*") row for this table into this struct is
// exactly the shape of scan an old pod performs against a schema a newer pod
// has already migrated.
type legacyPublicationStruct struct {
	bun.BaseModel `bun:"table:newsletter_publications,alias:p"`

	ID         uuid.UUID `bun:"id,pk"`
	ProjectUID string    `bun:"project_uid"`
	Slug       string    `bun:"slug"`
	Name       string    `bun:"name"`
	CreatedBy  string    `bun:"created_by"`
}

// newIntegrationSQLDB connects to NEWSLETTER_TEST_DATABASE_URL, applies the
// embedded schema, and returns a *sql.DB, or skips when the env var is unset
// — the same untagged, env-gated pattern as
// internal/repository/postgres_integration_test.go, which is what lets this
// run automatically in CI's "Test" step
// (.github/workflows/newsletter-api-build.yml) without a separate
// -tags integration invocation. Mirrors that file's destructive-database
// guard and schema.Apply call too: this package's test binary runs
// concurrently with internal/repository's under a plain `go test ./...`, so
// it cannot assume the other package's Apply has already run against a fresh
// database — schema.Apply is idempotent and advisory-locked, so calling it
// again here is safe.
func newIntegrationSQLDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("NEWSLETTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWSLETTER_TEST_DATABASE_URL not set — skipping Postgres-backed bun.DB tests")
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
		t.Fatalf("refusing tests against database %q — NEWSLETTER_TEST_DATABASE_URL must point at a dedicated test database whose name contains \"test\"", dbName)
	}

	if err := schema.Apply(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("apply schema: %v", err)
	}

	// Named testSQLDB at the call site, not sqlDB: this package also has a
	// package-level production singleton var sqlDB *sql.DB
	// (implementations.go), and a local sqlDB here would shadow it silently.
	testSQLDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() {
		_ = testSQLDB.Close()
		pool.Close()
	})
	return testSQLDB
}

// TestNewBunDB_DiscardsUnknownColumns proves newBunDB — the exact constructor
// production wiring calls — tolerates a Returning("*") row carrying columns
// its struct doesn't map, which is the rolling-deploy scenario this
// constructor exists to make safe. A control case against a bun.DB built
// without the option confirms the test is not vacuously passing: the same
// insert on the same schema fails without WithDiscardUnknownColumns.
//
// Uses a per-run unique project_uid and deletes every row it inserts (rather
// than truncating the table), since this package's tests run as a separate
// binary from internal/repository's under a plain `go test ./...` and must
// not disturb that package's own publication rows.
func TestNewBunDB_DiscardsUnknownColumns(t *testing.T) {
	testSQLDB := newIntegrationSQLDB(t)
	ctx := context.Background()
	projectUID := "newbundb-test-" + uuid.NewString()

	insert := func(db *bun.DB) error {
		pub := &legacyPublicationStruct{
			ID:         uuid.New(),
			ProjectUID: projectUID,
			Slug:       "legacy-" + uuid.NewString(),
			Name:       "Legacy",
			CreatedBy:  "tester",
		}
		_, err := db.NewInsert().Model(pub).Returning("*").Exec(ctx)
		t.Cleanup(func() {
			// Best-effort, unconditional on err: the insert can commit even when
			// Exec returns an error, since the scan runs after Postgres has
			// already committed the row.
			if _, delErr := testSQLDB.ExecContext(context.Background(), "DELETE FROM newsletter_publications WHERE id = $1", pub.ID); delErr != nil {
				t.Logf("cleanup: delete test publication %s: %v", pub.ID, delErr)
			}
		})
		return err
	}

	t.Run("newBunDB discards the columns the legacy struct doesn't map", func(t *testing.T) {
		db := newBunDB(testSQLDB)
		if err := insert(db); err != nil {
			t.Fatalf("newBunDB insert with Returning(\"*\") should tolerate unknown columns, got: %v", err)
		}
	})

	t.Run("control: a bun.DB without the option fails the same insert", func(t *testing.T) {
		db := bun.NewDB(testSQLDB, pgdialect.New())
		err := insert(db)
		if err == nil {
			t.Fatal("expected a scan error without WithDiscardUnknownColumns — the control proves nothing if this passes")
		}
		if !strings.Contains(err.Error(), "does not have column") {
			t.Fatalf("expected a bun \"does not have column\" scan error, got: %v", err)
		}
	})
}
