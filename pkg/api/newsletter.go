// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package api is the public HTTP contract for the newsletter service. Field
// names are snake_case to match the LFX V2 attribute-naming convention and
// the rest of the V2 services (committee, project, meeting).
package api

import (
	"encoding/json"
	"time"
)

// Status enumerates newsletter lifecycle states.
type Status string

// Status values persisted by the service.
//
// StatusSending means a send has been accepted and the per-recipient fan-out
// is running asynchronously. The newsletter settles to StatusSent on
// completion (at least one recipient succeeded or any result is ambiguous),
// or reverts to StatusDraft if no recipient could be delivered to. A settled
// StatusSent does NOT guarantee that all recipients were accepted — only that
// the send was initiated. For scheduling, StatusSending transitions to
// StatusScheduled once at least one recipient message is accepted by SendGrid
// for release at the scheduled_at time, or if any scheduling outcome is
// ambiguous. A settled StatusScheduled does NOT guarantee all recipients were
// accepted — only that the schedule was armed at the provider or its outcome
// was unknown.
const (
	StatusDraft     Status = "draft"
	StatusSending   Status = "sending"
	StatusScheduled Status = "scheduled"
	StatusSent      Status = "sent"
)

// Newsletter is the response shape returned by single-resource endpoints.
type Newsletter struct {
	ID            string     `json:"id"`
	ProjectUID    string     `json:"project_uid"`
	Subject       string     `json:"subject"`
	BodyHTML      string     `json:"body_html"`
	EDReplyEmail  string     `json:"ed_reply_email"`
	CommitteeUIDs []string   `json:"committee_uids"`
	Status        Status     `json:"status"`
	SentAt        *time.Time `json:"sent_at,omitempty"`
	// GroupID is the lfx-v2-email-service correlation identifier, set when
	// the newsletter is sent. Null on drafts.
	GroupID *string `json:"group_id,omitempty"`
	// ScheduledAt carries two meanings depending on Status: while the
	// newsletter is a draft, it is the author's saved intent — saving it does
	// not by itself contact SendGrid. Once the newsletter is scheduled (via
	// POST .../schedule), it is the committed release time. Null when no
	// schedule has ever been set.
	ScheduledAt     *time.Time `json:"scheduled_at,omitempty"`
	PublicationID   *string    `json:"publication_id,omitempty"`
	TotalRecipients int        `json:"total_recipients"`
	CreatedBy       string     `json:"created_by"`
	Version         int64      `json:"version"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// CreateNewsletterRequest is the body of POST /projects/{project_uid}/newsletters.
type CreateNewsletterRequest struct {
	Subject       string   `json:"subject"`
	BodyHTML      string   `json:"body_html"`
	EDReplyEmail  string   `json:"ed_reply_email"`
	CommitteeUIDs []string `json:"committee_uids"`
	// ScheduledAt is optional. When set, only a future time is required at
	// save time — arming the schedule (72h horizon, minimum lead) is
	// validated separately by POST .../schedule.
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`
	// PublicationID optionally files the edition under a publication. Omitting
	// it leaves the edition unfiled, which is valid: publications are created
	// explicitly, a project is not given a default one, and server-initiated
	// editions (the weekly brief) have no publication to pick.
	PublicationID *string `json:"publication_id,omitempty"`
}

// UpdateNewsletterRequest is the body of PUT /projects/{project_uid}/newsletters/{newsletter_uid}.
//
// Full-replace semantics apply to ScheduledAt like every other field here: an
// omitted value clears a previously-saved schedule.
type UpdateNewsletterRequest struct {
	Subject       string     `json:"subject"`
	BodyHTML      string     `json:"body_html"`
	EDReplyEmail  string     `json:"ed_reply_email"`
	CommitteeUIDs []string   `json:"committee_uids"`
	ScheduledAt   *time.Time `json:"scheduled_at,omitempty"`
	// PublicationID is deliberately NOT full-replace, unlike the fields above.
	// It is a raw message so the handler can tell three states apart that a
	// *string collapses into one:
	//
	//	field absent      -> preserve the edition's current publication
	//	explicit null/""  -> unfile the edition
	//	"<uuid>"          -> move the edition to that publication
	//
	// With a *string, an absent field and an explicit null are both nil, so a
	// client that PUTs without the key would silently unfile the edition.
	PublicationID *json.RawMessage `json:"publication_id,omitempty"`
}

// RecipientCountRequest is the body of POST /projects/{project_uid}/newsletters/recipient-count.
type RecipientCountRequest struct {
	CommitteeUIDs []string `json:"committee_uids"`
}

// RecipientCountResponse is the body of POST /projects/{project_uid}/newsletters/recipient-count.
type RecipientCountResponse struct {
	Count int `json:"count"`
}

// Recipient is a single entry in the preview recipients list.
type Recipient struct {
	Email     string `json:"email"`
	FirstName string `json:"first_name,omitempty"`
}

// RecipientsRequest is the body of POST /projects/{project_uid}/newsletters/recipients.
type RecipientsRequest struct {
	CommitteeUIDs []string `json:"committee_uids"`
}

// RecipientsResponse is the body of POST /projects/{project_uid}/newsletters/recipients.
type RecipientsResponse struct {
	Recipients []Recipient `json:"recipients"`
}

// TestSendRequest is the body of POST /projects/{project_uid}/newsletters/test-send.
type TestSendRequest struct {
	Subject      string `json:"subject"`
	BodyHTML     string `json:"body_html"`
	ToEmail      string `json:"to_email"`
	EDReplyEmail string `json:"ed_reply_email,omitempty"`
}

// TestSendResponse is the body of POST /projects/{project_uid}/newsletters/test-send.
type TestSendResponse struct {
	OK bool `json:"ok"`
}

// SendFailure describes a single per-recipient failure surfaced from the send fan-out.
type SendFailure struct {
	Email string `json:"email"`
	Error string `json:"error"`
}

// SendNewsletterResponse is the body of POST /projects/{project_uid}/newsletters/{newsletter_uid}/send.
//
// The newsletter-service owns the email dispatch: it mints group_id, resolves
// recipients via NATS to committee-service, and fans out per-recipient sends
// via NATS to email-service.
//
// The send is asynchronous: the endpoint returns 202 Accepted as soon as the
// newsletter transitions to status='sending' (with Sent=0 and no Failures),
// and the fan-out completes in the background. Callers observe the outcome by
// re-fetching the newsletter — status settles to 'sent' when at least one
// recipient succeeded or any result is ambiguous, or reverts to 'draft' when
// zero recipients could be delivered to. A settled status='sent' does NOT
// guarantee all recipients were accepted — only that the send was initiated.
// The zero-recipient edge case settles synchronously and returns 200 with
// status='sent'. Clients should branch on Newsletter.Status rather than the
// HTTP status code; consult the Failures list and provider engagement metrics
// for per-recipient delivery outcomes.
type SendNewsletterResponse struct {
	Newsletter      Newsletter    `json:"newsletter"`
	GroupID         string        `json:"group_id"`
	TotalRecipients int           `json:"total_recipients"`
	Sent            int           `json:"sent"`
	Failed          int           `json:"failed"`
	Failures        []SendFailure `json:"failures,omitempty"`
}

// ScheduleNewsletterRequest is the body of
// POST /projects/{project_uid}/newsletters/{newsletter_uid}/schedule.
//
// ScheduledAt is optional: when omitted, the draft's previously-saved
// scheduled_at is used. When neither exists, the request is rejected with a
// 400. When present, it overrides the saved value for this arm.
type ScheduleNewsletterRequest struct {
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`
}

// ScheduleNewsletterResponse is the body of
// POST /projects/{project_uid}/newsletters/{newsletter_uid}/schedule.
//
// Mirrors SendNewsletterResponse: the endpoint returns 202 Accepted as soon
// as the newsletter transitions to status='sending' with the schedule armed
// at SendGrid, and the fan-out completes in the background. The newsletter
// settles to status='scheduled' once at least one recipient's message has been
// accepted by SendGrid for release at ScheduledAt, or if any scheduling outcome
// is ambiguous (unknown result from the provider). It reverts to 'draft' only
// when zero recipients could be scheduled and no outcome was ambiguous. A
// settled status='scheduled' does NOT guarantee all recipients were accepted
// — only that the schedule was armed. Consult the Failures list for
// per-recipient scheduling outcomes.
type ScheduleNewsletterResponse struct {
	Newsletter      Newsletter    `json:"newsletter"`
	GroupID         string        `json:"group_id"`
	ScheduledAt     time.Time     `json:"scheduled_at"`
	TotalRecipients int           `json:"total_recipients"`
	Sent            int           `json:"sent"`
	Failed          int           `json:"failed"`
	Failures        []SendFailure `json:"failures,omitempty"`
}

// CancelScheduleResponse is the body of
// POST /projects/{project_uid}/newsletters/{newsletter_uid}/cancel-schedule.
// The reverted newsletter is back to status='draft', retaining scheduled_at
// as the author's saved intent while group_id and batch_id are cleared.
type CancelScheduleResponse struct {
	Newsletter Newsletter `json:"newsletter"`
}

// NewsletterListItem is one row in the unified list response. Inherits the
// Newsletter shape and adds engagement fields populated only when status='sent'.
type NewsletterListItem struct {
	Newsletter
	UniqueOpens *int     `json:"unique_opens,omitempty"`
	OpenRate    *float64 `json:"open_rate,omitempty"`
}

// NewsletterListResponse is the body of GET /projects/{project_uid}/newsletters.
type NewsletterListResponse struct {
	Newsletters   []NewsletterListItem `json:"newsletters"`
	NextPageToken string               `json:"next_page_token,omitempty"`
}

// CommitteeNewsletter is one row in the committee-scoped list response. This
// is the member-facing shape: it deliberately omits body_html (payload size —
// members fetch the body via the single-resource endpoint), ed_reply_email,
// group_id, and created_by (manager-only concerns), and committee_uids (the
// full audience would disclose every other committee a newsletter was
// addressed to; the caller already knows their own committee matched by
// construction of the route).
type CommitteeNewsletter struct {
	ID         string     `json:"id"`
	ProjectUID string     `json:"project_uid"`
	Subject    string     `json:"subject"`
	SentAt     *time.Time `json:"sent_at,omitempty"`
}

// CommitteeNewsletterListResponse is the body of
// GET /committees/{committee_uid}/newsletters.
type CommitteeNewsletterListResponse struct {
	Newsletters   []CommitteeNewsletter `json:"newsletters"`
	NextPageToken string                `json:"next_page_token,omitempty"`
}

// NewsletterDailyOpens is one bucket of the daily-opens time series.
type NewsletterDailyOpens struct {
	Date        string `json:"date"`
	Opens       int    `json:"opens"`
	UniqueOpens int    `json:"unique_opens"`
}

// NewsletterDailyClicks is one bucket of the daily-clicks time series,
// mirroring NewsletterDailyOpens.
type NewsletterDailyClicks struct {
	Date         string `json:"date"`
	Clicks       int    `json:"clicks"`
	UniqueClicks int    `json:"unique_clicks"`
}

// NewsletterLinkClicks is one entry in the top-clicked-links breakdown.
type NewsletterLinkClicks struct {
	URL          string `json:"url"`
	Clicks       int    `json:"clicks"`
	UniqueClicks int    `json:"unique_clicks"`
}

// NewsletterAnalytics is the body of GET /projects/{project_uid}/newsletters/{newsletter_uid}/analytics.
type NewsletterAnalytics struct {
	NewsletterID    string     `json:"newsletter_id"`
	Subject         string     `json:"subject"`
	Status          Status     `json:"status"`
	SentAt          *time.Time `json:"sent_at,omitempty"`
	TotalRecipients int        `json:"total_recipients"`
	Delivered       int        `json:"delivered"`
	Failed          int        `json:"failed"`
	// FailedRecipients lists the lowercased recipient email addresses that
	// failed delivery. Always present; empty when there are no known failures
	// or per-recipient delivery status is unavailable. Best-effort: the list
	// may briefly lag the Failed count while delivery status propagates. See
	// docs/newsletter-service-contract.md for the full semantics.
	FailedRecipients []string               `json:"failed_recipients"`
	TotalOpens       int                    `json:"total_opens"`
	UniqueOpens      int                    `json:"unique_opens"`
	OpenRate         float64                `json:"open_rate"`
	DailyOpens       []NewsletterDailyOpens `json:"daily_opens"`
	// TotalClicks / UniqueClicks / ClickRate / ClickToOpenRate / DailyClicks /
	// TopLinks are SendGrid-only: a newsletter dispatched via send_provider
	// "email-service" (SES) always reports zero/empty here. See
	// docs/newsletter-service-contract.md.
	TotalClicks     int                     `json:"total_clicks"`
	UniqueClicks    int                     `json:"unique_clicks"`
	ClickRate       float64                 `json:"click_rate"`
	ClickToOpenRate float64                 `json:"click_to_open_rate"`
	DailyClicks     []NewsletterDailyClicks `json:"daily_clicks"`
	TopLinks        []NewsletterLinkClicks  `json:"top_links"`
	LastEventAt     *time.Time              `json:"last_event_at,omitempty"`
}

// NewsletterRecipientEngagement is one recipient's row in the per-recipient
// analytics response: who the newsletter went to, the delivery outcome, and
// every recorded open.
type NewsletterRecipientEngagement struct {
	// Name is the recipient's full name, resolved best-effort from the
	// newsletter's committees at read time. Empty when the member no longer
	// appears in the committees or has no name on file — clients display Name
	// when present and fall back to Email.
	Name         string     `json:"name,omitempty"`
	Email        string     `json:"email"`
	SentAt       *time.Time `json:"sent_at,omitempty"`
	Delivered    bool       `json:"delivered"`
	DeliveredAt  *time.Time `json:"delivered_at,omitempty"`
	Failed       bool       `json:"failed"`
	FailedAt     *time.Time `json:"failed_at,omitempty"`
	Opened       bool       `json:"opened"`
	OpenCount    int        `json:"open_count"`
	LastOpenedAt *time.Time `json:"last_opened_at,omitempty"`
	// OpenedAtList holds every recorded open timestamp, ascending. Always
	// present; empty when the recipient never opened.
	OpenedAtList  []time.Time `json:"opened_at_list"`
	Clicked       bool        `json:"clicked"`
	ClickCount    int         `json:"click_count"`
	LastClickedAt *time.Time  `json:"last_clicked_at,omitempty"`
	// ClickedAtList holds every recorded click timestamp, ascending. Always
	// present; empty when the recipient never clicked.
	ClickedAtList []time.Time `json:"clicked_at_list"`
}

// NewsletterRecipientEngagementResponse is the body of
// GET /projects/{project_uid}/newsletters/{newsletter_uid}/analytics/recipients.
type NewsletterRecipientEngagementResponse struct {
	NewsletterID string `json:"newsletter_id"`
	// TotalRecipients is the newsletter's send-time audience snapshot.
	TotalRecipients int `json:"total_recipients"`
	// Complete is false when the sending provider returned fewer per-recipient
	// records than TotalRecipients — the provider stores are best-effort
	// (records may be omitted, and a freshly-sent newsletter's records may
	// still be propagating), so clients must treat an incomplete list as
	// partial rather than assume absent recipients were never sent to.
	Complete bool `json:"complete"`
	// Recipients is sorted by email ascending and always present; empty for
	// drafts or when the sending provider recorded no recipients.
	Recipients []NewsletterRecipientEngagement `json:"recipients"`
}

// OptOut is a single entry in the newsletter opt-outs list.
type OptOut struct {
	ID             string    `json:"id"`
	Email          string    `json:"email"`
	UnsubscribedAt time.Time `json:"unsubscribed_at"`
}

// OptOutListResponse is the body of GET /projects/{project_uid}/newsletter-opt-outs.
type OptOutListResponse struct {
	OptOuts []OptOut `json:"opt_outs"`
}

// NewsletterPublication is the response shape returned by publication endpoints.
type NewsletterPublication struct {
	ID             string  `json:"id"`
	ProjectUID     string  `json:"project_uid"`
	Slug           string  `json:"slug"`
	Name           string  `json:"name"`
	IsDefault      bool    `json:"is_default"`
	WrapperContent any     `json:"wrapper_content"`
	TemplateSetID  *string `json:"template_set_id,omitempty"`
	// EditorType is which composer this publication's editions open in,
	// "classic" or "blocks". Editions inherit it.
	EditorType string `json:"editor_type"`
	// SenderEmail is the optional per-publication From address its editions
	// inherit.
	SenderEmail    *string   `json:"sender_email,omitempty"`
	ViewOnlineBase *string   `json:"view_online_base,omitempty"`
	CreatedBy      string    `json:"created_by"`
	Version        int64     `json:"version"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreatePublicationRequest is the body of POST /projects/{project_uid}/newsletter-publications.
type CreatePublicationRequest struct {
	Slug           string  `json:"slug"`
	Name           string  `json:"name"`
	WrapperContent any     `json:"wrapper_content,omitempty"`
	TemplateSetID  *string `json:"template_set_id,omitempty"`
	// EditorType is "classic" or "blocks". Omitted defaults to "classic", so an
	// existing caller that does not know about the field keeps its behaviour.
	EditorType     *string `json:"editor_type,omitempty"`
	SenderEmail    *string `json:"sender_email,omitempty"`
	ViewOnlineBase *string `json:"view_online_base,omitempty"`
}

// UpdatePublicationRequest is the body of PUT /projects/{project_uid}/newsletter-publications/{publication_uid}.
type UpdatePublicationRequest struct {
	Name           *string `json:"name,omitempty"`
	WrapperContent any     `json:"wrapper_content,omitempty"`
	TemplateSetID  *string `json:"template_set_id,omitempty"`
	EditorType     *string `json:"editor_type,omitempty"`
	SenderEmail    *string `json:"sender_email,omitempty"`
	ViewOnlineBase *string `json:"view_online_base,omitempty"`
}

// PublicationListResponse is the body of GET /projects/{project_uid}/newsletter-publications.
type PublicationListResponse struct {
	Publications []NewsletterPublication `json:"publications"`
	// NextPageToken continues the list via ?page_token=. Empty means this is
	// the last page. The list is bounded, so a caller that ignores this token
	// sees only the first page.
	NextPageToken string `json:"next_page_token,omitempty"`
}
