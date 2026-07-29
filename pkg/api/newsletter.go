// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package api is the public HTTP contract for the newsletter service. Field
// names are snake_case to match the LFX V2 attribute-naming convention and
// the rest of the V2 services (committee, project, meeting).
package api

import "time"

// Status enumerates newsletter lifecycle states.
type Status string

// Status values persisted by the service.
//
// StatusSending means a send has been accepted and the per-recipient fan-out
// is running asynchronously. The newsletter settles to StatusSent on
// completion, or reverts to StatusDraft if no recipient could be delivered to.
const (
	StatusDraft   Status = "draft"
	StatusSending Status = "sending"
	StatusSent    Status = "sent"
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
	GroupID         *string   `json:"group_id,omitempty"`
	TotalRecipients int       `json:"total_recipients"`
	CreatedBy       string    `json:"created_by"`
	Version         int64     `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// CreateNewsletterRequest is the body of POST /projects/{project_uid}/newsletters.
type CreateNewsletterRequest struct {
	Subject       string   `json:"subject"`
	BodyHTML      string   `json:"body_html"`
	EDReplyEmail  string   `json:"ed_reply_email"`
	CommitteeUIDs []string `json:"committee_uids"`
}

// UpdateNewsletterRequest is the body of PUT /projects/{project_uid}/newsletters/{newsletter_uid}.
type UpdateNewsletterRequest struct {
	Subject       string   `json:"subject"`
	BodyHTML      string   `json:"body_html"`
	EDReplyEmail  string   `json:"ed_reply_email"`
	CommitteeUIDs []string `json:"committee_uids"`
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
// re-fetching the newsletter — status settles to 'sent', or reverts to
// 'draft' when zero recipients could be delivered to. The zero-recipient edge
// case settles synchronously and returns 200 with status='sent'. Clients
// should branch on Newsletter.Status rather than the HTTP status code.
type SendNewsletterResponse struct {
	Newsletter      Newsletter    `json:"newsletter"`
	GroupID         string        `json:"group_id"`
	TotalRecipients int           `json:"total_recipients"`
	Sent            int           `json:"sent"`
	Failed          int           `json:"failed"`
	Failures        []SendFailure `json:"failures,omitempty"`
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
	LastEventAt      *time.Time             `json:"last_event_at,omitempty"`
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
