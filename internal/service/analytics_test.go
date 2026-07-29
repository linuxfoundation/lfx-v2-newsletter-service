// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
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
func (s *statusByGroupFake) GetStatusByGroupID(_ context.Context, _ string) ([]port.EmailRecipientRecord, error) {
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
func (a *analyticsRepoFake) MarkSending(_ context.Context, _ uuid.UUID, _ string, _ int, _ int64) (*model.Newsletter, error) {
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

	svc := NewAnalyticsService(repo, email)
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

	svc := NewAnalyticsService(repo, email)
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

	svc := NewAnalyticsService(repo, email)
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

	svc := NewAnalyticsService(repo, email)
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
		engagement: &port.EmailEngagement{TotalSent: 5, Delivered: 5, Opened: 4},
		recordsErr: errors.New("nats: timeout"),
	}

	svc := NewAnalyticsService(repo, email)
	got, err := svc.Get(context.Background(), projectUID, newsletterID)
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if got.TotalOpens != 4 {
		t.Errorf("TotalOpens: got %d, want 4 (from engagement)", got.TotalOpens)
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
