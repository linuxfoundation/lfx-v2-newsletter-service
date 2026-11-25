// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package model

import (
	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// NewsletterPageView is one unique web page view of a sent newsletter, unique
// per (newsletter, user). Like NewsletterShare it deliberately carries no
// timestamp: the count only needs the dedupe key.
type NewsletterPageView struct {
	bun.BaseModel `bun:"table:newsletter_page_views,alias:pv"`

	NewsletterID uuid.UUID `bun:"newsletter_id,pk,type:uuid"`
	UserHash     string    `bun:"user_hash,pk"`
}
