// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// fakeScheduledEmailDispatcher wraps fakeEmailDispatcher and additionally
// implements port.ScheduledSender, so SendNewsletter's type assertion
// succeeds. It also captures the SendAt/BatchID carried by each per-recipient
// SendEmailInput, which the base fake does not.
type fakeScheduledEmailDispatcher struct {
	fakeEmailDispatcher

	mu           sync.Mutex
	sendAts      []*time.Time
	batchIDs     []string
	batchCount   int
	batchIDErr   error
	cancelledIDs []string
	cancelErr    error
}

func (f *fakeScheduledEmailDispatcher) SendEmail(ctx context.Context, in port.SendEmailInput) (string, error) {
	f.mu.Lock()
	f.sendAts = append(f.sendAts, in.SendAt)
	f.batchIDs = append(f.batchIDs, in.BatchID)
	f.mu.Unlock()
	return f.fakeEmailDispatcher.SendEmail(ctx, in)
}

func (f *fakeScheduledEmailDispatcher) CreateBatchID(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.batchIDErr != nil {
		return "", f.batchIDErr
	}
	f.batchCount++
	return "batch-" + uuid.NewString(), nil
}

func (f *fakeScheduledEmailDispatcher) CancelScheduledBatch(_ context.Context, batchID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelledIDs = append(f.cancelledIDs, batchID)
	return f.cancelErr
}

// newTestScheduleOrchestrator wires an orchestrator against a
// ScheduledSender-capable dispatcher with the given schedule windows. Zero
// windows fall back to the package defaults.
func newTestScheduleOrchestrator(repo *fakeNewsletterRepo, committee *fakeCommitteeClient, email *fakeScheduledEmailDispatcher, minLead, maxHorizon, cancelBuffer time.Duration) *SendOrchestrator {
	return NewSendOrchestrator(SendOrchestratorConfig{
		Repo:                 repo,
		Committee:            committee,
		Project:              &fakeProjectClient{},
		Email:                email,
		Concurrency:          2,
		FanoutEnabled:        true,
		SendProvider:         model.SendProviderSendGrid,
		SendGridScheduler:    email,
		ScheduleMinLead:      minLead,
		ScheduleMaxHorizon:   maxHorizon,
		ScheduleCancelBuffer: cancelBuffer,
	})
}

func waitForStatus(t *testing.T, repo *fakeNewsletterRepo, id uuid.UUID, want model.Status) model.Newsletter {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		n := repo.get(id)
		if n.Status == want {
			return n
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("newsletter %s did not reach status %q, still %q", id, want, repo.get(id).Status)
	return model.Newsletter{}
}

// TestScheduleFanOutCarriesIdenticalSendAtAndBatchID covers the plan's
// "identical across every recipient" requirement.
func TestScheduleFanOutCarriesIdenticalSendAtAndBatchID(t *testing.T) {
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "a@example.com"}, {Email: "b@example.com"}},
	}}
	email := &fakeScheduledEmailDispatcher{}
	orch := NewSendOrchestrator(SendOrchestratorConfig{
		Repo:          repo,
		Committee:     committee,
		Project:       &fakeProjectClient{},
		Email:         email,
		Concurrency:   2,
		FanoutEnabled: true,
	})
	draft := repo.addDraft("p1", []string{"c1"})
	scheduledAt := time.Now().UTC().Add(time.Hour)

	result, err := orch.SendNewsletter(context.Background(), SendNewsletterInput{
		ProjectUID:   "p1",
		NewsletterID: draft.ID,
		Schedule:     true,
		ScheduledAt:  &scheduledAt,
	})
	if err != nil {
		t.Fatalf("SendNewsletter: %v", err)
	}
	if result.Newsletter.Status != model.StatusSending {
		t.Fatalf("expected status=sending immediately after schedule, got %q", result.Newsletter.Status)
	}

	waitForStatus(t, repo, draft.ID, model.StatusScheduled)

	email.mu.Lock()
	defer email.mu.Unlock()
	if len(email.sendAts) != 2 {
		t.Fatalf("expected 2 recipient sends, got %d", len(email.sendAts))
	}
	if email.batchCount != 1 {
		t.Fatalf("expected exactly one batch id minted, got %d", email.batchCount)
	}
	firstBatch := email.batchIDs[0]
	for i, b := range email.batchIDs {
		if b == "" {
			t.Fatalf("recipient %d missing batch id", i)
		}
		if b != firstBatch {
			t.Fatalf("recipient %d batch id %q differs from %q", i, b, firstBatch)
		}
	}
	for i, sa := range email.sendAts {
		if sa == nil {
			t.Fatalf("recipient %d missing send_at", i)
			continue
		}
		if !sa.Equal(scheduledAt) {
			t.Fatalf("recipient %d send_at %v != %v", i, sa, scheduledAt)
		}
	}

	final := repo.get(draft.ID)
	if final.BatchID == nil || *final.BatchID != firstBatch {
		t.Fatalf("stored batch id %v != minted %q", final.BatchID, firstBatch)
	}
}

// TestScheduleRequiresScheduledSenderProvider covers the 503 guard when the
// active dispatcher does not implement port.ScheduledSender.
func TestScheduleRequiresScheduledSenderProvider(t *testing.T) {
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "a@example.com"}},
	}}
	email := &fakeEmailDispatcher{} // does NOT implement port.ScheduledSender
	orch := newTestOrchestrator(repo, committee, email, nil)
	draft := repo.addDraft("p1", []string{"c1"})
	scheduledAt := time.Now().UTC().Add(time.Hour)

	_, err := orch.SendNewsletter(context.Background(), SendNewsletterInput{
		ProjectUID:   "p1",
		NewsletterID: draft.ID,
		Schedule:     true,
		ScheduledAt:  &scheduledAt,
	})
	var svcUnavailable pkgerrors.ServiceUnavailable
	if !errors.As(err, &svcUnavailable) {
		t.Fatalf("expected ServiceUnavailable, got %v (%T)", err, err)
	}
}

// TestValidateScheduleWindow is the arm-time window table test from the plan.
func TestValidateScheduleWindow(t *testing.T) {
	repo := newFakeRepo()
	email := &fakeScheduledEmailDispatcher{}
	minLead := 5 * time.Minute
	maxHorizon := 72 * time.Hour
	orch := newTestScheduleOrchestrator(repo, &fakeCommitteeClient{}, email, minLead, maxHorizon, 10*time.Minute)

	tests := []struct {
		name    string
		delta   time.Duration
		wantErr bool
	}{
		{"past", -time.Hour, true},
		{"inside min lead", 1 * time.Minute, true},
		{"valid mid-window", 24 * time.Hour, false},
		{"just under horizon", maxHorizon - time.Minute, false},
		{"just over horizon", maxHorizon + time.Minute, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := orch.validateScheduleWindow(time.Now().UTC().Add(tc.delta))
			if tc.wantErr && !errors.Is(err, domain.ErrInvalidRequest) {
				t.Fatalf("expected ErrInvalidRequest, got %v", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

// TestSendIgnoresDraftScheduledAt confirms POST .../send (Schedule=false)
// never consults a draft's saved scheduled_at, even when one is present.
func TestSendIgnoresDraftScheduledAt(t *testing.T) {
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "a@example.com"}},
	}}
	email := &fakeScheduledEmailDispatcher{}
	orch := NewSendOrchestrator(SendOrchestratorConfig{
		Repo:          repo,
		Committee:     committee,
		Project:       &fakeProjectClient{},
		Email:         email,
		Concurrency:   2,
		FanoutEnabled: true,
	})
	draft := repo.addDraft("p1", []string{"c1"})
	scheduledAt := time.Now().UTC().Add(time.Hour)
	draft.ScheduledAt = &scheduledAt
	repo.mu.Lock()
	repo.drafts[draft.ID].ScheduledAt = &scheduledAt
	repo.mu.Unlock()

	_, err := orch.SendNewsletter(context.Background(), SendNewsletterInput{
		ProjectUID:   "p1",
		NewsletterID: draft.ID,
		Schedule:     false,
	})
	if err != nil {
		t.Fatalf("SendNewsletter: %v", err)
	}

	waitForStatus(t, repo, draft.ID, model.StatusSent)

	email.mu.Lock()
	defer email.mu.Unlock()
	if email.batchCount != 0 {
		t.Fatalf("expected no batch id minted for an immediate send, got %d", email.batchCount)
	}
	for i, sa := range email.sendAts {
		if sa != nil {
			t.Fatalf("recipient %d unexpectedly carried send_at %v on a plain send", i, sa)
		}
	}
}

// TestScheduleWithoutOverrideUsesSavedValue covers an empty-body
// POST .../schedule falling back to the draft's saved scheduled_at, and the
// 400 when neither is set.
func TestScheduleWithoutOverrideUsesSavedValue(t *testing.T) {
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "a@example.com"}},
	}}
	email := &fakeScheduledEmailDispatcher{}
	orch := newTestScheduleOrchestrator(repo, committee, email, 5*time.Minute, 72*time.Hour, 5*time.Minute)

	saved := time.Now().UTC().Add(2 * time.Hour)
	draftWithSaved := repo.addDraft("p1", []string{"c1"})
	repo.mu.Lock()
	repo.drafts[draftWithSaved.ID].ScheduledAt = &saved
	repo.mu.Unlock()

	result, err := orch.SendNewsletter(context.Background(), SendNewsletterInput{
		ProjectUID:   "p1",
		NewsletterID: draftWithSaved.ID,
		Schedule:     true,
	})
	if err != nil {
		t.Fatalf("SendNewsletter with saved scheduled_at and no override: %v", err)
	}
	waitForStatus(t, repo, draftWithSaved.ID, model.StatusScheduled)
	final := repo.get(draftWithSaved.ID)
	if final.ScheduledAt == nil || !final.ScheduledAt.Equal(saved) {
		t.Fatalf("expected saved scheduled_at %v to be used, got %v", saved, final.ScheduledAt)
	}
	_ = result

	draftWithoutSaved := repo.addDraft("p1", []string{"c1"})
	_, err = orch.SendNewsletter(context.Background(), SendNewsletterInput{
		ProjectUID:   "p1",
		NewsletterID: draftWithoutSaved.ID,
		Schedule:     true,
	})
	if !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest with neither saved nor override scheduled_at, got %v", err)
	}
}

// TestCancelScheduledHappyPath covers the happy-path cancel: revert to draft,
// clear group_id/batch_id, retain scheduled_at.
func TestCancelScheduledHappyPath(t *testing.T) {
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "a@example.com"}},
	}}
	email := &fakeScheduledEmailDispatcher{}
	orch := newTestScheduleOrchestrator(repo, committee, email, 5*time.Minute, 72*time.Hour, 5*time.Minute)
	draft := repo.addDraft("p1", []string{"c1"})
	scheduledAt := time.Now().UTC().Add(2 * time.Hour)

	if _, err := orch.SendNewsletter(context.Background(), SendNewsletterInput{
		ProjectUID:   "p1",
		NewsletterID: draft.ID,
		Schedule:     true,
		ScheduledAt:  &scheduledAt,
	}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	scheduled := waitForStatus(t, repo, draft.ID, model.StatusScheduled)

	reverted, err := orch.CancelScheduled(context.Background(), CancelScheduledInput{
		ProjectUID:      "p1",
		NewsletterID:    draft.ID,
		ExpectedVersion: scheduled.Version,
	})
	if err != nil {
		t.Fatalf("CancelScheduled: %v", err)
	}
	if reverted.Status != model.StatusDraft {
		t.Fatalf("expected status=draft after cancel, got %q", reverted.Status)
	}
	if reverted.GroupID != nil {
		t.Fatalf("expected group_id cleared after cancel, got %v", *reverted.GroupID)
	}
	if reverted.BatchID != nil {
		t.Fatalf("expected batch_id cleared after cancel, got %v", *reverted.BatchID)
	}
	if reverted.ScheduledAt == nil || !reverted.ScheduledAt.Equal(scheduledAt) {
		t.Fatalf("expected scheduled_at retained as %v, got %v", scheduledAt, reverted.ScheduledAt)
	}

	email.mu.Lock()
	defer email.mu.Unlock()
	if len(email.cancelledIDs) != 1 {
		t.Fatalf("expected exactly one provider cancel call, got %d", len(email.cancelledIDs))
	}
}

// TestCancelScheduledRejectsInsideBuffer covers the cancel-window guard.
func TestCancelScheduledRejectsInsideBuffer(t *testing.T) {
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "a@example.com"}},
	}}
	email := &fakeScheduledEmailDispatcher{}
	// Cancel buffer larger than the lead time used to arm the schedule, so
	// scheduling succeeds (schedule window uses its own minLead) but the
	// cancel attempt lands inside the buffer.
	orch := newTestScheduleOrchestrator(repo, committee, email, 5*time.Minute, 72*time.Hour, 20*time.Minute)
	draft := repo.addDraft("p1", []string{"c1"})
	scheduledAt := time.Now().UTC().Add(10 * time.Minute)

	if _, err := orch.SendNewsletter(context.Background(), SendNewsletterInput{
		ProjectUID:   "p1",
		NewsletterID: draft.ID,
		Schedule:     true,
		ScheduledAt:  &scheduledAt,
	}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	scheduled := waitForStatus(t, repo, draft.ID, model.StatusScheduled)

	_, err := orch.CancelScheduled(context.Background(), CancelScheduledInput{
		ProjectUID:      "p1",
		NewsletterID:    draft.ID,
		ExpectedVersion: scheduled.Version,
	})
	if !errors.Is(err, domain.ErrCancelWindowClosed) {
		t.Fatalf("expected ErrCancelWindowClosed, got %v", err)
	}
}

// TestCancelScheduledProviderFailureLeavesRowScheduled covers the ordering
// guarantee: a SendGrid cancel failure must not mutate our row.
func TestCancelScheduledProviderFailureLeavesRowScheduled(t *testing.T) {
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "a@example.com"}},
	}}
	email := &fakeScheduledEmailDispatcher{}
	orch := newTestScheduleOrchestrator(repo, committee, email, 5*time.Minute, 72*time.Hour, 5*time.Minute)
	draft := repo.addDraft("p1", []string{"c1"})
	scheduledAt := time.Now().UTC().Add(2 * time.Hour)

	if _, err := orch.SendNewsletter(context.Background(), SendNewsletterInput{
		ProjectUID:   "p1",
		NewsletterID: draft.ID,
		Schedule:     true,
		ScheduledAt:  &scheduledAt,
	}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	scheduled := waitForStatus(t, repo, draft.ID, model.StatusScheduled)

	email.mu.Lock()
	email.cancelErr = errors.New("sendgrid: cancel failed")
	email.mu.Unlock()

	_, err := orch.CancelScheduled(context.Background(), CancelScheduledInput{
		ProjectUID:      "p1",
		NewsletterID:    draft.ID,
		ExpectedVersion: scheduled.Version,
	})
	if err == nil {
		t.Fatal("expected an error from a failed provider cancel")
	}

	still := repo.get(draft.ID)
	if still.Status != model.StatusScheduled {
		t.Fatalf("expected row to remain 'scheduled' after a failed provider cancel, got %q", still.Status)
	}
}
