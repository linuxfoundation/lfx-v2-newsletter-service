// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

func TestAudienceResolve_RefusesCommitteesNotOnTheDraft(t *testing.T) {
	ctx := context.Background()
	f := newAudienceFixture(t, &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "a@example.com"}},
	}})
	n := f.repo.addDraft("p1", []string{"c1"})

	_, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1", "other-committee"}, false)
	assert.ErrorIs(t, err, domain.ErrInvalidRequest)
	assert.Empty(t, f.audiences.begins, "nothing is claimed or read")
	assert.Empty(t, f.committee.calledWith())
}

func TestAudienceResolve_EmptyCommitteeFailsTheAudience(t *testing.T) {
	ctx := context.Background()
	f := newAudienceFixture(t, &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "a@example.com"}},
	}})
	n := f.repo.addDraft("p1", []string{"c1", "c-empty"})

	_, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1", "c-empty"}, false)
	require.NoError(t, err)
	f.svc.Drain(ctx)

	a := waitForAudienceStatus(t, f, n.ID, model.AudienceFailed)
	assert.Equal(t, "a selected committee returned no members", a.Error)
}

func TestAudienceResolve_RefreshRebuildsAReadySnapshot(t *testing.T) {
	ctx := context.Background()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "a@example.com"}},
	}}
	f := newAudienceFixture(t, committee)
	n := f.repo.addDraft("p1", []string{"c1"})
	f.audiences.put(n, []string{"c1"}, model.AudienceReady, []model.CommitteeMember{{Email: "old@example.com"}})

	got, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1"}, false)
	require.NoError(t, err)
	assert.Equal(t, model.AudienceReady, got.Status, "without refresh the ready snapshot is returned as is")
	assert.Empty(t, f.audiences.begins)

	_, err = f.svc.Resolve(ctx, "p1", n.ID, []string{"c1"}, true)
	require.NoError(t, err)
	f.svc.Drain(ctx)
	require.Len(t, f.audiences.begins, 1, "refresh starts a new resolution")
	members, err := f.audiences.ListMembers(ctx, n.ID)
	require.NoError(t, err)
	require.Len(t, members, 1)
	assert.Equal(t, "a@example.com", members[0].Email)
}

func TestAudienceStop_FailsInFlightJobs(t *testing.T) {
	ctx := context.Background()
	block := newBlockingCommittee(&fakeCommitteeClient{members: map[string][]model.CommitteeMember{"c1": {{Email: "a@example.com"}}}})
	f := newAudienceFixture(t, &fakeCommitteeClient{})
	f.svc.committee = block
	n := f.repo.addDraft("p1", []string{"c1"})

	_, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1"}, false)
	require.NoError(t, err)
	f.svc.Stop()
	require.True(t, f.svc.Drain(ctx))

	a := waitForAudienceStatus(t, f, n.ID, model.AudienceFailed)
	assert.Contains(t, a.Error, "restarted")
}

func newCompatOrchestrator(repo *fakeNewsletterRepo, audiences *fakeAudienceRepo, committee *fakeCommitteeClient, email *fakeEmailDispatcher) *SendOrchestrator {
	return NewSendOrchestrator(SendOrchestratorConfig{
		Repo:          repo,
		Audience:      audiences,
		AudiencePurge: audiences,
		Committee:     committee,
		Project:       &fakeProjectClient{},
		Email:         email,
		Unsubscribe:   NewUnsubscribeService(repo, []byte("k"), "https://api.example"),
		Concurrency:   2,
		FanoutEnabled: true,
	})
}

func TestRecipientCount_WithoutNewsletterReadsCommitteesLive(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	repo.unsubs["p1"] = map[string]struct{}{"gone@example.com": {}}
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "a@example.com"}, {Email: "gone@example.com"}},
		"c2": {},
	}}
	orch := newCompatOrchestrator(repo, newFakeAudienceRepo(), committee, &fakeEmailDispatcher{})

	count, err := orch.RecipientCount(ctx, "p1", uuid.Nil, []string{"c1", "c2"})
	require.NoError(t, err)
	assert.Equal(t, 1, count, "unsubscribes are dropped; an empty committee still counts as 0, as before")

	members, err := orch.Recipients(ctx, "p1", uuid.Nil, []string{"c1"})
	require.NoError(t, err)
	require.Len(t, members, 1)
	assert.Equal(t, "a@example.com", members[0].Email)
}

func TestSendNewsletter_NoSnapshotSendsFromALiveRead(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "a@example.com"}, {Email: "b@example.com"}},
	}}
	email := &fakeEmailDispatcher{}
	orch := newCompatOrchestrator(repo, newFakeAudienceRepo(), committee, email)
	draft := repo.addDraft("p1", []string{"c1"})

	_, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: draft.ID})
	require.NoError(t, err)
	orch.Drain(ctx)
	assert.Len(t, email.sends, 2)
}

func TestSendNewsletter_NoSnapshotEmptyCommitteeRefusesTheSend(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	email := &fakeEmailDispatcher{}
	orch := newCompatOrchestrator(repo, newFakeAudienceRepo(), &fakeCommitteeClient{}, email)
	draft := repo.addDraft("p1", []string{"c-empty"})

	_, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: draft.ID})
	assert.ErrorIs(t, err, domain.ErrUnprocessable)
	assert.Empty(t, email.sends)
	got, err := repo.Get(ctx, draft.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusDraft, got.Status, "the newsletter is not used up")
}

func TestSendNewsletter_ProcessingSnapshotStillStopsTheSend(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	audiences := newFakeAudienceRepo()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{"c1": {{Email: "a@example.com"}}}}
	orch := newCompatOrchestrator(repo, audiences, committee, &fakeEmailDispatcher{})
	draft := repo.addDraft("p1", []string{"c1"})
	audiences.put(draft, []string{"c1"}, model.AudienceProcessing, nil)

	_, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: draft.ID})
	var notReady *domain.AudienceNotReadyError
	require.ErrorAs(t, err, &notReady)
	assert.Equal(t, string(model.AudienceProcessing), notReady.Status)
	assert.Empty(t, committee.calledWith(), "no live read once a client has started a snapshot")
}

func TestSendNewsletter_DeletesSnapshotMembersAfterTheSend(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	audiences := newFakeAudienceRepo()
	email := &fakeEmailDispatcher{}
	orch := newCompatOrchestrator(repo, audiences, &fakeCommitteeClient{}, email)
	draft := repo.addDraft("p1", []string{"c1"})
	audiences.put(draft, []string{"c1"}, model.AudienceReady, []model.CommitteeMember{{Email: "a@example.com"}})

	_, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: draft.ID})
	require.NoError(t, err)
	orch.Drain(ctx)

	require.Len(t, email.sends, 1)
	assert.Equal(t, []uuid.UUID{draft.ID}, audiences.purged)
	members, err := audiences.ListMembers(ctx, draft.ID)
	require.NoError(t, err)
	assert.Empty(t, members)
}

func TestAudienceResolve_CommitteeOfAnotherProjectFailsTheAudience(t *testing.T) {
	ctx := context.Background()
	f := newAudienceFixture(t, &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "a@example.com", ProjectUID: "p1"}},
		"c2": {{Email: "b@example.com", ProjectUID: "other-project"}},
	}})
	n := f.repo.addDraft("p1", []string{"c1", "c2"})

	_, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1", "c2"}, false)
	require.NoError(t, err)
	f.svc.Drain(ctx)

	a := waitForAudienceStatus(t, f, n.ID, model.AudienceFailed)
	assert.Equal(t, "a selected committee belongs to another project", a.Error)
}

func TestRecipientCount_LivePathOnlyLogsACommitteeOfAnotherProject(t *testing.T) {
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "a@example.com", ProjectUID: "other-project"}},
	}}
	orch := newCompatOrchestrator(newFakeRepo(), newFakeAudienceRepo(), committee, &fakeEmailDispatcher{})

	count, err := orch.RecipientCount(context.Background(), "p1", uuid.Nil, []string{"c1"})
	require.NoError(t, err, "the read clients used before snapshots behaves as before")
	assert.Equal(t, 1, count)
}

func TestRecipients_RefusedOnceTheNewsletterIsSent(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	audiences := newFakeAudienceRepo()
	orch := newCompatOrchestrator(repo, audiences, &fakeCommitteeClient{}, &fakeEmailDispatcher{})
	n := repo.addDraft("p1", []string{"c1"})
	audiences.put(n, []string{"c1"}, model.AudienceReady, []model.CommitteeMember{{Email: "a@example.com"}})
	repo.mu.Lock()
	repo.drafts[n.ID].Status = model.StatusSent
	repo.mu.Unlock()

	_, err := orch.Recipients(ctx, "p1", n.ID, []string{"c1"})
	assert.ErrorIs(t, err, domain.ErrAlreadySent)
}

func TestSendNewsletter_ReadySnapshotWithPurgedMembersIsStale(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	audiences := newFakeAudienceRepo()
	email := &fakeEmailDispatcher{}
	orch := newCompatOrchestrator(repo, audiences, &fakeCommitteeClient{}, email)
	n := repo.addDraft("p1", []string{"c1"})
	audiences.put(n, []string{"c1"}, model.AudienceReady, []model.CommitteeMember{{Email: "a@example.com"}})
	require.NoError(t, audiences.DeleteMembers(ctx, n.ID))

	_, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: n.ID})
	var notReady *domain.AudienceNotReadyError
	require.ErrorAs(t, err, &notReady)
	assert.Equal(t, domain.AudienceStateStale, notReady.Status)
	assert.Empty(t, email.sends, "the newsletter is not used up on an empty send")
}
