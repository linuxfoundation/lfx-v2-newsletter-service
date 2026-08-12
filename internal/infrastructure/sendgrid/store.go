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
	// ApplyClick records one click, deduplicated by sgEventID, and bumps the
	// recipient's click count and last-clicked timestamp. Mirrors ApplyOpen's
	// idempotency and self-healing upsert semantics exactly. url is the
	// clicked link, persisted for the top-clicked-links analytics breakdown.
	ApplyClick(ctx context.Context, sgEventID, emailID, groupID, to, url string, at time.Time) error
	// ApplyFailed marks the recipient failed — bounce / dropped / spamreport
	// (first failure wins). Upserts on emailID from groupID / to.
	ApplyFailed(ctx context.Context, emailID, groupID, to string, at time.Time) error

	// Engagement returns the per-group rollup used by the analytics summary.
	Engagement(ctx context.Context, groupID string) (*port.EmailEngagement, error)
	// RecipientByEmailID returns one recipient's engagement record.
	RecipientByEmailID(ctx context.Context, emailID string) (*port.EmailRecipientRecord, error)
	// GroupEngagementDetail computes the group's bounded analytics detail (unique
	// opens, the per-UTC-day opens series, the last open instant, and the failed
	// recipients) with a few SQL aggregates, so no raw per-open data is loaded.
	GroupEngagementDetail(ctx context.Context, groupID string) (*port.GroupEngagementDetail, error)
	// RecipientRecordsByGroupID returns every recipient record for a group with
	// its ascending open-timestamp series, for the per-recipient analytics
	// endpoint. Bounded by the group's audience and its recorded opens.
	RecipientRecordsByGroupID(ctx context.Context, groupID string) ([]port.EmailRecipientRecord, error)

	// RevertGroup tombstones a group and purges its engagement rows and open
	// events, atomically. Used after a fully-failed send reverts to draft and
	// clears its group_id, so the recorded rows are not orphaned. The tombstone
	// durably rejects any delayed webhook that would otherwise self-heal a purged
	// row and re-persist recipient PII under a group no newsletter references.
	RevertGroup(ctx context.Context, groupID string) error
}
