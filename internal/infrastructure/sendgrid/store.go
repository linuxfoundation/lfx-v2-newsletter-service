// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package sendgrid

import (
	"context"
	"time"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// EngagementStore persists and reads per-recipient SendGrid engagement.
//
// It is the SendGrid-owned equivalent of the email-service engagement KV: with
// SendGrid the newsletter service brokers sends directly, so it also owns
// engagement. The dispatcher records a row per send (RecordSent) so TotalSent is
// accurate before any event arrives, and reads engagement for analytics; the
// event webhook applies delivered / open / failed events as they arrive.
//
// The Apply* methods are idempotent so a webhook redelivery is a no-op:
// ApplyOpen deduplicates on the SendGrid sg_event_id, and ApplyDelivered /
// ApplyFailed are first-event-wins. Method arguments are primitives + port
// types so the repository can implement this without importing this package.
type EngagementStore interface {
	// RecordSent inserts the initial engagement row when a send is accepted.
	// Idempotent on emailID (a re-send with the same id leaves the row intact).
	// Best-effort at the call site: if it fails, the Apply* methods below still
	// recover the row from the first webhook event, so engagement is not lost.
	RecordSent(ctx context.Context, emailID, groupID, to string, sentAt time.Time) error
	// ApplyDelivered marks the recipient delivered (first delivery wins). It
	// upserts on emailID from the event's groupID / to so a missing row (failed
	// RecordSent) is created rather than dropped.
	ApplyDelivered(ctx context.Context, emailID, groupID, to string, at time.Time) error
	// ApplyOpen records one open, deduplicated by sgEventID, and bumps the
	// recipient's open count and last-opened timestamp. Event insert and rollup
	// are one transaction; the rollup upserts on emailID from groupID / to.
	ApplyOpen(ctx context.Context, sgEventID, emailID, groupID, to string, at time.Time) error
	// ApplyFailed marks the recipient failed — bounce / dropped / spamreport
	// (first failure wins). Upserts on emailID from groupID / to.
	ApplyFailed(ctx context.Context, emailID, groupID, to string, at time.Time) error

	// Engagement returns the per-group rollup used by the analytics summary.
	Engagement(ctx context.Context, groupID string) (*port.EmailEngagement, error)
	// RecipientByEmailID returns one recipient's engagement record.
	RecipientByEmailID(ctx context.Context, emailID string) (*port.EmailRecipientRecord, error)
	// RecipientsByGroupID returns every recipient record for a group, carrying
	// per-open timestamps so analytics can build the daily-opens series.
	RecipientsByGroupID(ctx context.Context, groupID string) ([]port.EmailRecipientRecord, error)

	// DeleteByGroupID removes every engagement row and its open events for a
	// group. Used to clean up after a fully-failed send reverts to draft and
	// clears its group_id, so the recorded rows are not orphaned.
	DeleteByGroupID(ctx context.Context, groupID string) error
}
