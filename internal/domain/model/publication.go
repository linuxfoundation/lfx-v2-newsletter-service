// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// Editor types a publication's editions are composed in. Persisted values —
// the schema CHECK constraint accepts exactly these two.
const (
	// EditorTypeClassic is the existing rich-text composer. Default, so every
	// publication that predates the block editor keeps working unchanged.
	EditorTypeClassic = "classic"
	// EditorTypeBlocks is the block composer, driven by the publication's
	// template set.
	EditorTypeBlocks = "blocks"
)

// ValidEditorType reports whether v is a persistable editor type.
func ValidEditorType(v string) bool {
	return v == EditorTypeClassic || v == EditorTypeBlocks
}

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
	// EditorType is which composer this publication's editions open in,
	// EditorTypeClassic or EditorTypeBlocks. Editions inherit it, which is what
	// lets per-edition template selection be dropped from the composer.
	EditorType string `bun:"editor_type,notnull,default:'classic'" json:"editorType"`
	// SenderEmail is the optional per-publication From address its editions
	// inherit. Domain approval and the send-time ownership check belong to the
	// send-from-self work (LFXV2-3316); this field only stores the choice.
	SenderEmail    *string `bun:"sender_email" json:"senderEmail,omitempty"`
	ViewOnlineBase *string `bun:"view_online_base" json:"viewOnlineBase,omitempty"`
	CreatedBy      string          `bun:"created_by,notnull" json:"createdBy"`
	Version        int64           `bun:"version,notnull,default:1" json:"version"`
	CreatedAt      time.Time       `bun:"created_at,notnull,default:current_timestamp" json:"createdAt"`
	UpdatedAt      time.Time       `bun:"updated_at,notnull,default:current_timestamp" json:"updatedAt"`
}
