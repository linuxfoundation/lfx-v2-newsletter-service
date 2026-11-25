// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

func newAudienceOrchestrator(repo *fakeNewsletterRepo, audiences *fakeAudienceRepo, email *fakeEmailDispatcher) *SendOrchestrator {
	return NewSendOrchestrator(SendOrchestratorConfig{
		Repo:          repo,
		Audience:      audiences,
		Project:       &fakeProjectClient{},
		Email:         email,
		Unsubscribe:   NewUnsubscribeService(repo, []byte("k"), "https://api.example"),
		Concurrency:   2,
		FanoutEnabled: true,
	})
}

func members(emails ...string) []model.CommitteeMember {
	out := make([]model.CommitteeMember, 0, len(emails))
	for _, e := range emails {
		out = append(out, model.CommitteeMember{Email: e, FirstName: "First", LastName: "Last"})
	}
	return out
}

func requireNotReady(t *testing.T, err error, wantStatus string) {
	t.Helper()
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrAudienceNotReady)
	var notReady *domain.AudienceNotReadyError
	require.True(t, errors.As(err, &notReady), "got %T: %v", err, err)
	assert.Equal(t, wantStatus, notReady.Status)
}

// notReadyCases are the audience states in which nothing may be sent.
var notReadyCases = []struct {
	name   string
	setup  func(a *fakeAudienceRepo, n *model.Newsletter)
	status string
}{
	{"missing", func(*fakeAudienceRepo, *model.Newsletter) {}, domain.AudienceStateNone},
	{"processing", func(a *fakeAudienceRepo, n *model.Newsletter) {
		a.put(n, n.CommitteeUIDs, model.AudienceProcessing, nil)
	}, "processing"},
	{"failed", func(a *fakeAudienceRepo, n *model.Newsletter) {
		a.put(n, n.CommitteeUIDs, model.AudienceFailed, nil)
	}, "failed"},
	{"ready for a different committee list", func(a *fakeAudienceRepo, n *model.Newsletter) {
		a.put(n, []string{"other"}, model.AudienceReady, members("a@example.com"))
	}, domain.AudienceStateStale},
	{"ready for a subset of the committee list", func(a *fakeAudienceRepo, n *model.Newsletter) {
		a.put(n, []string{"c1"}, model.AudienceReady, members("a@example.com"))
	}, domain.AudienceStateStale},
}

func TestSendNewsletter_RequiresReadyAudience(t *testing.T) {
	for _, tc := range notReadyCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := newFakeRepo()
			audiences := newFakeAudienceRepo()
			email := &fakeEmailDispatcher{}
			orch := newAudienceOrchestrator(repo, audiences, email)
			n := repo.addDraft("p1", []string{"c1", "c2"})
			tc.setup(audiences, n)

			_, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: n.ID})
			requireNotReady(t, err, tc.status)

			orch.Drain(ctx)
			assert.Empty(t, email.sends, "nothing is sent")
			assert.Equal(t, model.StatusDraft, repo.get(n.ID).Status, "the newsletter stays a draft")
		})
	}
}

func TestSendNewsletter_FansOutFromReadySnapshot(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	audiences := newFakeAudienceRepo()
	email := &fakeEmailDispatcher{}
	orch := newAudienceOrchestrator(repo, audiences, email)
	n := repo.addDraft("p1", []string{"c2", "c1"})
	audiences.put(n, []string{"c1", "c2"}, model.AudienceReady, members("a@example.com", "b@example.com", "c@example.com"))

	res, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: n.ID})
	require.NoError(t, err)
	orch.Drain(ctx)

	assert.Equal(t, 3, res.TotalRecipients)
	assert.ElementsMatch(t, []string{"a@example.com", "b@example.com", "c@example.com"}, email.recipients())
	got := repo.get(n.ID)
	assert.Equal(t, model.StatusSent, got.Status)
	assert.Equal(t, 3, got.TotalRecipients)
}

func TestSendNewsletter_AppliesUnsubscribeFilterAtSendTime(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	audiences := newFakeAudienceRepo()
	email := &fakeEmailDispatcher{}
	orch := newAudienceOrchestrator(repo, audiences, email)
	n := repo.addDraft("p1", []string{"c1"})
	audiences.put(n, []string{"c1"}, model.AudienceReady, members("a@example.com", "late@example.com"))
	// The address unsubscribed after the snapshot was taken.
	repo.unsubs["p1"] = map[string]struct{}{"late@example.com": {}}
	repo.unsubs["p2"] = map[string]struct{}{"a@example.com": {}}

	res, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: n.ID})
	require.NoError(t, err)
	orch.Drain(ctx)

	assert.Equal(t, 1, res.TotalRecipients, "total_recipients is the filtered count")
	assert.Equal(t, []string{"a@example.com"}, email.recipients(), "another project's opt-out does not apply")
	assert.Equal(t, 1, repo.get(n.ID).TotalRecipients)
}

func TestSendNewsletter_ReadySnapshotWithNoRecipientsSettlesSent(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	audiences := newFakeAudienceRepo()
	email := &fakeEmailDispatcher{}
	orch := newAudienceOrchestrator(repo, audiences, email)
	n := repo.addDraft("p1", []string{"c1"})
	audiences.put(n, []string{"c1"}, model.AudienceReady, nil)

	res, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: n.ID})
	require.NoError(t, err)
	assert.Zero(t, res.TotalRecipients)
	assert.Equal(t, model.StatusSent, repo.get(n.ID).Status)
	assert.Empty(t, email.sends)
}

func TestSendNewsletter_AudienceOfAnotherNewsletterIsNotUsed(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	audiences := newFakeAudienceRepo()
	email := &fakeEmailDispatcher{}
	orch := newAudienceOrchestrator(repo, audiences, email)
	a := repo.addDraft("p1", []string{"c1"})
	b := repo.addDraft("p1", []string{"c1"})
	audiences.put(a, []string{"c1"}, model.AudienceReady, members("a@example.com"))

	_, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: b.ID})
	requireNotReady(t, err, domain.AudienceStateNone)
	assert.Empty(t, email.sends)
}

func TestSendNewsletter_AudienceFromAnotherProjectIsNotUsed(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	audiences := newFakeAudienceRepo()
	email := &fakeEmailDispatcher{}
	orch := newAudienceOrchestrator(repo, audiences, email)
	n := repo.addDraft("p1", []string{"c1"})
	audiences.put(n, []string{"c1"}, model.AudienceReady, members("a@example.com"))
	audiences.audiences[n.ID].ProjectUID = "other-project"

	_, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: n.ID})
	requireNotReady(t, err, domain.AudienceStateNone)
	assert.Empty(t, email.sends)
}

func TestSendNewsletter_AudienceReadFailureIsNotReportedAsNotReady(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	audiences := newFakeAudienceRepo()
	audiences.readErr = errors.New("db down")
	orch := newAudienceOrchestrator(repo, audiences, &fakeEmailDispatcher{})
	n := repo.addDraft("p1", []string{"c1"})

	_, err := orch.SendNewsletter(ctx, SendNewsletterInput{ProjectUID: "p1", NewsletterID: n.ID})
	require.Error(t, err)
	assert.NotErrorIs(t, err, domain.ErrAudienceNotReady)
}

func TestRecipientCountAndRecipients_RequireReadyAudience(t *testing.T) {
	for _, tc := range notReadyCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := newFakeRepo()
			audiences := newFakeAudienceRepo()
			orch := newAudienceOrchestrator(repo, audiences, &fakeEmailDispatcher{})
			n := repo.addDraft("p1", []string{"c1", "c2"})
			tc.setup(audiences, n)

			_, err := orch.RecipientCount(ctx, "p1", n.ID, []string{"c1", "c2"})
			requireNotReady(t, err, tc.status)
			_, err = orch.Recipients(ctx, "p1", n.ID, []string{"c1", "c2"})
			requireNotReady(t, err, tc.status)
		})
	}
}

func TestRecipientCountAndRecipients_ReadAudienceSnapshot(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	audiences := newFakeAudienceRepo()
	orch := newAudienceOrchestrator(repo, audiences, &fakeEmailDispatcher{})
	n := repo.addDraft("p1", []string{"c1", "c2"})
	audiences.put(n, []string{"c1", "c2"}, model.AudienceReady, members("b@example.com", "a@example.com"))

	count, err := orch.RecipientCount(ctx, "p1", n.ID, []string{"c2", "c1"})
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	got, err := orch.Recipients(ctx, "p1", n.ID, []string{"c1", "c2"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "a@example.com", got[0].Email)
	assert.Equal(t, "b@example.com", got[1].Email)
}

func TestRecipientCount_ComparesTheCallersCommitteeList(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	audiences := newFakeAudienceRepo()
	orch := newAudienceOrchestrator(repo, audiences, &fakeEmailDispatcher{})
	n := repo.addDraft("p1", []string{"c1"})
	audiences.put(n, []string{"c1"}, model.AudienceReady, members("a@example.com"))

	_, err := orch.RecipientCount(ctx, "p1", n.ID, []string{"c1", "c2"})
	requireNotReady(t, err, domain.AudienceStateStale)
}

func TestRecipientCountAndRecipients_Validation(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	audiences := newFakeAudienceRepo()
	orch := newAudienceOrchestrator(repo, audiences, &fakeEmailDispatcher{})
	n := repo.addDraft("p1", []string{"c1"})
	audiences.put(n, []string{"c1"}, model.AudienceReady, members("a@example.com"))

	for name, call := range map[string]func(project string, id uuid.UUID, uids []string) error{
		"count": func(project string, id uuid.UUID, uids []string) error {
			_, err := orch.RecipientCount(ctx, project, id, uids)
			return err
		},
		"recipients": func(project string, id uuid.UUID, uids []string) error {
			_, err := orch.Recipients(ctx, project, id, uids)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.ErrorIs(t, call(" ", n.ID, []string{"c1"}), domain.ErrInvalidRequest)
			assert.ErrorIs(t, call("p1", n.ID, nil), domain.ErrInvalidRequest)
			assert.ErrorIs(t, call("p1", uuid.New(), []string{"c1"}), domain.ErrNotFound)
			assert.ErrorIs(t, call("p2", n.ID, []string{"c1"}), domain.ErrNotFound, "another project cannot read the audience")
		})
	}
}
