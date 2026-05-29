// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package nats

// Subjects consumed by newsletter-service for upstream service-to-service
// calls. Names mirror the conventions in lfx-v2-committee-service's
// pkg/constants/subjects.go and lfx-v2-email-service's pkg/api package.
const (
	// CommitteeListMembersSubject lists a single committee's members.
	// Request payload: committee UID as raw bytes.
	// Response: JSON array of committee member records (email, first_name, ...).
	CommitteeListMembersSubject = "lfx.committee-api.list_members"

	// ProjectGetNameSubject resolves a project's display name.
	// Request payload: project UID as raw bytes.
	// Response: project name as raw bytes.
	ProjectGetNameSubject = "lfx.projects-api.get_name"

	// ProjectGetSlugSubject resolves a project's slug.
	// Request payload: project UID as raw bytes.
	// Response: project slug as raw bytes.
	ProjectGetSlugSubject = "lfx.projects-api.get_slug"

	// Email-service NATS subjects. Imported as constants so the call sites
	// don't drift from the email-service contract.
	EmailServiceSendEmailSubject      = "lfx.email-service.send_email"
	EmailServiceGetEmailStatusSubject = "lfx.email-service.get_email_status"
	EmailServiceGetEngagementSubject  = "lfx.email-service.get_email_engagement_analytics"
)
