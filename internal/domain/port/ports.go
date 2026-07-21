// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package port defines the inbound and outbound interfaces the service depends
// on. Concrete implementations live in internal/infrastructure or
// internal/repository.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

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
	Update(ctx context.Context, n *model.Newsletter, expectedVersion int64) (*model.Newsletter, error)
	Delete(ctx context.Context, id uuid.UUID) error

	// MarkSending atomically transitions draft → sending, persisting the
	// email-service group_id and the resolved audience size. The single
	// optimistically-locked UPDATE (gated on id, expectedVersion, and
	// status='draft') is the duplicate-send guard across replicas: a zero-row
	// result is classified as domain.ErrNotFound, domain.ErrAlreadySent,
	// domain.ErrSendInProgress, or domain.ErrVersionMismatch.
	MarkSending(ctx context.Context, id uuid.UUID, groupID string, totalRecipients int, expectedVersion int64) (*model.Newsletter, error)
	// MarkSent transitions sending → sent, gated on the version returned by
	// MarkSending. group_id and total_recipients were already persisted at the
	// sending transition.
	MarkSent(ctx context.Context, id uuid.UUID, sentAt time.Time, expectedVersion int64) (*model.Newsletter, error)
	// RevertSending returns a sending newsletter to draft after a total
	// fan-out failure (zero recipients delivered) so the operator can retry.
	RevertSending(ctx context.Context, id uuid.UUID) error
	// RecoverStuckSending marks sending rows older than the given age as sent.
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

// SendEmailInput is one per-recipient send envelope dispatched to email-service.
//
// From, FromDisplayName, and ReplyTo are optional: when empty, email-service
// applies its configured service defaults and (for ReplyTo) omits the SMTP
// Reply-To header entirely. Domains on From and ReplyTo must be in the
// email-service allowlist — enforcement lives there, not here.
type SendEmailInput struct {
	To              string
	Subject         string
	HTML            string
	Text            string
	From            string
	FromDisplayName string
	ReplyTo         string
	GroupID         string
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
	EmailID      string
	GroupID      string
	To           string
	SentAt       *time.Time
	Delivered    bool
	Opened       bool
	OpenCount    int
	LastOpened   *time.Time
	OpenedAtList []time.Time
	Failed       bool
}

// EmailEngagement is the per-group rollup returned by email-service.
type EmailEngagement struct {
	GroupID     string
	TotalSent   int
	Delivered   int
	Opened      int
	UniqueOpens int
	Failed      int
}

// EmailDispatcher fans out individual emails to lfx-v2-email-service and
// fetches engagement data needed for analytics aggregation.
//
// All calls are NATS request/reply against the email-service subjects:
//   - lfx.email-service.send_email
//   - lfx.email-service.get_email_status
//   - lfx.email-service.get_email_engagement_analytics
//
// No auth context is propagated — see comments on ProjectMetadataClient.
type EmailDispatcher interface {
	SendEmail(ctx context.Context, in SendEmailInput) (emailID string, err error)
	GetEngagement(ctx context.Context, groupID string) (*EmailEngagement, error)
	GetStatusByEmailID(ctx context.Context, emailID string) (*EmailRecipientRecord, error)
	// GetStatusByGroupID fetches the per-recipient records for every email
	// dispatched under the given group_id. Used by the analytics service to
	// build the daily-opens time series and the unique-opens count, since
	// email-service's engagement summary is scalar-only.
	GetStatusByGroupID(ctx context.Context, groupID string) ([]EmailRecipientRecord, error)
}
