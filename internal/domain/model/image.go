// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package model

import (
	"time"

	"github.com/uptrace/bun"
)

// NewsletterImage is the aggregate persisted in the newsletter_images table.
// Images are content-addressed by sha256 hash and scoped to a project.
type NewsletterImage struct {
	bun.BaseModel `bun:"table:newsletter_images,alias:ni"`

	Hash                       string    `bun:"hash,pk"` // sha256 (hex) of the final re-encoded bytes
	ProjectUID                 string    `bun:"project_uid,notnull"`
	ContentType                string    `bun:"content_type,notnull"`
	Width                      int       `bun:"width,notnull"`
	Height                     int       `bun:"height,notnull"`
	ByteSize                   int       `bun:"byte_size,notnull"`
	UploadedBy                 string    `bun:"uploaded_by,notnull"`
	CreatedAt                  time.Time `bun:"created_at,notnull,default:current_timestamp"`
	ReferencedBySentNewsletter bool      `bun:"referenced_by_sent_newsletter,notnull,default:false"`
}
