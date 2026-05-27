// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package port defines the inbound and outbound interfaces the service depends
// on. Concrete implementations live in internal/infrastructure or
// internal/repository.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

// ListFilters narrows a newsletter listing query.
//
// Status is optional: if empty, both drafts and sent newsletters are returned.
// PageToken is the opaque cursor returned in the previous page's response.
type ListFilters struct {
	ContextType model.ContextType
	ContextUID  string
	Status      model.Status
	PageToken   string
	Limit       int
}

// ListPage is one page of newsletters plus an optional NextPageToken for
// continuation.
type ListPage struct {
	Newsletters   []*model.Newsletter
	NextPageToken string
}

// NewsletterRepository persists Newsletter aggregates.
//
// Implementations must surface optimistic-locking conflicts as
// domain.ErrVersionMismatch and missing records as domain.ErrNotFound.
type NewsletterRepository interface {
	Create(ctx context.Context, n *model.Newsletter) error
	Get(ctx context.Context, id uuid.UUID) (*model.Newsletter, error)
	List(ctx context.Context, contextType model.ContextType, contextUID string) ([]*model.Newsletter, error)
	ListAll(ctx context.Context, filters ListFilters) (*ListPage, error)
	Update(ctx context.Context, n *model.Newsletter, expectedVersion int64) (*model.Newsletter, error)
	Delete(ctx context.Context, id uuid.UUID) error
	MarkSent(ctx context.Context, id uuid.UUID, sentAt time.Time, totalRecipients int, groupID string, expectedVersion int64) (*model.Newsletter, error)

	// Open tracking
	RecordOpen(ctx context.Context, newsletterID uuid.UUID, recipientHash string) error
	Analytics(ctx context.Context, newsletterID uuid.UUID) (*model.Analytics, error)
}

// CommitteeClient resolves committee members for newsletter recipient calculation.
//
// Implementations should forward the caller's bearer token so the upstream
// service can enforce per-user authorization.
type CommitteeClient interface {
	GetMembers(ctx context.Context, committeeUID string) ([]model.CommitteeMember, error)
}
