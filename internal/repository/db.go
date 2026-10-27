// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"database/sql"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
)

// NewBunDB builds the bun.DB every store in this service runs on. Production
// wiring and the integration-test harnesses must all go through it so the
// tests exercise the same configuration the binary ships with.
//
// WithDiscardUnknownColumns makes RETURNING "*" scans tolerate columns the
// compiled model does not know. This is rolling-deploy resilience: when a new
// pod applies an additive schema migration (ADD COLUMN IF NOT EXISTS) while an
// older binary is still serving, the old pod's writes would otherwise fail
// scanning the extra column into its model, a 500 after an autocommitted
// write, risking duplicate or stranded state. Discarding unknown columns keeps
// every pod forward-compatible with an added column across the deploy window.
//
// Trade-off: the option applies to every scan on this DB, not only to
// RETURNING "*" writes. A raw query that selects a misnamed or renamed column
// no longer fails at scan time; the model field is left at its zero value.
// Cover new raw queries with an integration test that asserts the field.
func NewBunDB(sqlDB *sql.DB) *bun.DB {
	return bun.NewDB(sqlDB, pgdialect.New(), bun.WithDiscardUnknownColumns())
}
