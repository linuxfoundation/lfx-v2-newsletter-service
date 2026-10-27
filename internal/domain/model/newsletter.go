// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// Status enumerates newsletter lifecycle states.
type Status string

// Status values persisted in the database. Mirrored by the schema CHECK constraint.
//
// StatusSending is the transient state between accepting a send request and
// completing the per-recipient fan-out. It is the cross-replica duplicate-send
// guard: the draft → sending transition is a single optimistically-locked
// UPDATE, so a concurrent or repeated send request observes the row is no
// longer a draft and is rejected with ErrSendInProgress.
// StatusScheduled means every recipient's message has been accepted by the
// send provider and is being held for release at ScheduledAt. It sits between
// StatusSending (the fan-out that arms the schedule) and StatusSent (which the
// recovery sweep settles it to once ScheduledAt passes — reconciliation of our
// display state only; the provider owns the actual delivery timing).
const (
	StatusDraft     Status = "draft"
	StatusSending   Status = "sending"
	StatusScheduled Status = "scheduled"
	StatusSent      Status = "sent"
)

// Send provider values persisted in newsletters.send_provider. They record
// which provider dispatched a newsletter so analytics reads engagement from the
// store that holds it. Mirrored by the schema CHECK constraint and matched to
// the newsletter-service EMAIL_PROVIDER config value.
const (
	// SendProviderEmailService is the SES path via lfx-v2-email-service (the
	// default and the value backfilled onto every pre-SendGrid row).
	SendProviderEmailService = "email-service"
	// SendProviderSendGrid is the direct-to-SendGrid path (LFXV2-2388).
	SendProviderSendGrid = "sendgrid"
)

// Newsletter is the aggregate root persisted in the newsletters table.
//
// Project_uid is the only scope dimension — foundation-scoped newsletters are
// not supported. Authorization at the gateway (Heimdall + FGA) gates by
// project_uid extracted from the path.
type Newsletter struct {
	bun.BaseModel `bun:"table:newsletters,alias:n"`

	ID         uuid.UUID `bun:"id,pk,type:uuid,default:gen_random_uuid()" json:"id"`
	ProjectUID string    `bun:"project_uid,notnull" json:"projectUid"`
	Subject    string    `bun:"subject,notnull" json:"subject"`
	BodyHTML   string    `bun:"body_html,notnull" json:"bodyHtml"`
	// BodyLayout is the editor's structured layout (wrapper key + ordered
	// blocks) stored verbatim as JSONB. When present it is the source of truth:
	// BodyHTML is derived from it by the declarative emitter on write. Nil for
	// legacy / body_html-only newsletters. Stored as raw JSON so the persistence
	// layer never needs to know the emitter's block schema.
	BodyLayout      json.RawMessage `bun:"body_layout,type:jsonb,nullzero" json:"bodyLayout,omitempty"`
	EDReplyEmail    string          `bun:"ed_reply_email,notnull" json:"edReplyEmail"`
	CommitteeUIDs   []string        `bun:"committee_uids,array" json:"committeeUids"`
	Status          Status          `bun:"status,notnull,default:'draft'" json:"status"`
	SentAt          *time.Time      `bun:"sent_at" json:"sentAt,omitempty"`
	TotalRecipients int             `bun:"total_recipients,notnull,default:0" json:"totalRecipients"`
	// GroupID is the lfx-v2-email-service correlation identifier, minted by
	// the SendOrchestrator at send time and persisted alongside the status
	// transition. Used by analytics queries to aggregate per-newsletter
	// engagement across the per-recipient sends.
	GroupID *string `bun:"group_id" json:"groupId,omitempty"`
	// SendProvider records which provider dispatched this newsletter
	// ('email-service' or 'sendgrid'), stamped by the SendOrchestrator at the
	// sending transition. Analytics routes engagement reads by this value so a
	// newsletter's stats always come from the store that holds them, regardless
	// of the currently-active provider. Defaults to 'email-service'.
	SendProvider string `bun:"send_provider,notnull,default:'email-service'" json:"sendProvider"`
	// ScheduledAt has two meanings depending on Status. While the row is
	// draft, it is the author's saved intent — set on create/update, not yet
	// armed, and does not by itself cause any send. Once Status is scheduled
	// (or sending en route to it), it is the committed release time handed to
	// the provider as send_at.
	ScheduledAt *time.Time `bun:"scheduled_at" json:"scheduledAt,omitempty"`
	// BatchID is the provider batch identifier minted when a schedule is
	// armed (SendGrid POST /v3/mail/batch). Every recipient in the fan-out
	// carries the same BatchID and ScheduledAt so the provider releases them
	// together, and it is what makes the batch cancellable.
	BatchID   *string   `bun:"batch_id" json:"-"`
	CreatedBy string    `bun:"created_by,notnull" json:"createdBy"`
	Version   int64     `bun:"version,notnull,default:1" json:"version"`
	CreatedAt time.Time `bun:"created_at,notnull,default:current_timestamp" json:"createdAt"`
	UpdatedAt time.Time `bun:"updated_at,notnull,default:current_timestamp" json:"updatedAt"`
}

// NewsletterOpen records a single open event for a sent newsletter.
type NewsletterOpen struct {
	bun.BaseModel `bun:"table:newsletter_opens,alias:o"`

	ID            uuid.UUID `bun:"id,pk,type:uuid,default:gen_random_uuid()" json:"id"`
	NewsletterID  uuid.UUID `bun:"newsletter_id,notnull,type:uuid" json:"newsletterId"`
	RecipientHash string    `bun:"recipient_hash,notnull" json:"recipientHash"`
	OpenedAt      time.Time `bun:"opened_at,notnull,default:current_timestamp" json:"openedAt"`
}

// DailyOpens is one bucket of opens for an analytics time-series.
type DailyOpens struct {
	Date        time.Time `json:"date"`
	Opens       int       `json:"opens"`
	UniqueOpens int       `json:"uniqueOpens"`
}

// DailyClicks is one bucket of clicks for an analytics time-series, mirroring
// DailyOpens.
type DailyClicks struct {
	Date         time.Time `json:"date"`
	Clicks       int       `json:"clicks"`
	UniqueClicks int       `json:"uniqueClicks"`
}

// LinkClicks is one entry in the top-clicked-links breakdown: a URL and how
// many total/unique clicks it received. Chrome/compliance links (unsubscribe,
// My Newsletters) are excluded at the source via clicktracking="off", so this
// reflects engagement with author content only.
type LinkClicks struct {
	URL          string `json:"url"`
	Clicks       int    `json:"clicks"`
	UniqueClicks int    `json:"uniqueClicks"`
}

// Analytics aggregates engagement metrics for a sent newsletter.
type Analytics struct {
	NewsletterID    uuid.UUID  `json:"newsletterId"`
	Subject         string     `json:"subject"`
	Status          Status     `json:"status"`
	SentAt          *time.Time `json:"sentAt,omitempty"`
	TotalRecipients int        `json:"totalRecipients"`
	Delivered       int        `json:"delivered"`
	Failed          int        `json:"failed"`
	// FailedRecipients is the best-effort list of recipient email addresses
	// email-service marked failed (synchronous send errors plus async bounce /
	// complaint events). It is sourced from the per-recipient status records, so
	// it may not exactly equal Failed (the scalar engagement count) while the
	// group index is still propagating, and is empty when the status fetch fails.
	FailedRecipients []string     `json:"failedRecipients"`
	TotalOpens       int          `json:"totalOpens"`
	UniqueOpens      int          `json:"uniqueOpens"`
	OpenRate         float64      `json:"openRate"`
	DailyOpens       []DailyOpens `json:"dailyOpens"`
	// TotalClicks / UniqueClicks / ClickRate / ClickToOpenRate / DailyClicks /
	// TopLinks are SendGrid-only (see docs/newsletter-service-contract.md): a
	// newsletter dispatched via send_provider='email-service' (SES) always
	// reports zero/empty here, since SES click tracking is a documented
	// follow-up, not implemented by this service.
	TotalClicks     int           `json:"totalClicks"`
	UniqueClicks    int           `json:"uniqueClicks"`
	ClickRate       float64       `json:"clickRate"`
	ClickToOpenRate float64       `json:"clickToOpenRate"`
	DailyClicks     []DailyClicks `json:"dailyClicks"`
	TopLinks        []LinkClicks  `json:"topLinks"`
	LastEventAt     *time.Time    `json:"lastEventAt,omitempty"`
}

// CommitteeMember is the slice of a committee member the newsletter needs for personalization.
type CommitteeMember struct {
	Email     string
	FirstName string
	LastName  string
}

// ProjectBranding is the slice of a project used to brand newsletter emails.
type ProjectBranding struct {
	DisplayName string
	LogoURL     string
}
