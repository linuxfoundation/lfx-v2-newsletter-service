// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// NewsletterPublication is the parent "newsletter identity": wrapper,
// branding, block-template set, subscriber list, slug, and the base URL used
// to build each edition's View Online link. An edition (a row in
// `newsletters`) belongs to exactly one publication via publication_id.
type NewsletterPublication struct {
	bun.BaseModel `bun:"table:newsletter_publications,alias:p"`

	ID             uuid.UUID       `bun:"id,pk,type:uuid,default:gen_random_uuid()" json:"id"`
	ProjectUID     string          `bun:"project_uid,notnull" json:"projectUid"`
	Slug           string          `bun:"slug,notnull" json:"slug"`
	Name           string          `bun:"name,notnull" json:"name"`
	IsDefault      bool            `bun:"is_default,notnull,default:false" json:"isDefault"`
	WrapperContent json.RawMessage `bun:"wrapper_content,type:jsonb,notnull,default:'{}'" json:"wrapperContent"`
	TemplateSetID  *string         `bun:"template_set_id" json:"templateSetId,omitempty"`
	ViewOnlineBase *string         `bun:"view_online_base" json:"viewOnlineBase,omitempty"`
	CreatedBy      string          `bun:"created_by,notnull" json:"createdBy"`
	Version        int64           `bun:"version,notnull,default:1" json:"version"`
	CreatedAt      time.Time       `bun:"created_at,notnull,default:current_timestamp" json:"createdAt"`
	UpdatedAt      time.Time       `bun:"updated_at,notnull,default:current_timestamp" json:"updatedAt"`
}
