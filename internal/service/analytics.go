// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// AnalyticsService aggregates engagement metrics for a sent newsletter,
// combining email-service totals (delivered / failed) with locally-tracked
// open events from the newsletter_opens table.
type AnalyticsService struct {
	repo  port.NewsletterRepository
	email port.EmailDispatcher
}

// NewAnalyticsService wires an AnalyticsService.
func NewAnalyticsService(repo port.NewsletterRepository, email port.EmailDispatcher) *AnalyticsService {
	return &AnalyticsService{repo: repo, email: email}
}

// Get returns aggregated analytics for the given newsletter, gated on project
// ownership. Returns ErrNotFound if the newsletter belongs to a different
// project than the one supplied, so callers can't probe across projects.
//
// Email-service totals are best-effort: if the engagement call fails we still
// return the locally-tracked analytics with the email-service-derived fields
// zeroed. Newsletter is the source of truth for total_recipients (snapshot
// taken at send time).
func (a *AnalyticsService) Get(ctx context.Context, projectUID string, newsletterID uuid.UUID) (*model.Analytics, error) {
	if err := validateProjectUID(projectUID); err != nil {
		return nil, err
	}
	n, err := a.repo.Get(ctx, newsletterID)
	if err != nil {
		return nil, err
	}
	if n.ProjectUID != projectUID {
		return nil, domain.ErrNotFound
	}

	local, err := a.repo.Analytics(ctx, newsletterID)
	if err != nil {
		return nil, err
	}

	// For drafts, skip the email-service call — there's nothing to aggregate.
	if n.Status != model.StatusSent || n.GroupID == nil || *n.GroupID == "" {
		return local, nil
	}

	engagement, engErr := a.email.GetEngagement(ctx, *n.GroupID)
	if engErr != nil {
		slog.WarnContext(ctx, "analytics: email-service engagement fetch failed, returning local-only",
			"newsletter_id", n.ID,
			"group_id", *n.GroupID,
			"error", engErr.Error(),
		)
		return local, nil
	}

	// Email-service-derived fields overlay the local analytics. UniqueOpens
	// and DailyOpens stay sourced from the local newsletter_opens table —
	// email-service doesn't aggregate uniques.
	if engagement.TotalSent > 0 {
		local.TotalRecipients = engagement.TotalSent
		local.Delivered = engagement.Delivered
	}
	local.Failed = engagement.Failed
	if engagement.Opened > local.TotalOpens {
		// If email-service is tracking more raw opens than our local pixel
		// observed, surface the bigger number. Local UniqueOpens still
		// drives the rate calculation below.
		local.TotalOpens = engagement.Opened
	}
	denominator := local.TotalRecipients
	if denominator == 0 {
		denominator = n.TotalRecipients
	}
	if denominator > 0 {
		local.OpenRate = float64(local.UniqueOpens) / float64(denominator)
	}
	return local, nil
}
