// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// NewsletterUnsubscribe records a project-scoped opt-out. One row means the
// given email address must be excluded from all future sends for that
// project_uid only.
type NewsletterUnsubscribe struct {
	bun.BaseModel `bun:"table:newsletter_unsubscribes,alias:u"`

	ID         uuid.UUID `bun:"id,pk,type:uuid,default:gen_random_uuid()" json:"id"`
	ProjectUID string    `bun:"project_uid,notnull" json:"projectUid"`
	Email      string    `bun:"email,notnull" json:"email"`
	CreatedAt  time.Time `bun:"created_at,notnull,default:current_timestamp" json:"createdAt"`
	UpdatedAt  time.Time `bun:"updated_at,notnull,default:current_timestamp" json:"updatedAt"`
}
