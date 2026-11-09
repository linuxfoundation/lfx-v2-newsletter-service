// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// Publication is the recurring parent of newsletters: each newsletter row is
// one edition of its publication. It owns the choices that outlive one send.
//
// Implicit publications are created by the schema for newsletters that have no
// publication of their own (rows that predate publications, or a client that
// sends no publication_uid). They carry no template set, sender or reply-to, so
// their editions send exactly as they did before publications existed.
type Publication struct {
	bun.BaseModel `bun:"table:publications,alias:p"`

	ID         uuid.UUID `bun:"id,pk,type:uuid,default:gen_random_uuid()"`
	ProjectUID string    `bun:"project_uid,notnull"`
	Name       string    `bun:"name,notnull"`
	// TemplateKey is the embedded template set every edition renders with. Nil
	// leaves the choice to each edition, as on a newsletter with no publication.
	TemplateKey *string `bun:"template_key"`
	// FromName overrides the From display name at send time. The From address
	// itself stays the per-project, deployment-configured one.
	FromName *string `bun:"from_name"`
	// ReplyToEmail overrides Reply-To at send time, subject to the deployment's
	// allowed reply-to domains, and is copied to a new edition's ed_reply_email
	// when the edition omits one.
	ReplyToEmail *string `bun:"reply_to_email"`
	// CommitteeUIDs is the default audience copied to a new edition that omits
	// committee_uids. Never nil.
	CommitteeUIDs []string  `bun:"committee_uids,array"`
	Implicit      bool      `bun:"implicit,notnull"`
	CreatedBy     string    `bun:"created_by,notnull"`
	Version       int64     `bun:"version,notnull,default:1"`
	CreatedAt     time.Time `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt     time.Time `bun:"updated_at,notnull,default:current_timestamp"`
}
