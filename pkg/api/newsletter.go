// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package api is the public HTTP contract for the newsletter service. Field
// names are snake_case to match the LFX V2 attribute-naming convention and
// the rest of the V2 services (committee, project, meeting).
package api

import (
	"bytes"
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
	ID         string `json:"id"`
	ProjectUID string `json:"project_uid"`
	Subject    string `json:"subject"`
	BodyHTML   string `json:"body_html"`
	// BodyLayout is the editor's structured layout. Present only for layout-based
	// newsletters; body_html is derived from it. Omitted for legacy / html-only
	// newsletters.
	BodyLayout    *NewsletterLayout `json:"body_layout,omitempty"`
	EDReplyEmail  string            `json:"ed_reply_email"`
	CommitteeUIDs []string          `json:"committee_uids"`
	Status        Status            `json:"status"`
	SentAt        *time.Time        `json:"sent_at,omitempty"`
	// GroupID is the lfx-v2-email-service correlation identifier, set when
	// the newsletter is sent. Null on drafts.
	GroupID *string `json:"group_id,omitempty"`
	// ScheduledAt carries two meanings depending on Status: while the
	// newsletter is a draft, it is the author's saved intent — saving it does
	// not by itself contact SendGrid. Once the newsletter is scheduled (via
	// POST .../schedule), it is the committed release time. Null when no
	// schedule has ever been set.
	ScheduledAt     *time.Time `json:"scheduled_at,omitempty"`
	TotalRecipients int        `json:"total_recipients"`
	CreatedBy       string     `json:"created_by"`
	Version         int64      `json:"version"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// CreateNewsletterRequest is the body of POST /projects/{project_uid}/newsletters.
//
// BodyLayout is optional. When supplied, the service renders it to body_html via
// the declarative emitter and persists both — any body_html in the request is
// ignored. When omitted, body_html is taken from the request as-is (legacy path).
type CreateNewsletterRequest struct {
	Subject       string            `json:"subject"`
	BodyHTML      string            `json:"body_html"`
	BodyLayout    *NewsletterLayout `json:"body_layout,omitempty"`
	EDReplyEmail  string            `json:"ed_reply_email"`
	CommitteeUIDs []string          `json:"committee_uids"`
	// ScheduledAt is optional. When set, only a future time is required at
	// save time — arming the schedule (72h horizon, minimum lead) is
	// validated separately by POST .../schedule.
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`
}

// UpdateNewsletterRequest is the body of PUT /projects/{project_uid}/newsletters/{newsletter_uid}.
//
// BodyLayout is tri-state:
//   - ABSENT (key omitted): a layout newsletter keeps its stored layout and
//     derived body_html (the request's body_html is ignored); an html-only
//     newsletter takes body_html from the request as before.
//   - EXPLICIT NULL ("body_layout": null): the stored layout is cleared and
//     the newsletter becomes html-only, taking body_html from the request.
//   - OBJECT: the layout replaces the stored one; body_html is re-derived
//     from it and the request's body_html is ignored.
//
// Full-replace semantics apply to ScheduledAt like every other field here: an
// omitted value clears a previously-saved schedule.
type UpdateNewsletterRequest struct {
	Subject       string         `json:"subject"`
	BodyHTML      string         `json:"body_html"`
	BodyLayout    OptionalLayout `json:"body_layout,omitzero"`
	EDReplyEmail  string         `json:"ed_reply_email"`
	CommitteeUIDs []string       `json:"committee_uids"`
	ScheduledAt   *time.Time     `json:"scheduled_at,omitempty"`
}

// OptionalLayout distinguishes the three JSON states of an optional
// body_layout field: key absent (Present false), explicit null (Present true,
// Layout nil), and an object (Present true, Layout non-nil). encoding/json
// only calls UnmarshalJSON when the key exists, which is what makes the
// absent/null distinction observable.
type OptionalLayout struct {
	Present bool
	Layout  *NewsletterLayout
}

// UnmarshalJSON implements the tri-state decode described on the type.
func (o *OptionalLayout) UnmarshalJSON(b []byte) error {
	o.Present = true
	if string(bytes.TrimSpace(b)) == "null" {
		o.Layout = nil
		return nil
	}
	return json.Unmarshal(b, &o.Layout)
}

// MarshalJSON round-trips the tri-state. The field carries the omitzero tag
// and IsZero reports true for the not-Present zero value, so a Go client that
// zero-initializes UpdateNewsletterRequest OMITS body_layout (preserve) rather
// than accidentally sending an explicit null (clear). Explicit null therefore
// requires Present true with a nil Layout — an intentional act.
func (o OptionalLayout) MarshalJSON() ([]byte, error) {
	if o.Layout == nil {
		return []byte("null"), nil
	}
	return json.Marshal(o.Layout)
}

// IsZero implements the encoding/json omitzero contract: the zero value
// (Present false) is omitted from marshaled requests.
func (o OptionalLayout) IsZero() bool {
	return !o.Present
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
	Subject  string `json:"subject"`
	BodyHTML string `json:"body_html"`
	ToEmail  string `json:"to_email"`
	// IsLayout is DEPRECATED and ignored. body_layout is the sole layout trigger
	// for a test send (see below); a precompiled is_layout body_html is no longer
	// dispatched verbatim, because binding its per-recipient unsubscribe sentinel
	// to empty left a dangling <a href="">Unsubscribe</a>. The field is retained
	// only so existing clients that still send it are not rejected (the decoder
	// disallows unknown fields); its value has no effect. Send body_layout for a
	// layout test send.
	IsLayout bool `json:"is_layout,omitempty"`
	// BodyLayout, when present, is the structured layout the service recompiles
	// server-side for the test send. It is the sole layout trigger and suppresses
	// only the unsubscribe opt-out row (a non-recipient test mints no opt-out
	// token, so the row would otherwise render a dangling empty Unsubscribe
	// link); the sender/reply/delivery attribution in the compliance footer
	// still renders. Send the layout — not a pre-compiled body_html — for a
	// layout test send so its footer matches the real send.
	BodyLayout   *NewsletterLayout `json:"body_layout,omitempty"`
	EDReplyEmail string            `json:"ed_reply_email,omitempty"`
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

// LayoutBlock is a single recursive content node in a newsletter layout.
// BlockType selects the declarative template; Content holds the field bindings
// the template consumes; Blocks holds nested child blocks. It mirrors the
// emitter's internal Block so the public contract does not leak an internal
// type.
type LayoutBlock struct {
	BlockType string         `json:"block_type"`
	Content   map[string]any `json:"content,omitempty"`
	Blocks    []LayoutBlock  `json:"blocks,omitempty"`
}

// NewsletterLayout is the structured newsletter body: a wrapper key plus the
// ordered top-level blocks that render inside the wrapper's body slot.
type NewsletterLayout struct {
	WrapperKey string `json:"wrapper_key"`
	// TemplateKey selects which block library the layout was composed from and is
	// rendered with. Optional: empty does NOT mean the "default" library — it
	// falls back to the aaif-user-community block superset paired with the
	// neutral "default" wrapper (see declarative.RenderTemplateKey /
	// NeutralWrapperTemplateKey), so a layout composed without an explicit
	// library, or saved before per-newsletter selection, still resolves every
	// block the editor can offer. Explicitly setting "default" instead selects
	// that library's own smaller block set and wrapper, and renders differently
	// from an empty key.
	TemplateKey string        `json:"template_key,omitempty"`
	Blocks      []LayoutBlock `json:"blocks"`
}

// RenderPreviewRequest is the body of POST
// /projects/{project_uid}/newsletters/render-preview.
//
// BodyLayout is the structured layout to render. WrapperContent supplies
// caller-controlled edition data the wrapper template binds against
// (e.g. edition.date, edition.view_online_link, edition.reply_email — the
// only source for the preview's reply row, since render-preview is
// stateless). The unsubscribe/sender/project footer sentinels are
// service-owned and protected against mutation; see previewWrapperContent
// in newsletter.go. WrapperContent is optional and may be omitted when no
// edition customization is needed.
type RenderPreviewRequest struct {
	BodyLayout     NewsletterLayout `json:"body_layout"`
	WrapperContent map[string]any   `json:"wrapper_content,omitempty"`
}

// RenderPreviewResponse is the body of POST
// /projects/{project_uid}/newsletters/render-preview. BodyHTML is the rendered,
// email-safe HTML produced by the declarative emitter.
type RenderPreviewResponse struct {
	BodyHTML string `json:"body_html"`
}

// TemplateSummary is one entry in the template-list response: a selectable block
// library keyed by its embedded template key.
type TemplateSummary struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// TemplatesResponse is the body of GET /projects/{project_uid}/newsletters/templates.
type TemplatesResponse struct {
	Templates []TemplateSummary `json:"templates"`
}

// TemplateManifestBlock is one palette entry in a template manifest: a block
// type with its editor schema and comment-stripped renderable template body.
// Mirrors NewsletterBlockManifestEntry in @lfx-one/shared.
type TemplateManifestBlock struct {
	BlockType   string          `json:"block_type"`
	Label       string          `json:"label"`
	Category    string          `json:"category"`
	Schema      json.RawMessage `json:"schema"`
	IsContainer bool            `json:"is_container,omitempty"`
	// Template is the raw element tree with all HTML comments stripped — the
	// client-side renderer walks it to draw the canvas preview.
	Template string `json:"template"`
}

// TemplateManifest is the editor manifest for one template set: the block
// palette plus the page-chrome wrapper. Body of
// GET /projects/{project_uid}/newsletters/templates/{template_key}/manifest.
// Mirrors NewsletterTemplateManifest in @lfx-one/shared.
type TemplateManifest struct {
	WrapperKey string                  `json:"wrapper_key"`
	Blocks     []TemplateManifestBlock `json:"blocks"`
	Wrapper    string                  `json:"wrapper,omitempty"`
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
// body_layout is always omitted on list rows: a layout can approach the 1 MiB
// request cap and list pages return up to 100 rows, so carrying it would bloat
// one list response by up to ~100 MiB. Fetch the single newsletter to get it.
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
