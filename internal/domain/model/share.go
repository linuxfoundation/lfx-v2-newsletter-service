// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// ShareChannel enumerates the in-app share targets a share can be recorded
// against. Mirrored by the newsletter_shares.channel CHECK constraint.
type ShareChannel string

// ShareChannel values. They match the LFX Self Serve share dialog's targets.
const (
	ShareChannelX        ShareChannel = "x"
	ShareChannelLinkedIn ShareChannel = "linkedin"
	ShareChannelReddit   ShareChannel = "reddit"
	ShareChannelEmail    ShareChannel = "email"
	ShareChannelCopyLink ShareChannel = "copy_link"
)

// ShareChannels lists every valid ShareChannel in display order.
var ShareChannels = []ShareChannel{
	ShareChannelX,
	ShareChannelLinkedIn,
	ShareChannelReddit,
	ShareChannelEmail,
	ShareChannelCopyLink,
}

// Valid reports whether c is a known share channel.
func (c ShareChannel) Valid() bool {
	for _, known := range ShareChannels {
		if c == known {
			return true
		}
	}
	return false
}

// NewsletterShare is one recorded share intent, unique per
// (newsletter, user, channel).
type NewsletterShare struct {
	bun.BaseModel `bun:"table:newsletter_shares,alias:s"`

	NewsletterID uuid.UUID    `bun:"newsletter_id,pk,type:uuid"`
	UserHash     string       `bun:"user_hash,pk"`
	Channel      ShareChannel `bun:"channel,pk"`
	SharedAt     time.Time    `bun:"shared_at,nullzero,notnull,default:current_timestamp"`
}

// ShareStats is a newsletter's share count, in total and per channel. Each
// share is one distinct (user, channel) pair.
type ShareStats struct {
	Total     int
	ByChannel map[ShareChannel]int
}
