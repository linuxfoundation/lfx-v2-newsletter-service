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

	// AuthUserMetadataReadSubject resolves a user's profile from auth-service.
	// Request payload: a JWT token, a subject identifier (e.g. "auth0|..."), or
	// a username — auth-service auto-detects. We send the validated principal
	// extracted from the inbound JWT. Response: JSON { success, data: { name,
	// given_name, family_name, ... } }. See lfx-v2-auth-service/docs/user_metadata.md.
	AuthUserMetadataReadSubject = "lfx.auth-service.user_metadata.read"

	// AuthUserEmailsReadSubject resolves a user's primary (and alternate) email
	// addresses from auth-service. Request payload: JSON
	// { "user": { "auth_token": "<principal>" } }, where auth_token accepts the
	// validated principal extracted from the inbound JWT (a JWT/Authelia token,
	// subject identifier, or LFID username — auth-service auto-detects).
	// Response: JSON { success, data: { primary_email, alternate_emails } }.
	// Mirrors lfx-v2-committee-service's EmailsByAuthToken pattern. See
	// lfx-v2-auth-service/docs/subjects/user_emails.md.
	AuthUserEmailsReadSubject = "lfx.auth-service.user_emails.read"
)
