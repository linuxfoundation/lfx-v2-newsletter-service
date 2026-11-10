// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package api

import "time"

// Publication is the response shape for a publication: the recurring parent
// of newsletter editions.
type Publication struct {
	ID         string `json:"id"`
	ProjectUID string `json:"project_uid"`
	Name       string `json:"name"`
	// TemplateKey is the template set new editions use. Omitted when each
	// edition picks its own.
	TemplateKey *string `json:"template_key,omitempty"`
	// FromName overrides the From display name at send. The From address is
	// per-project deployment configuration and cannot be set here.
	FromName *string `json:"from_name,omitempty"`
	// ReplyToEmail overrides Reply-To at send (when its domain is allowed) and is
	// the default ed_reply_email for new editions.
	ReplyToEmail *string `json:"reply_to_email,omitempty"`
	// CommitteeUIDs is the default audience new editions inherit.
	CommitteeUIDs []string `json:"committee_uids"`
	// Implicit marks a publication the service created for a newsletter that has
	// no named publication. Implicit publications are not listed.
	Implicit  bool      `json:"implicit"`
	CreatedBy string    `json:"created_by"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PublicationListResponse is the body of GET .../newsletter-publications.
type PublicationListResponse struct {
	Publications  []Publication `json:"publications"`
	NextPageToken string        `json:"next_page_token,omitempty"`
}

// PublicationRequest is the body of POST .../newsletter-publications and of the
// full-replace PUT .../newsletter-publications/{publication_uid}: an omitted
// optional field is cleared.
type PublicationRequest struct {
	Name          string   `json:"name"`
	TemplateKey   *string  `json:"template_key,omitempty"`
	FromName      *string  `json:"from_name,omitempty"`
	ReplyToEmail  *string  `json:"reply_to_email,omitempty"`
	CommitteeUIDs []string `json:"committee_uids,omitempty"`
}
