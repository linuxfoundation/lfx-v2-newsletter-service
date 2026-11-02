// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

// RecordShare inserts a share intent. The (newsletter_id, user_hash, channel)
// primary key makes a repeat by the same user to the same channel a no-op.
func (r *PostgresNewsletterRepo) RecordShare(ctx context.Context, share *model.NewsletterShare) error {
	if _, err := r.db.NewInsert().
		Model(share).
		On("CONFLICT DO NOTHING").
		Exec(ctx); err != nil {
		return fmt.Errorf("record share: %w", err)
	}
	return nil
}

// ShareCounts returns the total share count per newsletter for the given ids
// in one grouped query. Newsletters with no shares are absent from the map.
func (r *PostgresNewsletterRepo) ShareCounts(ctx context.Context, newsletterIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	counts := make(map[uuid.UUID]int, len(newsletterIDs))
	if len(newsletterIDs) == 0 {
		return counts, nil
	}
	var rows []struct {
		NewsletterID uuid.UUID `bun:"newsletter_id"`
		Shares       int       `bun:"shares"`
	}
	if err := r.db.NewSelect().
		Table("newsletter_shares").
		Column("newsletter_id").
		ColumnExpr("COUNT(*) AS shares").
		Where("newsletter_id IN (?)", bun.In(newsletterIDs)).
		Group("newsletter_id").
		Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("count shares: %w", err)
	}
	for _, row := range rows {
		counts[row.NewsletterID] = row.Shares
	}
	return counts, nil
}

// ShareStats returns one newsletter's share count, in total and per channel.
func (r *PostgresNewsletterRepo) ShareStats(ctx context.Context, newsletterID uuid.UUID) (*model.ShareStats, error) {
	var rows []struct {
		Channel model.ShareChannel `bun:"channel"`
		Shares  int                `bun:"shares"`
	}
	if err := r.db.NewSelect().
		Table("newsletter_shares").
		Column("channel").
		ColumnExpr("COUNT(*) AS shares").
		Where("newsletter_id = ?", newsletterID).
		Group("channel").
		Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("share stats: %w", err)
	}
	stats := &model.ShareStats{ByChannel: make(map[model.ShareChannel]int, len(rows))}
	for _, row := range rows {
		stats.ByChannel[row.Channel] = row.Shares
		stats.Total += row.Shares
	}
	return stats, nil
}
