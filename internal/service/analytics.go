// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"log/slog"
	"sort"
	"strings"
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
// combining the sending provider's totals (delivered / failed) with
// locally-tracked open events from the newsletter_opens table.
//
// A newsletter's engagement lives in whichever provider dispatched it: the SES
// path reads it back from email-service over NATS, the SendGrid path reads it
// from the local engagement store. readers maps each provider name to its
// EngagementReader, and Get resolves the reader from newsletters.send_provider,
// so a newsletter's stats always come from the store that holds them regardless
// of the currently-active provider. defaultProvider is used for rows with an
// unset or unrecognized send_provider (historical rows are all email-service).
type AnalyticsService struct {
	repo            port.NewsletterRepository
	readers         map[string]port.EngagementReader
	defaultProvider string
}

// NewAnalyticsService wires an AnalyticsService. readers must contain an entry
// for defaultProvider; a nil or missing reader for a newsletter's provider
// degrades that newsletter to local-only analytics rather than panicking.
func NewAnalyticsService(repo port.NewsletterRepository, readers map[string]port.EngagementReader, defaultProvider string) *AnalyticsService {
	return &AnalyticsService{repo: repo, readers: readers, defaultProvider: defaultProvider}
}

// readerFor resolves the engagement reader for a newsletter's send_provider,
// falling back to the default provider's reader when the value is empty or
// unrecognized. Returns nil when no usable reader is registered, which the
// caller treats as an engagement-fetch failure (local-only analytics).
func (a *AnalyticsService) readerFor(provider string) port.EngagementReader {
	if r, ok := a.readers[provider]; ok && r != nil {
		return r
	}
	return a.readers[a.defaultProvider]
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
	// Honor the FailedRecipients "always present" invariant on every return
	// path (draft, engagement/status fetch error, empty records); overwritten
	// below when per-recipient records report failures.
	local.FailedRecipients = []string{}

	// For drafts, skip the engagement call — there's nothing to aggregate.
	if n.Status != model.StatusSent || n.GroupID == nil || *n.GroupID == "" {
		return local, nil
	}

	// Route the engagement read to the provider that actually sent this
	// newsletter, so historical email-service sends and SendGrid sends both
	// resolve to the store that holds their data.
	reader := a.readerFor(n.SendProvider)
	if reader == nil {
		slog.WarnContext(ctx, "analytics: no engagement reader for provider, returning local-only",
			"newsletter_id", n.ID,
			"send_provider", n.SendProvider,
		)
		return local, nil
	}

	engagement, engErr := reader.GetEngagement(ctx, *n.GroupID)
	if engErr != nil {
		slog.WarnContext(ctx, "analytics: engagement fetch failed, returning local-only",
			"newsletter_id", n.ID,
			"group_id", *n.GroupID,
			"send_provider", n.SendProvider,
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
	records, recErr := reader.GetStatusByGroupID(ctx, *n.GroupID)
	if recErr != nil {
		slog.WarnContext(ctx, "analytics: group status fetch failed, keeping engagement-only rollup",
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
		local.FailedRecipients = failedRecipients(records)
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

// failedRecipients extracts the deduplicated email addresses of recipients
// email-service marked failed. Addresses are lowercased and deduplicated to
// match the recipient-resolution convention (the send path lowercases before
// dispatch), so the same recipient never appears twice in different cases.
// Order follows first appearance in records. Returns a non-nil empty slice when
// nothing failed.
func failedRecipients(records []port.EmailRecipientRecord) []string {
	seen := make(map[string]struct{}, len(records))
	out := make([]string, 0)
	for _, r := range records {
		if !r.Failed || r.To == "" {
			continue
		}
		email := strings.ToLower(strings.TrimSpace(r.To))
		if email == "" {
			continue
		}
		if _, dup := seen[email]; dup {
			continue
		}
		seen[email] = struct{}{}
		out = append(out, email)
	}
	return out
}

// aggregatePerRecipient buckets per-recipient open events into a sorted
// DailyOpens series and counts unique opens (one per recipient that ever
// opened). Per-day Opens count every event; per-day UniqueOpens counts
// distinct recipients with at least one event on that day.
//
// When email-service exposes opened_at_list, each entry becomes its own
// counted event so repeat opens by the same recipient show up in Opens. With
// older email-service builds that only emit a flat opened_at, the dispatcher
// fills OpenedAtList with a single element so the same code path applies.
//
// Returns the last observed open timestamp so callers can advance LastEventAt.
func aggregatePerRecipient(records []port.EmailRecipientRecord) (int, []model.DailyOpens, *time.Time) {
	type bucket struct {
		date             time.Time
		opens            int
		uniqueRecipients map[string]struct{}
	}
	buckets := map[string]*bucket{}
	unique := 0
	var lastEvent *time.Time
	for _, r := range records {
		events := r.OpenedAtList
		if len(events) == 0 && r.Opened && r.LastOpened != nil {
			// Defensive fallback if the dispatcher hands us a record built
			// from a wire shape that lacked both opened_at_list and
			// opened_at but still set Opened=true with a LastOpened.
			events = []time.Time{*r.LastOpened}
		}
		if len(events) == 0 {
			continue
		}
		unique++
		recipientKey := r.EmailID
		if recipientKey == "" {
			recipientKey = r.To
		}
		for _, ev := range events {
			opened := ev.UTC()
			if lastEvent == nil || opened.After(*lastEvent) {
				cp := opened
				lastEvent = &cp
			}
			key := opened.Format(dayBucketLayout)
			b, ok := buckets[key]
			if !ok {
				day, _ := time.Parse(dayBucketLayout, key)
				b = &bucket{date: day, uniqueRecipients: map[string]struct{}{}}
				buckets[key] = b
			}
			b.opens++
			b.uniqueRecipients[recipientKey] = struct{}{}
		}
	}
	daily := make([]model.DailyOpens, 0, len(buckets))
	for _, b := range buckets {
		daily = append(daily, model.DailyOpens{Date: b.date, Opens: b.opens, UniqueOpens: len(b.uniqueRecipients)})
	}
	sort.Slice(daily, func(i, j int) bool { return daily[i].Date.Before(daily[j].Date) })
	return unique, daily, lastEvent
}
