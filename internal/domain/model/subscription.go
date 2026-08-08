// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// ListAAIFUserCommunity is the sole list_id value accepted by the database's
// newsletter_subscriptions_list_id_check constraint today (LFXV2-2713). Widen
// the constraint in internal/schema/schema.sql before adding a second value
// here.
const ListAAIFUserCommunity = "aaif-user-community"

// SubscriptionSourceImport marks a row created by cmd/newsletter-import. It is
// the only source this ticket produces; a future subscribe/unsubscribe flow
// would use a different value.
const SubscriptionSourceImport = "import"

// Subscription is a per-(list, email) subscription row. It is not yet a
// recipient-resolution source — see internal/schema/schema.sql for the
// current scope note.
type Subscription struct {
	bun.BaseModel `bun:"table:newsletter_subscriptions,alias:s"`

	ID     uuid.UUID `bun:"id,pk,type:uuid,default:gen_random_uuid()" json:"id"`
	ListID string    `bun:"list_id,notnull" json:"listId"`
	Email  string    `bun:"email,notnull" json:"email"`
	// No bun `default:true`: with a DEFAULT set, bun emits the DEFAULT token for
	// a Go zero value (false), so a parsed `subscribed=false` row would be stored
	// as true — silently resubscribing someone who opted out. The DDL default in
	// schema.sql still protects any insert path that omits the column.
	Subscribed     bool       `bun:"subscribed,notnull" json:"subscribed"`
	Source         string     `bun:"source,notnull,default:'import'" json:"source"`
	SubscribedAt   time.Time  `bun:"subscribed_at,notnull,default:current_timestamp" json:"subscribedAt"`
	UnsubscribedAt *time.Time `bun:"unsubscribed_at" json:"unsubscribedAt,omitempty"`
	CreatedAt      time.Time  `bun:"created_at,notnull,default:current_timestamp" json:"createdAt"`
	UpdatedAt      time.Time  `bun:"updated_at,notnull,default:current_timestamp" json:"updatedAt"`
}
