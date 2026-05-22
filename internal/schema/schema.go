// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package schema embeds the newsletter-service schema.sql and applies it
// idempotently at service startup.
package schema

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var sql string

// advisoryLockKey is an arbitrary 64-bit constant used with
// pg_advisory_xact_lock to serialize concurrent pods during bootstrap.
const advisoryLockKey int64 = 0x4E45_5753_4C54_5253 // "NEWSLTRS"

// Apply runs the embedded schema.sql in a single transaction, gated by a
// Postgres transaction-scoped advisory lock so concurrent pod startups don't
// race on CREATE statements. The schema is idempotent (CREATE … IF NOT EXISTS).
func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin schema tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Bound the lock acquisition: pg_advisory_xact_lock blocks indefinitely
	// by default, so a hung peer pod would stall every subsequent rollout.
	// SET LOCAL applies the timeout to this transaction only.
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '60s'"); err != nil {
		return fmt.Errorf("set statement timeout: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", advisoryLockKey); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}

	slog.InfoContext(ctx, "applying database schema")
	if _, err := tx.Exec(ctx, sql); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit schema tx: %w", err)
	}
	slog.InfoContext(ctx, "database schema applied")
	return nil
}
