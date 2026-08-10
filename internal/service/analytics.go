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
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

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
	committee       port.CommitteeClient
	defaultProvider string
}

// NewAnalyticsService wires an AnalyticsService. readers must contain an entry
// for defaultProvider; a nil or missing reader for a newsletter's provider
// degrades that newsletter to local-only analytics rather than panicking.
// committee resolves recipient display names for the per-recipient endpoint;
// nil is tolerated (names stay empty).
func NewAnalyticsService(repo port.NewsletterRepository, readers map[string]port.EngagementReader, committee port.CommitteeClient, defaultProvider string) *AnalyticsService {
	return &AnalyticsService{repo: repo, readers: readers, committee: committee, defaultProvider: defaultProvider}
}

// readerFor resolves the engagement reader for a newsletter's send_provider.
//
// A recognized provider must resolve to its OWN reader and never fall back to
// another store's: reading SendGrid engagement from the email-service reader (or
// vice versa) would query a store that never holds that newsletter's data. So for
// a known provider whose reader is missing or nil (a misconfiguration), this
// returns nil, which the caller treats as an engagement-fetch failure and
// degrades to local-only analytics rather than reading the wrong store.
//
// Fallback to the default provider's reader applies only when the persisted
// provider is empty or unrecognized — historical rows predate send_provider and
// are all email-service.
func (a *AnalyticsService) readerFor(provider string) port.EngagementReader {
	switch provider {
	case model.SendProviderEmailService, model.SendProviderSendGrid:
		return a.readers[provider] // nil if that provider's reader isn't wired
	default:
		return a.readers[a.defaultProvider]
	}
}

// Get returns aggregated analytics for the given newsletter, gated on project
// ownership. Returns ErrNotFound if the newsletter belongs to a different
// project than the one supplied, so callers can't probe across projects.
//
// Provider engagement totals are best-effort: if the engagement read fails we
// still return the locally-tracked analytics with the provider-derived fields
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

	// Provider engagement overlays the local analytics. total_recipients stays the
	// send-time snapshot (the authoritative audience) unless engagement reports a
	// larger count: the SendGrid store holds only a row per recorded send, and
	// SendEmail intentionally records no row for ambiguous transport/5xx outcomes,
	// so engagement.TotalSent can undercount the audience and must never lower the
	// snapshot (which would inflate open rate and misreport how many were sent).
	if engagement.TotalSent > local.TotalRecipients {
		local.TotalRecipients = engagement.TotalSent
	}
	local.Delivered = engagement.Delivered
	local.Failed = engagement.Failed
	// Seed UniqueOpens (and thus OpenRate) from the scalar summary so it survives
	// a per-recipient status-fetch failure below. Keep the larger of the local
	// pixel count and the provider's, matching the TotalOpens overlay — never
	// lower a local open count the provider's scalar happens to trail.
	if engagement.UniqueOpens > local.UniqueOpens {
		local.UniqueOpens = engagement.UniqueOpens
	}
	if engagement.Opened > local.TotalOpens {
		// If the provider is tracking more raw opens than our local pixel
		// observed, surface the bigger number.
		local.TotalOpens = engagement.Opened
	}

	// The engagement summary is scalar-only. GroupEngagementDetail is a single
	// bounded fetch (SQL aggregates for SendGrid, one email-service reply bucketed
	// in memory) giving the detailed unique-open count, the daily-opens series,
	// the last event instant, and the failed recipients — analytics never loads
	// raw per-open data. UniqueOpens keeps the larger so a non-empty local count is
	// not lowered by the provider's. The provider owns the daily-opens breakdown
	// and the failed list, but replace the local values only when the provider
	// actually returned data: an empty provider detail must not clobber a
	// non-empty local series (consistent with keeping the larger UniqueOpens).
	// The local tracking pixel is not embedded in outgoing emails today, so in
	// practice the local series is empty and the provider's replaces it.
	detail, detailErr := reader.GroupEngagementDetail(ctx, *n.GroupID)
	if detailErr != nil {
		slog.WarnContext(ctx, "analytics: group engagement detail fetch failed, keeping scalar rollup",
			"newsletter_id", n.ID,
			"group_id", *n.GroupID,
			"error", detailErr.Error(),
		)
	} else {
		if detail.UniqueOpens > local.UniqueOpens {
			local.UniqueOpens = detail.UniqueOpens
		}
		if len(detail.DailyOpens) > 0 {
			local.DailyOpens = detail.DailyOpens
		}
		if len(detail.FailedRecipients) > 0 {
			local.FailedRecipients = detail.FailedRecipients
		}
		if detail.LastEventAt != nil && (local.LastEventAt == nil || detail.LastEventAt.After(*local.LastEventAt)) {
			local.LastEventAt = detail.LastEventAt
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

// Recipients returns the per-recipient engagement records for a sent
// newsletter — who was sent to, delivery outcome, and every recorded open
// timestamp — gated on project ownership like Get. Records come from the
// provider that dispatched the newsletter (routed by send_provider); local
// pixel opens are excluded (they are keyed by recipient hash, which cannot be
// mapped back to an email address).
//
// Unlike Get there is no local fallback, so a provider read failure is
// returned as an error rather than an empty list — an empty list must mean
// "no records", never "the read failed". Drafts and sent rows without a
// group_id return an empty result marked complete.
//
// The result carries an explicit Complete marker: both providers'
// per-recipient stores are best-effort (email-service omits missing/malformed
// records and its group index briefly lags a fresh send; the SendGrid store
// records no row for ambiguous outcomes), so the record list is compared
// against the newsletter's send-time total_recipients snapshot and marked
// incomplete when it trails, letting clients distinguish partial data from a
// complete audience instead of guessing.
//
// Display names are resolved best-effort by re-querying the newsletter's
// committees: a failed lookup leaves names empty (the email remains the
// fallback identity) and never fails the request, since membership may have
// legitimately changed after the send.
func (a *AnalyticsService) Recipients(ctx context.Context, projectUID string, newsletterID uuid.UUID) (*port.RecipientEngagementResult, error) {
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

	if n.Status != model.StatusSent || n.GroupID == nil || *n.GroupID == "" {
		return &port.RecipientEngagementResult{Complete: true, Recipients: []port.RecipientEngagement{}}, nil
	}

	reader := a.readerFor(n.SendProvider)
	if reader == nil {
		return nil, pkgerrors.NewServiceUnavailable("no engagement reader for send provider " + n.SendProvider)
	}
	records, err := reader.RecipientRecords(ctx, *n.GroupID)
	if err != nil {
		return nil, err
	}

	names := a.recipientNames(ctx, n.CommitteeUIDs)
	out := make([]port.RecipientEngagement, 0, len(records))
	for _, r := range records {
		// Open timestamps come pre-sorted from the SendGrid store's ORDER BY;
		// the email-service reply's ordering is upstream's, so sort defensively
		// to keep the contract's "ascending" promise provider-independent.
		sort.Slice(r.OpenedAtList, func(i, j int) bool { return r.OpenedAtList[i].Before(r.OpenedAtList[j]) })
		// Cap per-recipient opens to the most recent MaxOpensPerRecipient to bound
		// response size provider-independently. The list is ascending (oldest first),
		// so keep the LAST MaxOpensPerRecipient entries (most recent). Copy instead
		// of reslicing to release the original backing array from memory.
		if len(r.OpenedAtList) > port.MaxOpensPerRecipient {
			capped := make([]time.Time, port.MaxOpensPerRecipient)
			copy(capped, r.OpenedAtList[len(r.OpenedAtList)-port.MaxOpensPerRecipient:])
			r.OpenedAtList = capped
		}
		out = append(out, port.RecipientEngagement{
			Name:   names[strings.ToLower(strings.TrimSpace(r.To))],
			Record: r,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Record.To) < strings.ToLower(out[j].Record.To)
	})
	return &port.RecipientEngagementResult{
		TotalRecipients: n.TotalRecipients,
		Complete:        len(out) >= n.TotalRecipients,
		Recipients:      out,
	}, nil
}

// recipientNames builds a lowercased-email → "First Last" map from the
// newsletter's committees. Best-effort: a failed committee lookup is logged
// and skipped, so callers still get names for the committees that resolved.
func (a *AnalyticsService) recipientNames(ctx context.Context, committeeUIDs []string) map[string]string {
	names := make(map[string]string)
	if a.committee == nil {
		return names
	}
	for _, uid := range committeeUIDs {
		members, err := a.committee.ListMembers(ctx, uid)
		if err != nil {
			slog.WarnContext(ctx, "analytics: committee member lookup failed, recipient names degrade to email",
				"committee_uid", uid,
				"error", err.Error(),
			)
			continue
		}
		for _, m := range members {
			email := strings.ToLower(strings.TrimSpace(m.Email))
			if email == "" {
				continue
			}
			name := strings.TrimSpace(strings.TrimSpace(m.FirstName) + " " + strings.TrimSpace(m.LastName))
			if name == "" {
				continue
			}
			names[email] = name
		}
	}
	return names
}
