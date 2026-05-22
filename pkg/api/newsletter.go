// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package api is the public HTTP contract consumed by lfx-v2-ui and other
// callers. Field names mirror packages/shared/src/interfaces/newsletter.interface.ts
// in the lfx-v2-ui monorepo.
package api

import "time"

// ContextType identifies the scope a newsletter is composed for.
type ContextType string

// ContextType values mirrored from the lfx-v2-ui shared interfaces.
const (
	ContextFoundation ContextType = "foundation"
	ContextProject    ContextType = "project"
)

// Status enumerates newsletter lifecycle states.
type Status string

// Status values mirrored from the lfx-v2-ui shared interfaces.
const (
	StatusDraft Status = "draft"
	StatusSent  Status = "sent"
)

// Newsletter is the response shape returned by draft endpoints.
type Newsletter struct {
	ID            string      `json:"id"`
	ContextType   ContextType `json:"contextType"`
	ContextUID    string      `json:"contextUid"`
	Subject       string      `json:"subject"`
	BodyHTML      string      `json:"bodyHtml"`
	EDReplyEmail  string      `json:"edReplyEmail"`
	CommitteeUIDs []string    `json:"committeeUids"`
	Status        Status      `json:"status"`
	SentAt        *time.Time  `json:"sentAt,omitempty"`
	CreatedBy     string      `json:"createdBy"`
	Version       int64       `json:"version"`
	CreatedAt     time.Time   `json:"createdAt"`
	UpdatedAt     time.Time   `json:"updatedAt"`
}

// CreateDraftRequest is the body of POST /newsletters/drafts.
type CreateDraftRequest struct {
	ContextType   ContextType `json:"contextType"`
	ContextUID    string      `json:"contextUid"`
	Subject       string      `json:"subject"`
	BodyHTML      string      `json:"bodyHtml"`
	EDReplyEmail  string      `json:"edReplyEmail"`
	CommitteeUIDs []string    `json:"committeeUids"`
}

// UpdateDraftRequest is the body of PUT /newsletters/drafts/{id}.
type UpdateDraftRequest struct {
	Subject       string   `json:"subject"`
	BodyHTML      string   `json:"bodyHtml"`
	EDReplyEmail  string   `json:"edReplyEmail"`
	CommitteeUIDs []string `json:"committeeUids"`
}

// ListDraftsResponse is the body of GET /newsletters/drafts.
type ListDraftsResponse struct {
	Drafts []Newsletter `json:"drafts"`
}

// RecipientCountRequest is the body of POST /newsletters/recipient-count.
type RecipientCountRequest struct {
	CommitteeUIDs []string `json:"committeeUids"`
}

// RecipientCountResponse is the body of POST /newsletters/recipient-count.
type RecipientCountResponse struct {
	Count int `json:"count"`
}

// Recipient is a single entry in the preview recipients list.
type Recipient struct {
	Email     string `json:"email"`
	FirstName string `json:"firstName,omitempty"`
}

// RecipientsRequest is the body of POST /newsletters/recipients.
type RecipientsRequest struct {
	CommitteeUIDs []string `json:"committeeUids"`
}

// RecipientsResponse is the body of POST /newsletters/recipients.
type RecipientsResponse struct {
	Recipients []Recipient `json:"recipients"`
}

// TestSendRequest is the body of POST /newsletters/test-send.
type TestSendRequest struct {
	Subject      string      `json:"subject"`
	BodyHTML     string      `json:"bodyHtml"`
	ToEmail      string      `json:"toEmail"`
	ContextType  ContextType `json:"contextType"`
	ContextUID   string      `json:"contextUid"`
	EDReplyEmail string      `json:"edReplyEmail"`
}

// TestSendResponse is the body of POST /newsletters/test-send.
type TestSendResponse struct {
	OK bool `json:"ok"`
}

// SendFailure records a single recipient that the email service rejected.
type SendFailure struct {
	Email  string `json:"email"`
	Reason string `json:"reason"`
}

// SendResponse is the body of POST /newsletters/drafts/{id}/send.
type SendResponse struct {
	TotalRecipients int           `json:"totalRecipients"`
	Sent            int           `json:"sent"`
	Failed          int           `json:"failed"`
	Failures        []SendFailure `json:"failures"`
}

// NewsletterListItem is one row in the unified list response. Inherits the
// Newsletter shape and adds engagement fields populated only when status='sent'.
type NewsletterListItem struct {
	Newsletter
	TotalRecipients *int     `json:"totalRecipients,omitempty"`
	UniqueOpens     *int     `json:"uniqueOpens,omitempty"`
	OpenRate        *float64 `json:"openRate,omitempty"`
}

// NewsletterListResponse is the body of GET /newsletters.
type NewsletterListResponse struct {
	Newsletters   []NewsletterListItem `json:"newsletters"`
	NextPageToken string               `json:"nextPageToken,omitempty"`
}

// NewsletterDailyOpens is one bucket of the daily-opens time series.
type NewsletterDailyOpens struct {
	Date        string `json:"date"`
	Opens       int    `json:"opens"`
	UniqueOpens int    `json:"uniqueOpens"`
}

// NewsletterAnalytics is the body of GET /newsletters/{id}/analytics.
type NewsletterAnalytics struct {
	NewsletterID    string                 `json:"newsletterId"`
	Subject         string                 `json:"subject"`
	Status          Status                 `json:"status"`
	SentAt          *time.Time             `json:"sentAt,omitempty"`
	TotalRecipients int                    `json:"totalRecipients"`
	Delivered       int                    `json:"delivered"`
	Failed          int                    `json:"failed"`
	TotalOpens      int                    `json:"totalOpens"`
	UniqueOpens     int                    `json:"uniqueOpens"`
	OpenRate        float64                `json:"openRate"`
	DailyOpens      []NewsletterDailyOpens `json:"dailyOpens"`
	LastEventAt     *time.Time             `json:"lastEventAt,omitempty"`
}
