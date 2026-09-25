// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package sendgrid

import (
	"context"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// engagementReader adapts an EngagementStore to port.EngagementReader so the
// analytics service can read SendGrid engagement without constructing a full
// send-capable Dispatcher. Reads come straight from the store, so no API key or
// authenticated-domain config is required — a deployment can serve analytics for
// SendGrid-sent newsletters even when it is not the currently-active sender.
type engagementReader struct {
	store EngagementStore
}

// NewEngagementReader wraps an EngagementStore as a port.EngagementReader.
func NewEngagementReader(store EngagementStore) port.EngagementReader {
	return &engagementReader{store: store}
}

// GetEngagement returns the per-group engagement rollup from the store.
func (r *engagementReader) GetEngagement(ctx context.Context, groupID string) (*port.EmailEngagement, error) {
	return r.store.Engagement(ctx, groupID)
}

// GetStatusByEmailID returns one recipient's engagement record from the store.
func (r *engagementReader) GetStatusByEmailID(ctx context.Context, emailID string) (*port.EmailRecipientRecord, error) {
	return r.store.RecipientByEmailID(ctx, emailID)
}

// GroupEngagementDetail returns the group's bounded analytics detail from the
// store's SQL aggregation.
func (r *engagementReader) GroupEngagementDetail(ctx context.Context, groupID string) (*port.GroupEngagementDetail, error) {
	return r.store.GroupEngagementDetail(ctx, groupID)
}

// RecipientRecords returns the group's per-recipient engagement records with
// their full open-timestamp series from the store.
func (r *engagementReader) RecipientRecords(ctx context.Context, groupID string) ([]port.EmailRecipientRecord, error) {
	return r.store.RecipientRecordsByGroupID(ctx, groupID)
}
