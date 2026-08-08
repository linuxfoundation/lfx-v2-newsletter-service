// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

// PostgresSubscriptionRepo persists Subscription rows in PostgreSQL via bun.
// It backs cmd/newsletter-import; no HTTP or NATS handler uses it yet.
type PostgresSubscriptionRepo struct {
	db *bun.DB
}

// NewPostgresSubscriptionRepo wires a Subscription repository over the given bun.DB.
func NewPostgresSubscriptionRepo(db *bun.DB) *PostgresSubscriptionRepo {
	return &PostgresSubscriptionRepo{db: db}
}

// ImportBatch bulk-inserts rows for listID, skipping any row whose
// (list_id, email) pair already exists so a re-run of the importer (or an
// overlapping export) never overwrites a row's current subscribed state —
// see the comment on port.SubscriptionRepository.ImportBatch. Returns the
// number of rows actually inserted (RowsAffected on an INSERT ... ON CONFLICT
// DO NOTHING statement counts only the rows that were not skipped).
func (r *PostgresSubscriptionRepo) ImportBatch(ctx context.Context, listID string, rows []model.Subscription) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	for i := range rows {
		rows[i].ListID = listID
	}

	res, err := r.db.NewInsert().
		Model(&rows).
		On("CONFLICT (list_id, email) DO NOTHING").
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("import subscription batch: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("import subscription batch (rows affected): %w", err)
	}
	return int(affected), nil
}
