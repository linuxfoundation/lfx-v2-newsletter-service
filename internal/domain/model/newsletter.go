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
const (
	StatusDraft Status = "draft"
	StatusSent  Status = "sent"
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
	GroupID   *string   `bun:"group_id" json:"groupId,omitempty"`
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

// Analytics aggregates engagement metrics for a sent newsletter.
type Analytics struct {
	NewsletterID    uuid.UUID    `json:"newsletterId"`
	Subject         string       `json:"subject"`
	Status          Status       `json:"status"`
	SentAt          *time.Time   `json:"sentAt,omitempty"`
	TotalRecipients int          `json:"totalRecipients"`
	Delivered       int          `json:"delivered"`
	Failed          int          `json:"failed"`
	TotalOpens      int          `json:"totalOpens"`
	UniqueOpens     int          `json:"uniqueOpens"`
	OpenRate        float64      `json:"openRate"`
	DailyOpens      []DailyOpens `json:"dailyOpens"`
	LastEventAt     *time.Time   `json:"lastEventAt,omitempty"`
}

// CommitteeMember is the slice of a committee member the newsletter needs for personalization.
type CommitteeMember struct {
	Email     string
	FirstName string
}

// ProjectBranding is the slice of a project used to brand newsletter emails.
type ProjectBranding struct {
	DisplayName string
	LogoURL     string
}
