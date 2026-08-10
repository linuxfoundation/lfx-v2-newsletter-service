// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// statusByGroupFake stands in for the EmailDispatcher when the analytics
// service is the unit under test. Only the methods the analytics path calls
// are non-trivial; the others satisfy the interface.
type statusByGroupFake struct {
	engagement    *port.EmailEngagement
	engagementErr error
	records       []port.EmailRecipientRecord
	recordsErr    error
}

func (s *statusByGroupFake) SendEmail(_ context.Context, _ port.SendEmailInput) (string, error) {
	return "", nil
}
func (s *statusByGroupFake) GetEngagement(_ context.Context, _ string) (*port.EmailEngagement, error) {
	return s.engagement, s.engagementErr
}
func (s *statusByGroupFake) GetStatusByEmailID(_ context.Context, _ string) (*port.EmailRecipientRecord, error) {
	return nil, nil
}
func (s *statusByGroupFake) GroupEngagementDetail(_ context.Context, _ string) (*port.GroupEngagementDetail, error) {
	// Mirror a real reader: the detail is derived from the same underlying records
	// (and shares its error), via the shared helper.
	if s.recordsErr != nil {
		return nil, s.recordsErr
	}
	return port.GroupDetailFromRecords(s.records), nil
}
func (s *statusByGroupFake) RecipientRecords(_ context.Context, _ string) ([]port.EmailRecipientRecord, error) {
	return s.records, s.recordsErr
}

// analyticsRepoFake returns a fixed newsletter + base analytics row regardless
// of id, which is enough to exercise the email-service overlay path.
type analyticsRepoFake struct {
	newsletter *model.Newsletter
	base       *model.Analytics
}

func (a *analyticsRepoFake) Create(_ context.Context, _ *model.Newsletter) error { return nil }
func (a *analyticsRepoFake) Get(_ context.Context, _ uuid.UUID) (*model.Newsletter, error) {
	return a.newsletter, nil
}
func (a *analyticsRepoFake) List(_ context.Context, _ string) ([]*model.Newsletter, error) {
	return nil, nil
}
func (a *analyticsRepoFake) ListAll(_ context.Context, _ port.ListFilters) (*port.ListPage, error) {
	return nil, nil
}
func (a *analyticsRepoFake) ListSentByCommittee(_ context.Context, _ string, _ string) (*port.ListPage, error) {
	return nil, nil
}
func (a *analyticsRepoFake) Update(_ context.Context, n *model.Newsletter, _ int64) (*model.Newsletter, error) {
	return n, nil
}
func (a *analyticsRepoFake) Delete(_ context.Context, _ uuid.UUID) error { return nil }
func (a *analyticsRepoFake) MarkSending(_ context.Context, _ uuid.UUID, _, _ string, _ int, _ int64) (*model.Newsletter, error) {
	return a.newsletter, nil
}
func (a *analyticsRepoFake) MarkSent(_ context.Context, _ uuid.UUID, _ time.Time, _ int64) (*model.Newsletter, error) {
	return a.newsletter, nil
}
func (a *analyticsRepoFake) RevertSending(_ context.Context, _ uuid.UUID) error { return nil }
func (a *analyticsRepoFake) RecoverStuckSending(_ context.Context, _ time.Duration) (int64, error) {
	return 0, nil
}
func (a *analyticsRepoFake) RecordOpen(_ context.Context, _ uuid.UUID, _ string) error { return nil }
func (a *analyticsRepoFake) Analytics(_ context.Context, _ uuid.UUID) (*model.Analytics, error) {
	cp := *a.base
	return &cp, nil
}

// TestAnalyticsGet_DailyOpensFromGroupStatus verifies the analytics service
// derives DailyOpens and UniqueOpens from the per-recipient email-service
// records — not from the (always-empty) local newsletter_opens table.
func TestAnalyticsGet_DailyOpensFromGroupStatus(t *testing.T) {
	projectUID := "63f32fa9-b1be-4b1a-9a1f-98fb2dd34870"
	newsletterID := uuid.New()
	groupID := "group-xyz"
	sentAt := time.Date(2026, 6, 1, 22, 26, 26, 0, time.UTC)

	day1Open := time.Date(2026, 6, 1, 22, 26, 32, 0, time.UTC)
	day1OpenLate := time.Date(2026, 6, 1, 22, 52, 30, 0, time.UTC)
	day2Open := time.Date(2026, 6, 2, 9, 5, 0, 0, time.UTC)

	repo := &analyticsRepoFake{
		newsletter: &model.Newsletter{
			ID:              newsletterID,
			ProjectUID:      projectUID,
			Status:          model.StatusSent,
			GroupID:         &groupID,
			SentAt:          &sentAt,
			TotalRecipients: 5,
		},
		base: &model.Analytics{
			NewsletterID:    newsletterID,
			Status:          model.StatusSent,
			SentAt:          &sentAt,
			TotalRecipients: 5,
			Delivered:       5,
		},
	}
	email := &statusByGroupFake{
		engagement: &port.EmailEngagement{
			GroupID:   groupID,
			TotalSent: 5,
			Delivered: 5,
			Opened:    3,
			Failed:    0,
		},
		records: []port.EmailRecipientRecord{
			{EmailID: "e1", To: "a@x", Delivered: true, Opened: true, LastOpened: &day1Open},
			{EmailID: "e2", To: "b@x", Delivered: true, Opened: true, LastOpened: &day1OpenLate},
			{EmailID: "e3", To: "c@x", Delivered: true, Opened: true, LastOpened: &day2Open},
			{EmailID: "e4", To: "d@x", Delivered: true, Opened: false},
			{EmailID: "e5", To: "e@x", Delivered: true, Opened: false},
		},
	}

	svc := NewAnalyticsService(repo, map[string]port.EngagementReader{model.SendProviderEmailService: email}, nil, model.SendProviderEmailService)
	got, err := svc.Get(context.Background(), projectUID, newsletterID)
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if got.UniqueOpens != 3 {
		t.Errorf("UniqueOpens: got %d, want 3", got.UniqueOpens)
	}
	if got.TotalOpens != 3 {
		t.Errorf("TotalOpens: got %d, want 3 (overlaid from engagement.Opened)", got.TotalOpens)
	}
	if len(got.DailyOpens) != 2 {
		t.Fatalf("DailyOpens length: got %d, want 2 (one bucket per UTC day)", len(got.DailyOpens))
	}
	if !got.DailyOpens[0].Date.Equal(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("DailyOpens[0].Date: got %v, want 2026-06-01 UTC", got.DailyOpens[0].Date)
	}
	if got.DailyOpens[0].Opens != 2 || got.DailyOpens[0].UniqueOpens != 2 {
		t.Errorf("DailyOpens[0]: got opens=%d unique=%d, want 2/2", got.DailyOpens[0].Opens, got.DailyOpens[0].UniqueOpens)
	}
	if !got.DailyOpens[1].Date.Equal(time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("DailyOpens[1].Date: got %v, want 2026-06-02 UTC", got.DailyOpens[1].Date)
	}
	if got.DailyOpens[1].Opens != 1 || got.DailyOpens[1].UniqueOpens != 1 {
		t.Errorf("DailyOpens[1]: got opens=%d unique=%d, want 1/1", got.DailyOpens[1].Opens, got.DailyOpens[1].UniqueOpens)
	}
	wantRate := 3.0 / 5.0
	if got.OpenRate != wantRate {
		t.Errorf("OpenRate: got %v, want %v", got.OpenRate, wantRate)
	}
	if got.LastEventAt == nil || !got.LastEventAt.Equal(day2Open) {
		t.Errorf("LastEventAt: got %v, want %v", got.LastEventAt, day2Open)
	}
}

// TestAnalyticsGet_FailedRecipients verifies the analytics service surfaces the
// deduplicated list of recipient addresses email-service marked failed, drawn
// from the per-recipient status records.
func TestAnalyticsGet_FailedRecipients(t *testing.T) {
	projectUID := "63f32fa9-b1be-4b1a-9a1f-98fb2dd34870"
	newsletterID := uuid.New()
	groupID := "group-xyz"
	sentAt := time.Date(2026, 6, 1, 22, 26, 26, 0, time.UTC)
	open := time.Date(2026, 6, 1, 22, 30, 0, 0, time.UTC)

	repo := &analyticsRepoFake{
		newsletter: &model.Newsletter{
			ID:              newsletterID,
			ProjectUID:      projectUID,
			Status:          model.StatusSent,
			GroupID:         &groupID,
			SentAt:          &sentAt,
			TotalRecipients: 4,
		},
		base: &model.Analytics{
			NewsletterID:    newsletterID,
			Status:          model.StatusSent,
			SentAt:          &sentAt,
			TotalRecipients: 4,
		},
	}
	email := &statusByGroupFake{
		engagement: &port.EmailEngagement{GroupID: groupID, TotalSent: 4, Delivered: 1, Opened: 1, Failed: 2},
		records: []port.EmailRecipientRecord{
			{EmailID: "e1", To: "good@x", Delivered: true, Opened: true, LastOpened: &open},
			{EmailID: "e2", To: "bounce@x", Failed: true},
			{EmailID: "e3", To: "Bounce@X", Failed: true}, // duplicate address, different case
			{EmailID: "e4", To: " complaint@x ", Failed: true},
			{EmailID: "e5", To: "", Failed: true}, // missing address is skipped
		},
	}

	svc := NewAnalyticsService(repo, map[string]port.EngagementReader{model.SendProviderEmailService: email}, nil, model.SendProviderEmailService)
	got, err := svc.Get(context.Background(), projectUID, newsletterID)
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	// Addresses are lowercased and trimmed; the mixed-case duplicate collapses.
	want := []string{"bounce@x", "complaint@x"}
	if len(got.FailedRecipients) != len(want) {
		t.Fatalf("FailedRecipients: got %v, want %v", got.FailedRecipients, want)
	}
	for i, addr := range want {
		if got.FailedRecipients[i] != addr {
			t.Errorf("FailedRecipients[%d]: got %q, want %q", i, got.FailedRecipients[i], addr)
		}
	}
}

// TestAnalyticsGet_NoFailedRecipients verifies the failed list is empty (not a
// panic) when every per-recipient record delivered successfully.
func TestAnalyticsGet_NoFailedRecipients(t *testing.T) {
	projectUID := "63f32fa9-b1be-4b1a-9a1f-98fb2dd34870"
	newsletterID := uuid.New()
	groupID := "group-xyz"
	sentAt := time.Date(2026, 6, 1, 22, 26, 26, 0, time.UTC)
	open := time.Date(2026, 6, 1, 22, 30, 0, 0, time.UTC)

	repo := &analyticsRepoFake{
		newsletter: &model.Newsletter{
			ID:              newsletterID,
			ProjectUID:      projectUID,
			Status:          model.StatusSent,
			GroupID:         &groupID,
			SentAt:          &sentAt,
			TotalRecipients: 1,
		},
		base: &model.Analytics{NewsletterID: newsletterID, Status: model.StatusSent, SentAt: &sentAt, TotalRecipients: 1},
	}
	email := &statusByGroupFake{
		engagement: &port.EmailEngagement{GroupID: groupID, TotalSent: 1, Delivered: 1, Opened: 1},
		records: []port.EmailRecipientRecord{
			{EmailID: "e1", To: "good@x", Delivered: true, Opened: true, LastOpened: &open},
		},
	}

	svc := NewAnalyticsService(repo, map[string]port.EngagementReader{model.SendProviderEmailService: email}, nil, model.SendProviderEmailService)
	got, err := svc.Get(context.Background(), projectUID, newsletterID)
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if len(got.FailedRecipients) != 0 {
		t.Errorf("FailedRecipients: got %v, want empty", got.FailedRecipients)
	}
}

// TestAnalyticsGet_CountsPerEventOpens verifies aggregation when email-service
// exposes opened_at_list — multiple opens by the same recipient are each
// counted into per-day Opens, while UniqueOpens still counts the recipient
// once per day.
func TestAnalyticsGet_CountsPerEventOpens(t *testing.T) {
	projectUID := "63f32fa9-b1be-4b1a-9a1f-98fb2dd34870"
	newsletterID := uuid.New()
	groupID := "group-xyz"
	sentAt := time.Date(2026, 6, 1, 22, 26, 26, 0, time.UTC)

	// Recipient A opens twice on day 1, once on day 2.
	// Recipient B opens once on day 1.
	a1 := time.Date(2026, 6, 1, 22, 30, 0, 0, time.UTC)
	a2 := time.Date(2026, 6, 1, 23, 45, 0, 0, time.UTC)
	a3 := time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)
	b1 := time.Date(2026, 6, 1, 22, 35, 0, 0, time.UTC)

	repo := &analyticsRepoFake{
		newsletter: &model.Newsletter{
			ID:              newsletterID,
			ProjectUID:      projectUID,
			Status:          model.StatusSent,
			GroupID:         &groupID,
			SentAt:          &sentAt,
			TotalRecipients: 2,
		},
		base: &model.Analytics{
			NewsletterID:    newsletterID,
			Status:          model.StatusSent,
			SentAt:          &sentAt,
			TotalRecipients: 2,
			Delivered:       2,
		},
	}
	email := &statusByGroupFake{
		engagement: &port.EmailEngagement{GroupID: groupID, TotalSent: 2, Delivered: 2, Opened: 4},
		records: []port.EmailRecipientRecord{
			{EmailID: "rA", To: "a@x", Delivered: true, Opened: true, OpenedAtList: []time.Time{a1, a2, a3}},
			{EmailID: "rB", To: "b@x", Delivered: true, Opened: true, OpenedAtList: []time.Time{b1}},
		},
	}

	svc := NewAnalyticsService(repo, map[string]port.EngagementReader{model.SendProviderEmailService: email}, nil, model.SendProviderEmailService)
	got, err := svc.Get(context.Background(), projectUID, newsletterID)
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if got.UniqueOpens != 2 {
		t.Errorf("UniqueOpens: got %d, want 2", got.UniqueOpens)
	}
	if len(got.DailyOpens) != 2 {
		t.Fatalf("DailyOpens length: got %d, want 2", len(got.DailyOpens))
	}
	// Day 1: a1, a2, b1 → 3 opens, 2 unique recipients (rA, rB).
	if got.DailyOpens[0].Opens != 3 || got.DailyOpens[0].UniqueOpens != 2 {
		t.Errorf("DailyOpens[0]: got opens=%d unique=%d, want 3/2", got.DailyOpens[0].Opens, got.DailyOpens[0].UniqueOpens)
	}
	// Day 2: a3 → 1 open, 1 unique (rA).
	if got.DailyOpens[1].Opens != 1 || got.DailyOpens[1].UniqueOpens != 1 {
		t.Errorf("DailyOpens[1]: got opens=%d unique=%d, want 1/1", got.DailyOpens[1].Opens, got.DailyOpens[1].UniqueOpens)
	}
	if got.LastEventAt == nil || !got.LastEventAt.Equal(a3) {
		t.Errorf("LastEventAt: got %v, want %v", got.LastEventAt, a3)
	}
}

// TestAnalyticsGet_DegradesGracefullyOnGroupStatusError verifies the analytics
// service still returns the engagement-derived rollup when GetStatusByGroupID
// fails — DailyOpens/UniqueOpens stay at the local-table values (typically
// empty) instead of erroring out.
func TestAnalyticsGet_DegradesGracefullyOnGroupStatusError(t *testing.T) {
	projectUID := "63f32fa9-b1be-4b1a-9a1f-98fb2dd34870"
	newsletterID := uuid.New()
	groupID := "group-xyz"
	sentAt := time.Now().UTC()

	repo := &analyticsRepoFake{
		newsletter: &model.Newsletter{
			ID:              newsletterID,
			ProjectUID:      projectUID,
			Status:          model.StatusSent,
			GroupID:         &groupID,
			SentAt:          &sentAt,
			TotalRecipients: 5,
		},
		base: &model.Analytics{
			NewsletterID:    newsletterID,
			Status:          model.StatusSent,
			SentAt:          &sentAt,
			TotalRecipients: 5,
		},
	}
	email := &statusByGroupFake{
		engagement: &port.EmailEngagement{TotalSent: 5, Delivered: 5, Opened: 4, UniqueOpens: 3},
		recordsErr: errors.New("nats: timeout"),
	}

	svc := NewAnalyticsService(repo, map[string]port.EngagementReader{model.SendProviderEmailService: email}, nil, model.SendProviderEmailService)
	got, err := svc.Get(context.Background(), projectUID, newsletterID)
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if got.TotalOpens != 4 {
		t.Errorf("TotalOpens: got %d, want 4 (from engagement)", got.TotalOpens)
	}
	// The detail fetch failed, so the scalar unique-open count from GetEngagement
	// must be preserved (not zeroed) — this is the fallback the scalar seed exists
	// for, and it depends on the email-service reader populating UniqueOpens.
	if got.UniqueOpens != 3 {
		t.Errorf("UniqueOpens: got %d, want 3 (scalar fallback from engagement when detail fetch fails)", got.UniqueOpens)
	}
	if len(got.DailyOpens) != 0 {
		t.Errorf("DailyOpens: got %d buckets, want 0 (no records available)", len(got.DailyOpens))
	}
	// The "always present" invariant holds even when the status fetch fails:
	// FailedRecipients is a non-nil empty slice, never nil.
	if got.FailedRecipients == nil {
		t.Error("FailedRecipients: got nil, want non-nil empty slice on the degraded path")
	}
	if len(got.FailedRecipients) != 0 {
		t.Errorf("FailedRecipients: got %v, want empty on the degraded path", got.FailedRecipients)
	}
}

// TestAnalyticsGet_RoutesByProvider verifies the analytics service reads
// engagement from the reader matching each newsletter's send_provider, and
// falls back to the default provider for an empty or unrecognized value. The
// two readers report distinct Opened counts so the assertion identifies which
// one served the request.
func TestAnalyticsGet_RoutesByProvider(t *testing.T) {
	projectUID := "63f32fa9-b1be-4b1a-9a1f-98fb2dd34870"
	groupID := "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	sentAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	const emailServiceOpened = 2
	const sendGridOpened = 7

	emailReader := &statusByGroupFake{
		engagement: &port.EmailEngagement{GroupID: groupID, TotalSent: 5, Delivered: 5, Opened: emailServiceOpened},
	}
	sendGridReader := &statusByGroupFake{
		engagement: &port.EmailEngagement{GroupID: groupID, TotalSent: 5, Delivered: 5, Opened: sendGridOpened},
	}
	readers := map[string]port.EngagementReader{
		model.SendProviderEmailService: emailReader,
		model.SendProviderSendGrid:     sendGridReader,
	}

	cases := []struct {
		name         string
		sendProvider string
		wantOpened   int
	}{
		{"sendgrid routes to the sendgrid reader", model.SendProviderSendGrid, sendGridOpened},
		{"email-service routes to the email-service reader", model.SendProviderEmailService, emailServiceOpened},
		{"empty provider falls back to the default reader", "", emailServiceOpened},
		{"unknown provider falls back to the default reader", "mystery", emailServiceOpened},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newsletterID := uuid.New()
			repo := &analyticsRepoFake{
				newsletter: &model.Newsletter{
					ID:              newsletterID,
					ProjectUID:      projectUID,
					Status:          model.StatusSent,
					GroupID:         &groupID,
					SendProvider:    tc.sendProvider,
					SentAt:          &sentAt,
					TotalRecipients: 5,
				},
				base: &model.Analytics{NewsletterID: newsletterID, Status: model.StatusSent, SentAt: &sentAt, TotalRecipients: 5, Delivered: 5},
			}
			svc := NewAnalyticsService(repo, readers, nil, model.SendProviderEmailService)
			got, err := svc.Get(context.Background(), projectUID, newsletterID)
			if err != nil {
				t.Fatalf("Get: unexpected error: %v", err)
			}
			if got.TotalOpens != tc.wantOpened {
				t.Errorf("TotalOpens: got %d, want %d (wrong reader served send_provider=%q)", got.TotalOpens, tc.wantOpened, tc.sendProvider)
			}
		})
	}
}

// TestAnalyticsGet_KnownProviderMissingReaderIsLocalOnly verifies that a known
// provider whose reader is not wired (a misconfiguration) degrades to local-only
// analytics rather than falling back to another store's reader — reading SendGrid
// engagement from the email-service reader would report another store's data for
// this newsletter.
func TestAnalyticsGet_KnownProviderMissingReaderIsLocalOnly(t *testing.T) {
	projectUID := "63f32fa9-b1be-4b1a-9a1f-98fb2dd34870"
	groupID := "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	sentAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	// Only the email-service reader is wired; the SendGrid reader is missing. Its
	// data would be visibly wrong (99 opens) if the fallback served it.
	emailReader := &statusByGroupFake{
		engagement: &port.EmailEngagement{GroupID: groupID, TotalSent: 5, Delivered: 5, Opened: 99},
	}
	readers := map[string]port.EngagementReader{model.SendProviderEmailService: emailReader}

	newsletterID := uuid.New()
	repo := &analyticsRepoFake{
		newsletter: &model.Newsletter{
			ID:              newsletterID,
			ProjectUID:      projectUID,
			Status:          model.StatusSent,
			GroupID:         &groupID,
			SendProvider:    model.SendProviderSendGrid, // known provider, reader missing
			SentAt:          &sentAt,
			TotalRecipients: 5,
		},
		base: &model.Analytics{NewsletterID: newsletterID, Status: model.StatusSent, SentAt: &sentAt, TotalRecipients: 5, Delivered: 5},
	}
	svc := NewAnalyticsService(repo, readers, nil, model.SendProviderEmailService)
	got, err := svc.Get(context.Background(), projectUID, newsletterID)
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	// Must not read the email-service reader's 99 opens for a SendGrid newsletter.
	if got.TotalOpens != 0 {
		t.Errorf("TotalOpens = %d; want 0 (a known provider with a missing reader must degrade to local-only, not fall back to another store)", got.TotalOpens)
	}
}

// TestAnalyticsGet_PreservesLocalDailyOpensWhenProviderDetailEmpty verifies that
// an empty provider detail series does not clobber a non-empty local daily-opens
// series — the provider owns the breakdown only when it actually returned one,
// consistent with keeping the larger unique-open count.
func TestAnalyticsGet_PreservesLocalDailyOpensWhenProviderDetailEmpty(t *testing.T) {
	projectUID := "63f32fa9-b1be-4b1a-9a1f-98fb2dd34870"
	groupID := "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	sentAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	localDay := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	// Scalar rollup is present, but there are no per-recipient records, so the
	// provider detail (daily series / failed list) is empty.
	email := &statusByGroupFake{
		engagement: &port.EmailEngagement{GroupID: groupID, TotalSent: 5, Delivered: 5, Opened: 2, UniqueOpens: 2},
		records:    nil,
	}
	newsletterID := uuid.New()
	repo := &analyticsRepoFake{
		newsletter: &model.Newsletter{
			ID: newsletterID, ProjectUID: projectUID, Status: model.StatusSent,
			GroupID: &groupID, SendProvider: model.SendProviderEmailService,
			SentAt: &sentAt, TotalRecipients: 5,
		},
		base: &model.Analytics{
			NewsletterID: newsletterID, Status: model.StatusSent, SentAt: &sentAt,
			TotalRecipients: 5, Delivered: 5,
			DailyOpens: []model.DailyOpens{{Date: localDay, Opens: 3, UniqueOpens: 2}},
		},
	}
	svc := NewAnalyticsService(repo, map[string]port.EngagementReader{model.SendProviderEmailService: email}, nil, model.SendProviderEmailService)
	got, err := svc.Get(context.Background(), projectUID, newsletterID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// The empty provider detail must NOT clobber the local daily series.
	if len(got.DailyOpens) != 1 || got.DailyOpens[0].Opens != 3 || got.DailyOpens[0].UniqueOpens != 2 {
		t.Errorf("DailyOpens: got %+v, want the preserved local series [{Opens:3 UniqueOpens:2}]", got.DailyOpens)
	}
}

// committeeFake is a minimal port.CommitteeClient for name-enrichment tests.
type committeeFake struct {
	members map[string][]model.CommitteeMember
	err     error
}

func (c *committeeFake) ListMembers(_ context.Context, uid string) ([]model.CommitteeMember, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.members[uid], nil
}

// TestAnalyticsRecipients_DraftReturnsEmpty verifies drafts (and sent rows
// without a group_id) return an empty, non-nil list without touching any
// engagement reader.
func TestAnalyticsRecipients_DraftReturnsEmpty(t *testing.T) {
	projectUID := "63f32fa9-b1be-4b1a-9a1f-98fb2dd34870"
	newsletterID := uuid.New()
	repo := &analyticsRepoFake{
		newsletter: &model.Newsletter{ID: newsletterID, ProjectUID: projectUID, Status: model.StatusDraft},
	}
	// No readers wired at all: the draft short-circuit must not need one.
	svc := NewAnalyticsService(repo, nil, nil, model.SendProviderEmailService)
	got, err := svc.Recipients(context.Background(), projectUID, newsletterID)
	if err != nil {
		t.Fatalf("Recipients: %v", err)
	}
	if got == nil || got.Recipients == nil || len(got.Recipients) != 0 {
		t.Errorf("Recipients: got %+v, want empty non-nil recipients slice", got)
	}
	if got != nil && !got.Complete {
		t.Error("Complete: got false for a draft, want true (nothing was sent, nothing can be missing)")
	}
}

// TestAnalyticsRecipients_ProjectMismatch verifies the project-ownership gate
// returns ErrNotFound so callers can't probe newsletters across projects.
func TestAnalyticsRecipients_ProjectMismatch(t *testing.T) {
	newsletterID := uuid.New()
	groupID := "group-xyz"
	repo := &analyticsRepoFake{
		newsletter: &model.Newsletter{
			ID: newsletterID, ProjectUID: "63f32fa9-b1be-4b1a-9a1f-98fb2dd34870",
			Status: model.StatusSent, GroupID: &groupID,
		},
	}
	svc := NewAnalyticsService(repo, nil, nil, model.SendProviderEmailService)
	if _, err := svc.Recipients(context.Background(), "7f0c88a4-6a8c-4fa4-95c1-6d640a12e3aa", newsletterID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Recipients: got err %v, want domain.ErrNotFound", err)
	}
}

// TestAnalyticsRecipients_SortsAndEnrichesNames verifies records come back
// sorted by lowercased email, open timestamps are ascending, and names resolve
// case-insensitively from the newsletter's committees (falling back to empty
// for recipients no longer in the committees).
func TestAnalyticsRecipients_SortsAndEnrichesNames(t *testing.T) {
	projectUID := "63f32fa9-b1be-4b1a-9a1f-98fb2dd34870"
	newsletterID := uuid.New()
	groupID := "group-xyz"
	t1 := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)

	repo := &analyticsRepoFake{
		newsletter: &model.Newsletter{
			ID: newsletterID, ProjectUID: projectUID, Status: model.StatusSent,
			GroupID: &groupID, SendProvider: model.SendProviderEmailService,
			CommitteeUIDs: []string{"committee-1", "committee-2"},
		},
	}
	email := &statusByGroupFake{
		records: []port.EmailRecipientRecord{
			{EmailID: "e2", To: "Zed@X.dev", Delivered: true, Opened: true, OpenCount: 2, OpenedAtList: []time.Time{t2, t1}},
			{EmailID: "e1", To: "ann@x.dev", Delivered: true},
			{EmailID: "e3", To: "gone@x.dev", Failed: true},
		},
	}
	committee := &committeeFake{members: map[string][]model.CommitteeMember{
		"committee-1": {{Email: "ANN@x.dev", FirstName: "Ann", LastName: "Ames"}},
		"committee-2": {{Email: "zed@x.dev", FirstName: "Zed"}},
	}}
	svc := NewAnalyticsService(repo, map[string]port.EngagementReader{model.SendProviderEmailService: email}, committee, model.SendProviderEmailService)
	result, err := svc.Recipients(context.Background(), projectUID, newsletterID)
	if err != nil {
		t.Fatalf("Recipients: %v", err)
	}
	got := result.Recipients
	if len(got) != 3 {
		t.Fatalf("Recipients: got %d records, want 3", len(got))
	}
	if got[0].Record.To != "ann@x.dev" || got[1].Record.To != "gone@x.dev" || got[2].Record.To != "Zed@X.dev" {
		t.Errorf("order: got [%s %s %s], want sorted by lowercased email", got[0].Record.To, got[1].Record.To, got[2].Record.To)
	}
	if got[0].Name != "Ann Ames" {
		t.Errorf("Name for ann@x.dev: got %q, want %q (case-insensitive match)", got[0].Name, "Ann Ames")
	}
	if got[1].Name != "" {
		t.Errorf("Name for gone@x.dev: got %q, want empty (not in committees)", got[1].Name)
	}
	if got[2].Name != "Zed" {
		t.Errorf("Name for zed@x.dev: got %q, want %q (first name only)", got[2].Name, "Zed")
	}
	opens := got[2].Record.OpenedAtList
	if len(opens) != 2 || !opens[0].Equal(t1) || !opens[1].Equal(t2) {
		t.Errorf("OpenedAtList: got %v, want ascending [%v %v]", opens, t1, t2)
	}
}

// TestAnalyticsRecipients_CompletenessMarker verifies the result is marked
// incomplete when the provider returns fewer records than the send-time
// total_recipients snapshot, and complete when the counts line up.
func TestAnalyticsRecipients_CompletenessMarker(t *testing.T) {
	projectUID := "63f32fa9-b1be-4b1a-9a1f-98fb2dd34870"
	groupID := "group-xyz"
	records := []port.EmailRecipientRecord{
		{EmailID: "e1", To: "ann@x.dev", Delivered: true},
		{EmailID: "e2", To: "zed@x.dev", Delivered: true},
	}
	for _, tc := range []struct {
		name            string
		totalRecipients int
		wantComplete    bool
	}{
		{"records match snapshot", 2, true},
		{"records trail snapshot (propagation lag / omissions)", 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newsletterID := uuid.New()
			repo := &analyticsRepoFake{
				newsletter: &model.Newsletter{
					ID: newsletterID, ProjectUID: projectUID, Status: model.StatusSent,
					GroupID: &groupID, SendProvider: model.SendProviderEmailService,
					TotalRecipients: tc.totalRecipients,
				},
			}
			email := &statusByGroupFake{records: records}
			svc := NewAnalyticsService(repo, map[string]port.EngagementReader{model.SendProviderEmailService: email}, nil, model.SendProviderEmailService)
			result, err := svc.Recipients(context.Background(), projectUID, newsletterID)
			if err != nil {
				t.Fatalf("Recipients: %v", err)
			}
			if result.Complete != tc.wantComplete {
				t.Errorf("Complete: got %v, want %v (records=%d, snapshot=%d)", result.Complete, tc.wantComplete, len(result.Recipients), tc.totalRecipients)
			}
			if result.TotalRecipients != tc.totalRecipients {
				t.Errorf("TotalRecipients: got %d, want %d", result.TotalRecipients, tc.totalRecipients)
			}
		})
	}
}

// TestAnalyticsRecipients_ReaderErrorPropagates verifies a provider read
// failure surfaces as an error — never as an empty list, which callers must be
// able to trust as "no records".
func TestAnalyticsRecipients_ReaderErrorPropagates(t *testing.T) {
	projectUID := "63f32fa9-b1be-4b1a-9a1f-98fb2dd34870"
	newsletterID := uuid.New()
	groupID := "group-xyz"
	repo := &analyticsRepoFake{
		newsletter: &model.Newsletter{
			ID: newsletterID, ProjectUID: projectUID, Status: model.StatusSent,
			GroupID: &groupID, SendProvider: model.SendProviderEmailService,
		},
	}
	wantErr := errors.New("engagement store unavailable")
	email := &statusByGroupFake{recordsErr: wantErr}
	svc := NewAnalyticsService(repo, map[string]port.EngagementReader{model.SendProviderEmailService: email}, nil, model.SendProviderEmailService)
	if _, err := svc.Recipients(context.Background(), projectUID, newsletterID); !errors.Is(err, wantErr) {
		t.Fatalf("Recipients: got err %v, want the reader error propagated", err)
	}
}

// TestAnalyticsRecipients_NilReaderForKnownProvider verifies a recognized
// provider with no wired reader is a service-unavailable error, not a silent
// empty list and not a read from the wrong provider's store.
func TestAnalyticsRecipients_NilReaderForKnownProvider(t *testing.T) {
	projectUID := "63f32fa9-b1be-4b1a-9a1f-98fb2dd34870"
	newsletterID := uuid.New()
	groupID := "group-xyz"
	repo := &analyticsRepoFake{
		newsletter: &model.Newsletter{
			ID: newsletterID, ProjectUID: projectUID, Status: model.StatusSent,
			GroupID: &groupID, SendProvider: model.SendProviderSendGrid,
		},
	}
	svc := NewAnalyticsService(repo, map[string]port.EngagementReader{model.SendProviderEmailService: &statusByGroupFake{}}, nil, model.SendProviderEmailService)
	_, err := svc.Recipients(context.Background(), projectUID, newsletterID)
	var unavailable pkgerrors.ServiceUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("Recipients: got err %v, want pkgerrors.ServiceUnavailable", err)
	}
}

// TestAnalyticsRecipients_CommitteeLookupFailureDegrades verifies a failed
// committee lookup degrades to empty names (email remains the identity) rather
// than failing the request.
func TestAnalyticsRecipients_CommitteeLookupFailureDegrades(t *testing.T) {
	projectUID := "63f32fa9-b1be-4b1a-9a1f-98fb2dd34870"
	newsletterID := uuid.New()
	groupID := "group-xyz"
	repo := &analyticsRepoFake{
		newsletter: &model.Newsletter{
			ID: newsletterID, ProjectUID: projectUID, Status: model.StatusSent,
			GroupID: &groupID, SendProvider: model.SendProviderEmailService,
			CommitteeUIDs: []string{"committee-1"},
		},
	}
	email := &statusByGroupFake{records: []port.EmailRecipientRecord{{EmailID: "e1", To: "ann@x.dev", Delivered: true}}}
	committee := &committeeFake{err: errors.New("committee-service down")}
	svc := NewAnalyticsService(repo, map[string]port.EngagementReader{model.SendProviderEmailService: email}, committee, model.SendProviderEmailService)
	result, err := svc.Recipients(context.Background(), projectUID, newsletterID)
	if err != nil {
		t.Fatalf("Recipients: %v", err)
	}
	got := result.Recipients
	if len(got) != 1 || got[0].Name != "" || got[0].Record.To != "ann@x.dev" {
		t.Errorf("Recipients: got %+v, want one record with empty name", got)
	}
}
