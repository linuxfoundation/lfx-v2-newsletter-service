// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package port

import (
	"context"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

// ShareRepository persists in-app newsletter share intents and reads back
// share counts.
type ShareRepository interface {
	// RecordShare inserts a share. Idempotent per (newsletter, user, channel):
	// a repeat is a silent no-op.
	RecordShare(ctx context.Context, share *model.NewsletterShare) error
	// ShareCounts returns the total share count for each of the given
	// newsletters. Newsletters with no shares are absent from the map.
	ShareCounts(ctx context.Context, newsletterIDs []uuid.UUID) (map[uuid.UUID]int, error)
	// ShareStats returns one newsletter's share count in total and per channel.
	ShareStats(ctx context.Context, newsletterID uuid.UUID) (*model.ShareStats, error)
}
