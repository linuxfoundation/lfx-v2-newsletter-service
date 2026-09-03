// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package port defines the inbound and outbound interfaces the service depends
// on. Concrete implementations live in internal/infrastructure or
// internal/repository.
package port

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

// MaxOpensPerRecipient is the per-recipient open-event cap applied
// provider-independently in the service layer. Each recipient's open
// list is truncated to the most recent 500 to bound response size.
const MaxOpensPerRecipient = 500

// MaxClicksPerRecipient mirrors MaxOpensPerRecipient for the per-recipient
// click-event list.
const MaxClicksPerRecipient = 500

// MaxTopLinks bounds the top-clicked-links analytics breakdown. The raw click
// events keep every row, so raising this is a query change, not a data-loss one.
const MaxTopLinks = 20

// ListFilters narrows a newsletter listing query.
//
// Statuses is optional: if empty, newsletters in every state are returned.
// Multiple values match any of them (the service layer maps the public
// 'sent' filter to [sent, sending] so in-flight sends appear on the Sent tab).
// PageToken is the opaque cursor returned in the previous page's response.
type ListFilters struct {
	ProjectUID string
	Statuses   []model.Status
	PageToken  string
	Limit      int
}

// ListPage is one page of newsletters plus an optional NextPageToken for
// continuation.
type ListPage struct {
	Newsletters   []*model.Newsletter
	NextPageToken string
}

// NewsletterRepository persists Newsletter aggregates.
//
// Implementations must surface optimistic-locking conflicts as
// domain.ErrVersionMismatch and missing records as domain.ErrNotFound.
type NewsletterRepository interface {
	Create(ctx context.Context, n *model.Newsletter) error
	Get(ctx context.Context, id uuid.UUID) (*model.Newsletter, error)
	List(ctx context.Context, projectUID string) ([]*model.Newsletter, error)
	ListAll(ctx context.Context, filters ListFilters) (*ListPage, error)
	// ListSentByCommittee returns a page of sent newsletters whose audience
	// includes the given committee, ordered by sent_at DESC. Backs the
	// committee-scoped read API (member-facing "my newsletters" feeds); the
	// sent-only filter is intentional — drafts must never be visible to
	// committee members.
	ListSentByCommittee(ctx context.Context, committeeUID string, pageToken string) (*ListPage, error)
	Update(ctx context.Context, n *model.Newsletter, expectedVersion int64) (*model.Newsletter, error)
	Delete(ctx context.Context, id uuid.UUID) error

	// MarkSending atomically transitions draft → sending, persisting the
	// group_id, the dispatching provider (send_provider), and the resolved
	// audience size. The single optimistically-locked UPDATE (gated on id,
	// expectedVersion, and status='draft') is the duplicate-send guard across
	// replicas: a zero-row result is classified as domain.ErrNotFound,
	// domain.ErrAlreadySent, domain.ErrSendInProgress, or
	// domain.ErrVersionMismatch.
	//
	// scheduledAt/batchID are non-nil/non-empty only when this fan-out is
	// arming a scheduled release (LFXV2-2685); both persist atomically with the
	// draft → sending claim so a crash immediately after this UPDATE still
	// carries enough state for RecoverStuckSending to settle the right way.
	MarkSending(ctx context.Context, id uuid.UUID, groupID, sendProvider string, totalRecipients int, expectedVersion int64, scheduledAt *time.Time, batchID string) (*model.Newsletter, error)
	// MarkSent transitions sending → sent, gated on the version returned by
	// MarkSending. group_id and total_recipients were already persisted at the
	// sending transition.
	MarkSent(ctx context.Context, id uuid.UUID, sentAt time.Time, expectedVersion int64) (*model.Newsletter, error)
	// MarkScheduled transitions sending → scheduled, gated on the version
	// returned by MarkSending (LFXV2-2685). Used instead of MarkSent when the
	// fan-out just armed a scheduled release: scheduled_at and batch_id were
	// already persisted at the sending transition.
	MarkScheduled(ctx context.Context, id uuid.UUID, expectedVersion int64) (*model.Newsletter, error)
	// RevertSending returns a sending newsletter to draft after a total
	// fan-out failure (zero recipients delivered) so the operator can retry.
	RevertSending(ctx context.Context, id uuid.UUID) error
	// RevertScheduled cancels an armed schedule and returns the newsletter to
	// draft, gated on expectedVersion since this transition is user-initiated
	// (LFXV2-2685) rather than a crash-recovery sweep. Clears group_id/batch_id
	// and resets total_recipients, but deliberately RETAINS scheduled_at — the
	// cancelled newsletter still carries the author's saved intent, which they
	// may edit or re-arm. batchID ties the revert to the specific batch that was
	// cancelled, preventing a delayed duplicate cancel from reverting a newer send.
	RevertScheduled(ctx context.Context, id uuid.UUID, expectedVersion int64, batchID *string) (*model.Newsletter, error)
	// SettleDueScheduled marks scheduled rows whose scheduled_at has passed as
	// sent (LFXV2-2685). Reconciliation of our display state only — SendGrid
	// owns the actual release timing. Returns the number of settled rows.
	SettleDueScheduled(ctx context.Context, now time.Time) (int64, error)
	// RecoverStuckSending marks sending rows older than the given age as sent,
	// except rows arming a scheduled release (scheduled_at IS NOT NULL), which
	// settle to scheduled instead (LFXV2-2685) — flipping those straight to
	// sent would be wrong since the provider is still holding the release.
	// Crash recovery: a pod dying mid-fan-out leaves status='sending' forever;
	// after the TTL an unknown number of emails may already have gone out, so
	// re-arming Send would guarantee duplicates — marking sent at worst
	// under-reports a remainder that analytics (via group_id) makes visible.
	// Known sub-case where sent-over-draft over-reports instead: a total
	// fan-out failure whose RevertSending write also failed (double fault)
	// leaves a zero-delivery row that the sweep settles as sent; the sweep
	// cannot distinguish it from a crash mid-fan-out, and analytics (zero
	// delivered under the group_id) exposes it for manual repair.
	// Returns the number of recovered rows.
	RecoverStuckSending(ctx context.Context, olderThan time.Duration) (int64, error)

	// Open tracking
	RecordOpen(ctx context.Context, newsletterID uuid.UUID, recipientHash string) error
	Analytics(ctx context.Context, newsletterID uuid.UUID) (*model.Analytics, error)
}

// UnsubscribeRepository persists project-scoped opt-outs.
//
// CreateUnsubscribe must be idempotent: a second unsubscribe for the same
// (project_uid, email) pair must succeed silently.
type UnsubscribeRepository interface {
	CreateUnsubscribe(ctx context.Context, projectUID, email string) error
	ListUnsubscribedEmails(ctx context.Context, projectUID string) (map[string]struct{}, error)
	ListUnsubscribes(ctx context.Context, projectUID string) ([]*model.NewsletterUnsubscribe, error)
	DeleteUnsubscribe(ctx context.Context, projectUID string, id uuid.UUID) error
}

// CommitteeClient resolves committee members for newsletter recipient calculation.
//
// The concrete implementation talks to lfx-v2-committee-service via the
// `lfx.committee-api.list_members` NATS subject. No auth context flows through
// — NATS is network-isolated and authorization is enforced at the inbound
// gateway before the request ever reaches newsletter-service.
type CommitteeClient interface {
	ListMembers(ctx context.Context, committeeUID string) ([]model.CommitteeMember, error)
}

// ProjectMetadataClient resolves the project name and slug used for email
// chrome (subject line variables, header branding, compliance footer).
//
// Backed by NATS subjects `lfx.projects-api.get_name` and `lfx.projects-api.get_slug`
// exposed by lfx-v2-projects-service. The pattern mirrors committee-service's
// project_retriever client.
type ProjectMetadataClient interface {
	Name(ctx context.Context, projectUID string) (string, error)
	Slug(ctx context.Context, projectUID string) (string, error)
}

// UserMetadataReader resolves a human display name for the authenticated user
// by their JWT principal. Backed by the NATS subject
// `lfx.auth-service.user_metadata.read` (lfx-v2-auth-service). The principal
// is the validated `sub`/`principal` claim from the inbound JWT — never a raw
// HTTP header — so the result can be trusted in user-visible contexts such as
// the SMTP From display name and the email body signature.
type UserMetadataReader interface {
	Name(ctx context.Context, principal string) (string, error)
}

// UserEmailReader resolves the authenticated user's primary email address by
// their JWT principal. Backed by the NATS subject
// `lfx.auth-service.user_emails.read` (lfx-v2-auth-service). Used to prefer
// the send-time sender's own address as Reply-To, from the same principal
// that drives the From display name — but only when the address resolves,
// parses, and passes the Reply-To domain allowlist; the orchestrator falls
// back to the draft's stored `ed_reply_email` otherwise, so Reply-To does
// not unconditionally track the sender.
type UserEmailReader interface {
	PrimaryEmail(ctx context.Context, principal string) (string, error)
}

// SendEmailInput is one per-recipient send envelope dispatched to email-service.
//
// From, FromDisplayName, and ReplyTo are optional: when empty, email-service
// applies its configured service defaults and (for ReplyTo) omits the SMTP
// Reply-To header entirely. Domains on From and ReplyTo must be in the
// email-service allowlist — enforcement lives there, not here.
//
// ListUnsubscribeURL and ListUnsubscribePost are optional and carry the RFC 8058
// one-click unsubscribe link for this recipient. Callers set them from
// unsub.BuildURL when the unsubscribe feature is enabled.
type SendEmailInput struct {
	To              string
	Subject         string
	HTML            string
	Text            string
	From            string
	FromDisplayName string
	ReplyTo         string
	GroupID         string
	// ListUnsubscribeURL and ListUnsubscribePost carry the recipient's unsubscribe
	// intent (LFXV2-2581). A non-empty ListUnsubscribeURL produces the RFC 2369
	// List-Unsubscribe header; the RFC 8058 one-click List-Unsubscribe-Post header
	// is added only when ListUnsubscribePost is ALSO true and (per RFC 8058) the
	// URL is https. The SendGrid dispatcher emits these headers today. The
	// NATS/email-service dispatcher forwards both fields, but the pinned
	// email-service ignores them, so the default provider does not emit the
	// headers until email-service adds support.
	ListUnsubscribeURL  string
	ListUnsubscribePost bool
	// SendAt and BatchID arm a scheduled release: when SendAt is non-nil, every
	// recipient in the fan-out carries the same SendAt/BatchID so the provider
	// holds and releases them together (LFXV2-2685). Nil/empty means send
	// immediately — the historical behavior. Only a dispatcher implementing
	// ScheduledSender can be asked to populate these.
	SendAt  *time.Time
	BatchID string
}

// EmailRecipientRecord mirrors lfx-v2-email-service's per-recipient state, used
// when aggregating analytics. Fields are kept loose because newsletter-service
// only reads a subset.
//
// OpenedAtList holds every observed open timestamp for this recipient when
// email-service exposes the per-event series; with older email-service builds
// that only emit a flat `opened_at`, OpenedAtList carries a single element so
// downstream callers can treat the multi-event shape as canonical.
type EmailRecipientRecord struct {
	EmailID       string
	GroupID       string
	To            string
	SentAt        *time.Time
	Delivered     bool
	DeliveredAt   *time.Time
	Opened        bool
	OpenCount     int
	LastOpened    *time.Time
	OpenedAtList  []time.Time
	Clicked       bool
	ClickCount    int
	LastClicked   *time.Time
	ClickedAtList []time.Time
	Failed        bool
	FailedAt      *time.Time
}

// RecipientEngagement pairs one per-recipient engagement record with the
// recipient's display name, resolved best-effort from the newsletter's
// committees at read time. Name is empty when the recipient no longer appears
// in the committees or has no name on file — callers fall back to the
// record's email address.
type RecipientEngagement struct {
	Name   string
	Record EmailRecipientRecord
}

// RecipientEngagementResult is the per-recipient analytics read: the records
// plus an explicit completeness marker, so clients can distinguish a complete
// audience from a best-effort partial one. Both providers' per-recipient
// stores are best-effort (email-service silently omits missing/malformed KV
// records and its group index briefly lags a fresh send; the SendGrid store
// records no row for ambiguous send outcomes), so Complete is false whenever
// fewer records came back than TotalRecipients, the newsletter's
// authoritative send-time audience snapshot.
type RecipientEngagementResult struct {
	TotalRecipients int
	Complete        bool
	Recipients      []RecipientEngagement
}

// EmailEngagement is the per-group engagement rollup for a sent newsletter,
// keyed by group_id. It is provider-agnostic: the email-service (NATS) reader
// and the SendGrid store-backed reader both return it.
type EmailEngagement struct {
	GroupID      string
	TotalSent    int
	Delivered    int
	Opened       int
	UniqueOpens  int
	Clicked      int
	UniqueClicks int
	Failed       int
}

// EmailDispatcher sends individual emails and reads back engagement for
// analytics. It is the outbound-send abstraction: the email-service
// implementation fans out over NATS (SES), while the SendGrid implementation
// brokers mail/send over HTTPS with a webhook-populated store. Subject/transport
// details live on each implementation, not here. No auth context is propagated —
// see comments on ProjectMetadataClient.
type EmailDispatcher interface {
	SendEmail(ctx context.Context, in SendEmailInput) (emailID string, err error)
	EngagementReader
}

// ErrAmbiguousSend marks a SendEmail failure whose delivery outcome is UNKNOWN:
// the provider may still have accepted the message (a transport error, or a 5xx
// with no guaranteed rejection). A recipient that fails this way must NOT be
// treated as a clean, retry-safe failure — reverting the newsletter to draft and
// retrying could duplicate a message the provider already accepted. Providers
// wrap their ambiguous errors so it is errors.Is-discoverable; a definitive
// rejection (the provider did not accept it) is returned unwrapped.
var ErrAmbiguousSend = errors.New("send outcome ambiguous: provider may have accepted the message")

// EngagementPurger optionally lets a dispatcher delete the engagement rows it
// persisted for a group_id. The orchestrator calls it after a full fan-out
// failure reverts a newsletter to draft and clears its group_id, so the
// provider's now-orphaned per-recipient rows (which hold recipient emails) are
// not leaked. Dispatchers whose engagement lives elsewhere (email-service) need
// not implement it; the orchestrator type-asserts for it.
type EngagementPurger interface {
	PurgeEngagement(ctx context.Context, groupID string) error
}

// ScheduledSender optionally lets a dispatcher arm and cancel a native
// provider-side scheduled send (LFXV2-2685). Only the SendGrid dispatcher
// implements it — Mail Send's send_at/batch_id have no NATS/email-service
// analogue — so the orchestrator type-asserts for it and rejects a schedule
// request with a clear error when the active provider does not support it,
// rather than silently sending immediately.
type ScheduledSender interface {
	// CreateBatchID mints a new provider batch identifier (SendGrid POST
	// /v3/mail/batch) to be shared by every recipient in one scheduled
	// fan-out.
	CreateBatchID(ctx context.Context) (batchID string, err error)
	// CancelScheduledBatch cancels a previously armed batch so the provider
	// discards it at release time instead of sending. Idempotent: cancelling
	// an already-cancelled batch is not an error.
	CancelScheduledBatch(ctx context.Context, batchID string) error
}

// EngagementReader reads per-newsletter engagement for analytics, keyed by the
// send group_id. Both the email-service (NATS) dispatcher and the SendGrid
// store-backed reader implement it, so the analytics service can resolve a
// newsletter's engagement from whichever provider actually dispatched it
// (newsletters.send_provider), regardless of the currently-active provider.
type EngagementReader interface {
	GetEngagement(ctx context.Context, groupID string) (*EmailEngagement, error)
	GetStatusByEmailID(ctx context.Context, emailID string) (*EmailRecipientRecord, error)
	// GroupEngagementDetail returns the bounded per-group analytics detail in a
	// SINGLE fetch: the unique-open count, the per-UTC-day opens series (total
	// opens and distinct recipients per day, ascending), the last open instant,
	// and the deduplicated failed-recipient addresses. Each provider computes it
	// its own bounded way — a set of SQL aggregates for the SendGrid store, one
	// email-service reply bucketed in memory for the NATS reader — so the
	// aggregate analytics endpoint makes one call without loading raw per-open
	// data. lastEvent is nil for a group with no opens.
	GroupEngagementDetail(ctx context.Context, groupID string) (*GroupEngagementDetail, error)
	// RecipientRecords returns every per-recipient engagement record for a
	// group, including each recipient's full open-timestamp series. Bounded by
	// the group's recipient count (a newsletter's committee audience). Backs
	// the per-recipient analytics endpoint, which — unlike the aggregate
	// endpoint — deliberately exposes who engaged and when.
	RecipientRecords(ctx context.Context, groupID string) ([]EmailRecipientRecord, error)
}

// GroupEngagementDetail is the bounded per-group analytics detail a reader
// computes in a single fetch. The aggregate analytics endpoint uses it instead
// of per-recipient records so only aggregated, bounded values cross the
// boundary there; the per-recipient analytics endpoint uses RecipientRecords
// when callers explicitly ask for who engaged and when.
type GroupEngagementDetail struct {
	UniqueOpens  int
	DailyOpens   []model.DailyOpens
	UniqueClicks int
	// DailyClicks and TopLinks are populated by the SendGrid store (SQL
	// aggregates). GroupDetailFromRecords — the email-service reader's
	// builder — leaves them nil/zero: SES clicks are a documented follow-up,
	// not this reader's job (see docs/newsletter-service-contract.md).
	DailyClicks      []model.DailyClicks
	TopLinks         []model.LinkClicks
	LastEventAt      *time.Time
	FailedRecipients []string
}

// ImageStore persists and serves newsletter image bytes, keyed by content hash.
//
// Implementations must surface a missing key as domain.ErrNotFound.
type ImageStore interface {
	Put(ctx context.Context, key string, data []byte, contentType string) error
	Get(ctx context.Context, key string) (data []byte, contentType string, err error)
	PublicURL(key string) string
}

// GroupDetailFromRecords builds a GroupEngagementDetail from bounded in-memory
// per-recipient records. It backs the email-service reader (whose by-group reply
// is bounded), keeping unique-opens, the daily series, lastEvent, and failed
// recipients identical to what the SendGrid store computes in SQL.
func GroupDetailFromRecords(records []EmailRecipientRecord) *GroupEngagementDetail {
	daily, lastEvent := DailyOpensFromRecords(records)
	unique := 0
	failedSeen := make(map[string]struct{}, len(records))
	failed := make([]string, 0)
	for _, r := range records {
		if r.Opened {
			unique++
		}
		if !r.Failed {
			continue
		}
		email := strings.ToLower(strings.TrimSpace(r.To))
		if email == "" {
			continue
		}
		if _, dup := failedSeen[email]; dup {
			continue
		}
		failedSeen[email] = struct{}{}
		failed = append(failed, email)
	}
	return &GroupEngagementDetail{
		UniqueOpens:      unique,
		DailyOpens:       daily,
		LastEventAt:      lastEvent,
		FailedRecipients: failed,
	}
}

// DailyOpensFromRecords buckets per-recipient open events into a sorted
// per-UTC-day series (total opens and distinct recipients per day) and returns
// the last open instant. It backs GroupDetailFromRecords for readers that hold
// bounded per-recipient records in memory (the email-service reader), keeping the
// bucketing identical to what the SendGrid store computes in SQL. A record with
// no opened_at_list but Opened=true and a LastOpened falls back to that single
// instant, matching older email-service shapes. lastEvent is nil for no opens.
func DailyOpensFromRecords(records []EmailRecipientRecord) ([]model.DailyOpens, *time.Time) {
	const dayLayout = "2006-01-02"
	type bucket struct {
		date  time.Time
		opens int
		uniq  map[string]struct{}
	}
	buckets := map[string]*bucket{}
	var lastEvent *time.Time
	for _, r := range records {
		events := r.OpenedAtList
		if len(events) == 0 && r.Opened && r.LastOpened != nil {
			events = []time.Time{*r.LastOpened}
		}
		key := r.EmailID
		if key == "" {
			key = r.To
		}
		for _, ev := range events {
			opened := ev.UTC()
			if lastEvent == nil || opened.After(*lastEvent) {
				cp := opened
				lastEvent = &cp
			}
			dk := opened.Format(dayLayout)
			b, ok := buckets[dk]
			if !ok {
				day, _ := time.Parse(dayLayout, dk)
				b = &bucket{date: day, uniq: map[string]struct{}{}}
				buckets[dk] = b
			}
			b.opens++
			b.uniq[key] = struct{}{}
		}
	}
	daily := make([]model.DailyOpens, 0, len(buckets))
	for _, b := range buckets {
		daily = append(daily, model.DailyOpens{Date: b.date, Opens: b.opens, UniqueOpens: len(b.uniq)})
	}
	sort.Slice(daily, func(i, j int) bool { return daily[i].Date.Before(daily[j].Date) })
	return daily, lastEvent
}
