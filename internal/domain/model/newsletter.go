// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package model

import (
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

// DeliveryStrategy values persisted in newsletters.delivery_strategy
// (LFXV2-2506). Mirrored by the schema CHECK constraint.
const (
	// DeliveryStrategyGlobal releases every recipient at the same
	// operator-chosen ScheduledAt. This is the default and the value
	// backfilled onto every pre-tz_local row.
	DeliveryStrategyGlobal = "global"
	// DeliveryStrategyTZLocal releases each recipient at TargetLocal on the
	// schedule date, resolved in that recipient's own timezone (falling back
	// to DefaultTimezone, then UTC), clamped so no recipient is released
	// before ScheduledAt. Requires TargetLocal and DefaultTimezone to be set.
	DeliveryStrategyTZLocal = "tz_local"
)

// RecipientTimezoneSource values persisted in
// newsletter_recipient_timezones.source. Mirrored by the schema CHECK
// constraint.
const (
	RecipientTimezoneSourceEnrichment    = "enrichment"
	RecipientTimezoneSourceIPGeolocation = "ip_geolocation"
	RecipientTimezoneSourceBrowser       = "browser"
)

// Newsletter is the aggregate root persisted in the newsletters table.
//
// Project_uid is the only scope dimension — foundation-scoped newsletters are
// not supported. Authorization at the gateway (Heimdall + FGA) gates by
// project_uid extracted from the path.
type Newsletter struct {
	bun.BaseModel `bun:"table:newsletters,alias:n"`

	ID              uuid.UUID  `bun:"id,pk,type:uuid,default:gen_random_uuid()" json:"id"`
	ProjectUID      string     `bun:"project_uid,notnull" json:"projectUid"`
	Subject         string     `bun:"subject,notnull" json:"subject"`
	BodyHTML        string     `bun:"body_html,notnull" json:"bodyHtml"`
	EDReplyEmail    string     `bun:"ed_reply_email,notnull" json:"edReplyEmail"`
	CommitteeUIDs   []string   `bun:"committee_uids,array" json:"committeeUids"`
	Status          Status     `bun:"status,notnull,default:'draft'" json:"status"`
	SentAt          *time.Time `bun:"sent_at" json:"sentAt,omitempty"`
	TotalRecipients int        `bun:"total_recipients,notnull,default:0" json:"totalRecipients"`
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
	BatchID *string `bun:"batch_id" json:"-"`
	// DeliveryStrategy selects how the release time is computed per
	// recipient at fan-out time (LFXV2-2506). See DeliveryStrategyGlobal /
	// DeliveryStrategyTZLocal. Defaults to 'global'.
	DeliveryStrategy string `bun:"delivery_strategy,notnull,default:'global'" json:"deliveryStrategy"`
	// TargetLocal is the HH:MM wall-clock release time on the schedule date,
	// resolved per recipient in their own timezone. Required when
	// DeliveryStrategy is tz_local, otherwise nil.
	TargetLocal *string `bun:"target_local" json:"targetLocal,omitempty"`
	// DefaultTimezone is the IANA timezone used as a fallback release
	// timezone when a recipient has no timezone on file in
	// newsletter_recipient_timezones. Required when DeliveryStrategy is
	// tz_local, otherwise nil.
	DefaultTimezone *string   `bun:"default_timezone" json:"defaultTimezone,omitempty"`
	CreatedBy       string    `bun:"created_by,notnull" json:"createdBy"`
	Version         int64     `bun:"version,notnull,default:1" json:"version"`
	CreatedAt       time.Time `bun:"created_at,notnull,default:current_timestamp" json:"createdAt"`
	UpdatedAt       time.Time `bun:"updated_at,notnull,default:current_timestamp" json:"updatedAt"`
}

// RecipientTimezone is this service's own record of a recipient's timezone,
// scoped per (project_uid, email) rather than per newsletter — a recipient's
// timezone is a property of the person, not of any one send (LFXV2-2506,
// Option B: self-contained to this service, no committee-service/auth-service
// contract change).
type RecipientTimezone struct {
	bun.BaseModel `bun:"table:newsletter_recipient_timezones,alias:rtz"`

	ProjectUID string    `bun:"project_uid,pk" json:"projectUid"`
	Email      string    `bun:"email,pk" json:"email"`
	Timezone   string    `bun:"timezone,notnull" json:"timezone"`
	Source     string    `bun:"source,notnull" json:"source"`
	CapturedAt time.Time `bun:"captured_at,notnull,default:current_timestamp" json:"capturedAt"`
}

// SendRecipient is a per-send audit trail row recording the timezone and
// resolved send_at actually used for one recipient of a tz_local send
// (LFXV2-2506). Written by the fan-out immediately after a successful
// per-recipient dispatch. It does not drive delivery — the provider owns the
// actual release timing — it exists purely for support/debugging visibility.
type SendRecipient struct {
	bun.BaseModel `bun:"table:newsletter_send_recipients,alias:sr"`

	ID           uuid.UUID `bun:"id,pk,type:uuid,default:gen_random_uuid()" json:"id"`
	NewsletterID uuid.UUID `bun:"newsletter_id,notnull,type:uuid" json:"newsletterId"`
	Email        string    `bun:"email,notnull" json:"email"`
	Timezone     string    `bun:"timezone,notnull" json:"timezone"`
	SendAt       time.Time `bun:"send_at,notnull" json:"sendAt"`
	CreatedAt    time.Time `bun:"created_at,notnull,default:current_timestamp" json:"createdAt"`
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
