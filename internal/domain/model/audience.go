// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package model

import (
	"time"

	"github.com/google/uuid"
)

// AudienceStatus is the state of a newsletter's audience snapshot. It is
// separate from the newsletter's own Status.
type AudienceStatus string

// AudienceStatus values persisted in newsletter_audiences.status. Mirrored by
// the schema CHECK constraint.
const (
	// AudienceProcessing means a background job is resolving the audience.
	AudienceProcessing AudienceStatus = "processing"
	// AudienceReady means the snapshot is complete and matches CommitteeUIDs.
	AudienceReady AudienceStatus = "ready"
	// AudienceFailed means the job failed, or a processing row went stale.
	AudienceFailed AudienceStatus = "failed"
)

// Audience is the recipient snapshot of one newsletter. The members are stored
// in newsletter_audience_members and read through AudienceRepository.
type Audience struct {
	NewsletterID  uuid.UUID
	ProjectUID    string
	CommitteeUIDs []string
	Status        AudienceStatus
	// RecipientCount is the number of snapshot members. Unsubscribed addresses
	// are already excluded.
	RecipientCount int
	SkippedNoEmail int
	Error          string
	RequestedAt    time.Time
	ResolvedAt     *time.Time
	UpdatedAt      time.Time
}
