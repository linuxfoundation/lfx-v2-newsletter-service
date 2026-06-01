// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// dayBucketLayout is the calendar-day key used to group per-recipient opens
// into DailyOpens buckets. UTC keeps the buckets stable regardless of the
// caller's timezone.
const dayBucketLayout = "2006-01-02"

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

	// Email-service-derived fields overlay the local analytics.
	if engagement.TotalSent > 0 {
		local.TotalRecipients = engagement.TotalSent
		local.Delivered = engagement.Delivered
	}
	local.Failed = engagement.Failed
	if engagement.Opened > local.TotalOpens {
		// If email-service is tracking more raw opens than our local pixel
		// observed, surface the bigger number.
		local.TotalOpens = engagement.Opened
	}

	// The engagement summary is scalar-only. Fetch the per-recipient records
	// to derive UniqueOpens and the DailyOpens time series. We replace (not
	// merge) the local-table values because the local tracking pixel is not
	// embedded in outgoing emails today, so newsletter_opens is effectively
	// always empty — email-service is the authoritative source.
	records, recErr := a.email.GetStatusByGroupID(ctx, *n.GroupID)
	if recErr != nil {
		slog.WarnContext(ctx, "analytics: email-service group status fetch failed, keeping engagement-only rollup",
			"newsletter_id", n.ID,
			"group_id", *n.GroupID,
			"error", recErr.Error(),
		)
	} else if len(records) > 0 {
		uniqueOpens, daily, lastEvent := aggregatePerRecipient(records)
		local.UniqueOpens = uniqueOpens
		local.DailyOpens = daily
		if lastEvent != nil && (local.LastEventAt == nil || lastEvent.After(*local.LastEventAt)) {
			local.LastEventAt = lastEvent
		}
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

// aggregatePerRecipient buckets per-recipient open events into a sorted
// DailyOpens series and counts unique opens (one per recipient that ever
// opened). Since email-service's deployed schema carries a single LastOpened
// timestamp per recipient (not a per-open event list), Opens and UniqueOpens
// per bucket are equal — one bucketed event per recipient.
//
// Returns the last observed open timestamp so callers can advance LastEventAt.
func aggregatePerRecipient(records []port.EmailRecipientRecord) (int, []model.DailyOpens, *time.Time) {
	type bucket struct {
		date        time.Time
		opens       int
		uniqueOpens int
	}
	buckets := map[string]*bucket{}
	unique := 0
	var lastEvent *time.Time
	for _, r := range records {
		if !r.Opened || r.LastOpened == nil {
			continue
		}
		unique++
		opened := r.LastOpened.UTC()
		if lastEvent == nil || opened.After(*lastEvent) {
			cp := opened
			lastEvent = &cp
		}
		key := opened.Format(dayBucketLayout)
		b, ok := buckets[key]
		if !ok {
			day, _ := time.Parse(dayBucketLayout, key)
			b = &bucket{date: day}
			buckets[key] = b
		}
		b.opens++
		b.uniqueOpens++
	}
	daily := make([]model.DailyOpens, 0, len(buckets))
	for _, b := range buckets {
		daily = append(daily, model.DailyOpens{Date: b.date, Opens: b.opens, UniqueOpens: b.uniqueOpens})
	}
	sort.Slice(daily, func(i, j int) bool { return daily[i].Date.Before(daily[j].Date) })
	return unique, daily, lastEvent
}
