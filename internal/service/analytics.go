// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

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
	audience        port.AudienceReader
	committee       port.CommitteeClient
	defaultProvider string
}

// NewAnalyticsService wires an AnalyticsService. readers must contain an entry
// for defaultProvider; a nil or missing reader for a newsletter's provider
// degrades that newsletter to local-only analytics rather than panicking.
// audience and committee resolve recipient display names for the per-recipient
// endpoint. Names come from the newsletter's audience snapshot when one is
// ready. Newsletters sent before snapshots existed fall back to a live committee
// read. A nil audience or committee is tolerated (names stay empty).
func NewAnalyticsService(repo port.NewsletterRepository, readers map[string]port.EngagementReader, audience port.AudienceReader, committee port.CommitteeClient, defaultProvider string) *AnalyticsService {
	return &AnalyticsService{repo: repo, readers: readers, audience: audience, committee: committee, defaultProvider: defaultProvider}
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
	n, err := a.repo.GetMeta(ctx, newsletterID)
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
	// Clicks have no local source today (unlike opens' tracking pixel), so the
	// provider's scalars always win here; the max keeps the overlay rule
	// uniform with opens rather than special-casing an always-zero local side.
	if engagement.UniqueClicks > local.UniqueClicks {
		local.UniqueClicks = engagement.UniqueClicks
	}
	if engagement.Clicked > local.TotalClicks {
		local.TotalClicks = engagement.Clicked
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
		if detail.UniqueClicks > local.UniqueClicks {
			local.UniqueClicks = detail.UniqueClicks
		}
		if len(detail.DailyOpens) > 0 {
			local.DailyOpens = detail.DailyOpens
		}
		if len(detail.DailyClicks) > 0 {
			local.DailyClicks = detail.DailyClicks
		}
		if len(detail.TopLinks) > 0 {
			local.TopLinks = detail.TopLinks
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
		local.ClickRate = float64(local.UniqueClicks) / float64(denominator)
	}
	if local.UniqueOpens > 0 {
		local.ClickToOpenRate = float64(local.UniqueClicks) / float64(local.UniqueOpens)
		// Cap at 1.0: UniqueClicks can exceed UniqueOpens if a recipient clicks
		// without opening (e.g., open pixel was blocked). This is legitimate behavior,
		// but the contract specifies capped 1.0 to avoid surprising API clients.
		if local.ClickToOpenRate > 1.0 {
			local.ClickToOpenRate = 1.0
		}
	}
	return local, nil
}

// totalOpensProviderTimeout bounds the provider read behind the
// single-newsletter GET. That read is the service's main read path, so a slow
// or absent provider must not hold it for the full NATS timeout; on expiry the
// local count is returned, exactly as on any other provider failure.
const totalOpensProviderTimeout = time.Second

// TotalOpens returns the raw open total for an already-loaded sent newsletter,
// using the same rule as Get's TotalOpens: the larger of the local pixel count
// and the sending provider's scalar Opened. It reads only a local COUNT and the
// provider's scalar rollup (no per-recipient detail), and gives the provider
// totalOpensProviderTimeout, so it can decorate a single-newsletter read. The
// caller has already authorized and loaded n, so there is no project check here.
//
// Returns ok=false for anything but a sent newsletter, so callers can omit the
// field instead of reporting zero. A provider read failure or timeout degrades
// to the local count, as in Get; only a local read failure is an error.
func (a *AnalyticsService) TotalOpens(ctx context.Context, n *model.Newsletter) (int, bool, error) {
	if n == nil || n.Status != model.StatusSent {
		return 0, false, nil
	}
	local, err := a.repo.OpenCount(ctx, n.ID)
	if err != nil {
		return 0, false, err
	}
	return a.withProviderOpens(ctx, n, local, time.Now().Add(totalOpensProviderTimeout)), true, nil
}

// totalOpensListConcurrency caps the concurrent provider reads behind
// TotalOpensForList. It matches the repository's default page size, so a
// normal list page runs its provider reads in a single wave.
const totalOpensListConcurrency = 20

// TotalOpensForList is TotalOpens for a whole list page. The local counts come
// from one grouped query (OpenCounts) rather than one COUNT per row, and every
// row's provider read runs concurrently under one shared providerDeadline
// (never later than totalOpensProviderTimeout from now), so a slow or absent
// provider adds at most that much to the page whatever its size. A row whose
// provider read misses the deadline or fails keeps its local count, as in
// TotalOpens. Unsent newsletters are absent from the map; a local read failure
// is returned as an error so the caller can omit the field for the page.
func (a *AnalyticsService) TotalOpensForList(ctx context.Context, newsletters []*model.Newsletter, providerDeadline time.Time) (map[uuid.UUID]int, error) {
	sent := make([]*model.Newsletter, 0, len(newsletters))
	ids := make([]uuid.UUID, 0, len(newsletters))
	for _, n := range newsletters {
		if n != nil && n.Status == model.StatusSent {
			sent = append(sent, n)
			ids = append(ids, n.ID)
		}
	}
	out := make(map[uuid.UUID]int, len(sent))
	if len(sent) == 0 {
		return out, nil
	}
	local, err := a.repo.OpenCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	totals := make([]int, len(sent))
	var g errgroup.Group
	g.SetLimit(totalOpensListConcurrency)
	for i, n := range sent {
		g.Go(func() error {
			totals[i] = a.withProviderOpens(ctx, n, local[n.ID], providerDeadline)
			return nil
		})
	}
	_ = g.Wait() // withProviderOpens never fails; it logs and keeps the local count.
	for i, n := range sent {
		out[n.ID] = totals[i]
	}
	return out, nil
}

// withProviderOpens overlays the sending provider's scalar open total on a
// sent newsletter's local count: the larger of the two, read under
// providerDeadline capped at totalOpensProviderTimeout from now. Any provider
// gap (no group, no reader, a failed or timed-out read) keeps the local count.
func (a *AnalyticsService) withProviderOpens(ctx context.Context, n *model.Newsletter, local int, providerDeadline time.Time) int {
	if n.GroupID == nil || *n.GroupID == "" {
		return local
	}
	reader := a.readerFor(n.SendProvider)
	if reader == nil {
		slog.WarnContext(ctx, "total opens: no engagement reader for provider, returning local-only",
			"newsletter_id", n.ID,
			"send_provider", n.SendProvider,
		)
		return local
	}
	if latest := time.Now().Add(totalOpensProviderTimeout); providerDeadline.After(latest) {
		providerDeadline = latest
	}
	providerCtx, cancel := context.WithDeadline(ctx, providerDeadline)
	defer cancel()
	engagement, engErr := reader.GetEngagement(providerCtx, *n.GroupID)
	if engErr != nil {
		slog.WarnContext(ctx, "total opens: engagement fetch failed, returning local-only",
			"newsletter_id", n.ID,
			"group_id", *n.GroupID,
			"send_provider", n.SendProvider,
			"error", engErr.Error(),
		)
		return local
	}
	if engagement != nil && engagement.Opened > local {
		return engagement.Opened
	}
	return local
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
	n, err := a.repo.GetMeta(ctx, newsletterID)
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

	names := a.recipientNames(ctx, n)
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
		// Same defensive sort + cap for clicks, mirroring the opens handling above.
		sort.Slice(r.ClickedAtList, func(i, j int) bool { return r.ClickedAtList[i].Before(r.ClickedAtList[j]) })
		if len(r.ClickedAtList) > port.MaxClicksPerRecipient {
			capped := make([]time.Time, port.MaxClicksPerRecipient)
			copy(capped, r.ClickedAtList[len(r.ClickedAtList)-port.MaxClicksPerRecipient:])
			r.ClickedAtList = capped
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

// recipientNames builds a lowercased-email → "First Last" map for the
// newsletter. It reads the audience snapshot when one is ready. Without a ready
// snapshot (newsletters sent before snapshots existed) it reads the newsletter's
// committees live. Both paths are best-effort: a failed lookup is logged and
// skipped, so callers still get the names that resolved.
func (a *AnalyticsService) recipientNames(ctx context.Context, n *model.Newsletter) map[string]string {
	names := make(map[string]string)
	if a.audience != nil {
		audience, err := a.audience.Get(ctx, n.ID)
		switch {
		case err == nil && audience.Status == model.AudienceReady:
			// A sent newsletter's members are deleted after the send, so an
			// empty list falls through to the live committee read.
			members, err := a.audience.ListMembers(ctx, n.ID)
			if err == nil && len(members) > 0 {
				addMemberNames(names, members)
				return names
			}
			if err == nil {
				break
			}
			slog.WarnContext(ctx, "analytics: audience member lookup failed, falling back to committees",
				"newsletter_id", n.ID,
				"error", err.Error(),
			)
		case err != nil && !errors.Is(err, domain.ErrNotFound):
			slog.WarnContext(ctx, "analytics: audience lookup failed, falling back to committees",
				"newsletter_id", n.ID,
				"error", err.Error(),
			)
		}
	}
	if a.committee == nil {
		return names
	}
	for _, uid := range n.CommitteeUIDs {
		list, err := a.committee.ListMembers(ctx, uid)
		if err != nil {
			slog.WarnContext(ctx, "analytics: committee member lookup failed, recipient names degrade to email",
				"committee_uid", uid,
				"error", err.Error(),
			)
			continue
		}
		addMemberNames(names, list.Members)
	}
	return names
}

// addMemberNames adds each member that has an email and a name to names, keyed
// by lowercased email.
func addMemberNames(names map[string]string, members []model.CommitteeMember) {
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
