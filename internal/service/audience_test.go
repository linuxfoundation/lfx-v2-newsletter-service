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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// blockingCommittee holds every ListMembers call until release is closed, so a
// test can observe the audience while its job is in flight.
type blockingCommittee struct {
	inner   *fakeCommitteeClient
	release chan struct{}
	started chan struct{}
	once    sync.Once
}

func newBlockingCommittee(inner *fakeCommitteeClient) *blockingCommittee {
	return &blockingCommittee{inner: inner, release: make(chan struct{}), started: make(chan struct{})}
}

func (b *blockingCommittee) ListMembers(ctx context.Context, uid string) (model.CommitteeMembers, error) {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return model.CommitteeMembers{}, ctx.Err()
	}
	return b.inner.ListMembers(ctx, uid)
}

type audienceFixture struct {
	repo      *fakeNewsletterRepo
	audiences *fakeAudienceRepo
	committee *fakeCommitteeClient
	svc       *AudienceService
}

func newAudienceFixture(t *testing.T, committee *fakeCommitteeClient) *audienceFixture {
	t.Helper()
	repo := newFakeRepo()
	audiences := newFakeAudienceRepo()
	return &audienceFixture{
		repo:      repo,
		audiences: audiences,
		committee: committee,
		svc: NewAudienceService(AudienceServiceConfig{
			Newsletters:  repo,
			Audiences:    audiences,
			Committee:    committee,
			Unsubscribed: repo,
			JobTimeout:   time.Minute,
		}),
	}
}

func waitForAudienceStatus(t *testing.T, f *audienceFixture, id uuid.UUID, want model.AudienceStatus) *model.Audience {
	t.Helper()
	require.Eventually(t, func() bool {
		a, err := f.audiences.Get(context.Background(), id)
		return err == nil && a.Status == want
	}, 5*time.Second, 5*time.Millisecond, "audience never reached %s", want)
	a, err := f.audiences.Get(context.Background(), id)
	require.NoError(t, err)
	return a
}

func TestAudienceResolve_BuildsReadySnapshot(t *testing.T) {
	ctx := context.Background()
	f := newAudienceFixture(t, &fakeCommitteeClient{
		members: map[string][]model.CommitteeMember{
			"c1": {{Email: "Alice@Example.com", FirstName: "First", LastName: "Last"}, {Email: "bob@example.com"}},
			"c2": {{Email: "alice@example.com"}, {Email: "carol@example.com"}, {Email: "gone@example.com"}},
		},
		skippedNoEmail: 1,
	})
	f.repo.unsubs["p1"] = map[string]struct{}{"gone@example.com": {}}
	n := f.repo.addDraft("p1", []string{"c1", "c2"})

	got, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c2", "c1"}, false)
	require.NoError(t, err)
	assert.Equal(t, n.ID, got.NewsletterID)
	assert.Equal(t, model.AudienceProcessing, got.Status, "the caller always gets the processing audience, even when the job is quick")
	f.svc.Drain(ctx)

	a := waitForAudienceStatus(t, f, n.ID, model.AudienceReady)
	assert.Equal(t, 3, a.RecipientCount)
	assert.Equal(t, 2, a.SkippedNoEmail, "one per committee from the source")
	assert.Equal(t, []string{"c1", "c2"}, a.CommitteeUIDs, "stored sorted")
	assert.NotNil(t, a.ResolvedAt)

	members, err := f.audiences.ListMembers(ctx, n.ID)
	require.NoError(t, err)
	emails := make([]string, 0, len(members))
	for _, m := range members {
		emails = append(emails, m.Email)
	}
	assert.Equal(t, []string{"alice@example.com", "bob@example.com", "carol@example.com"}, emails)
	assert.Equal(t, "Last", members[0].LastName)

	assert.ElementsMatch(t, []string{"c1", "c2"}, f.committee.calledWith(), "each committee is read once")
	require.Len(t, f.audiences.begins, 1)
	assert.Equal(t, beginCall{NewsletterID: n.ID, ProjectUID: "p1", CommitteeUIDs: []string{"c1", "c2"}}, f.audiences.begins[0])
}

func TestAudienceResolve_ReturnsProcessingImmediately(t *testing.T) {
	ctx := context.Background()
	block := newBlockingCommittee(&fakeCommitteeClient{members: map[string][]model.CommitteeMember{"c1": {{Email: "a@example.com"}}}})
	f := newAudienceFixture(t, &fakeCommitteeClient{})
	f.svc.committee = block
	n := f.repo.addDraft("p1", []string{"c1"})

	got, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1"}, false)
	require.NoError(t, err)
	assert.Equal(t, model.AudienceProcessing, got.Status)

	close(block.release)
	f.svc.Drain(ctx)
	waitForAudienceStatus(t, f, n.ID, model.AudienceReady)
}

func TestAudienceResolve_SecondCallWhileProcessingStartsNoSecondJob(t *testing.T) {
	ctx := context.Background()
	inner := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{"c1": {{Email: "a@example.com"}}}}
	block := newBlockingCommittee(inner)
	f := newAudienceFixture(t, inner)
	f.svc.committee = block
	n := f.repo.addDraft("p1", []string{"c1"})

	first, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1"}, false)
	require.NoError(t, err)
	<-block.started

	second, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1"}, false)
	require.NoError(t, err)
	assert.Equal(t, model.AudienceProcessing, second.Status)
	assert.Equal(t, first.RequestedAt, second.RequestedAt, "the in-flight audience is returned unchanged")

	close(block.release)
	f.svc.Drain(ctx)
	waitForAudienceStatus(t, f, n.ID, model.AudienceReady)

	assert.Len(t, f.audiences.begins, 2, "both calls tried to claim")
	assert.Equal(t, []string{"c1"}, inner.calledWith(), "only one job read the committee")
}

func TestAudienceResolve_ConcurrentCallsClaimOnce(t *testing.T) {
	ctx := context.Background()
	inner := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{"c1": {{Email: "a@example.com"}}}}
	block := newBlockingCommittee(inner)
	f := newAudienceFixture(t, inner)
	f.svc.committee = block
	n := f.repo.addDraft("p1", []string{"c1"})

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1"}, false)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	close(block.release)
	f.svc.Drain(ctx)

	assert.Equal(t, []string{"c1"}, inner.calledWith(), "exactly one job ran")
}

func TestAudienceResolve_ReadyForSameCommitteesIsReturnedWithoutResolvingAgain(t *testing.T) {
	ctx := context.Background()
	f := newAudienceFixture(t, &fakeCommitteeClient{members: map[string][]model.CommitteeMember{"c1": {{Email: "a@example.com"}}}})
	n := f.repo.addDraft("p1", []string{"c1"})
	f.audiences.put(n, []string{"c1"}, model.AudienceReady, []model.CommitteeMember{{Email: "a@example.com"}})

	got, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1"}, false)
	require.NoError(t, err)
	assert.Equal(t, model.AudienceReady, got.Status)
	assert.Empty(t, f.audiences.begins)
	assert.Empty(t, f.committee.calledWith())
}

func TestAudienceResolve_ReadyForDifferentCommitteesResolvesAgain(t *testing.T) {
	ctx := context.Background()
	f := newAudienceFixture(t, &fakeCommitteeClient{members: map[string][]model.CommitteeMember{
		"c1": {{Email: "a@example.com"}},
		"c2": {{Email: "b@example.com"}},
	}})
	n := f.repo.addDraft("p1", []string{"c1", "c2"})
	f.audiences.put(n, []string{"c1"}, model.AudienceReady, []model.CommitteeMember{{Email: "a@example.com"}})

	got, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1", "c2"}, false)
	require.NoError(t, err)
	assert.NotEqual(t, model.AudienceFailed, got.Status)
	f.svc.Drain(ctx)

	a := waitForAudienceStatus(t, f, n.ID, model.AudienceReady)
	assert.Equal(t, 2, a.RecipientCount)
	assert.Equal(t, []string{"c1", "c2"}, a.CommitteeUIDs)
}

func TestAudienceResolve_FailedAudienceCanBeRetried(t *testing.T) {
	ctx := context.Background()
	f := newAudienceFixture(t, &fakeCommitteeClient{members: map[string][]model.CommitteeMember{"c1": {{Email: "a@example.com"}}}})
	n := f.repo.addDraft("p1", []string{"c1"})
	f.audiences.put(n, []string{"c1"}, model.AudienceFailed, nil)

	_, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1"}, false)
	require.NoError(t, err)
	f.svc.Drain(ctx)
	waitForAudienceStatus(t, f, n.ID, model.AudienceReady)
}

func TestAudienceResolve_FailsOnCommitteeError(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantMsg string
	}{
		{"committee not found", pkgerrors.NewNotFound("committee not found"), "a selected committee was not found"},
		{"upstream down", pkgerrors.NewServiceUnavailable("query service unavailable"), "could not read the committee members"},
		{"timeout", context.DeadlineExceeded, "resolving the audience took too long"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newAudienceFixture(t, &fakeCommitteeClient{err: tc.err})
			n := f.repo.addDraft("p1", []string{"c1", "c2"})

			_, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1", "c2"}, false)
			require.NoError(t, err)
			f.svc.Drain(ctx)

			a := waitForAudienceStatus(t, f, n.ID, model.AudienceFailed)
			assert.Equal(t, tc.wantMsg, a.Error)
			assert.Nil(t, a.ResolvedAt)
			members, err := f.audiences.ListMembers(ctx, n.ID)
			require.NoError(t, err)
			assert.Empty(t, members, "a failed audience has no members")
		})
	}
}

func TestAudienceResolve_OneFailingCommitteeFailsWholeAudience(t *testing.T) {
	ctx := context.Background()
	f := newAudienceFixture(t, &fakeCommitteeClient{members: map[string][]model.CommitteeMember{"c1": {{Email: "a@example.com"}}}})
	f.svc.committee = committeeByUID{
		"c1": f.committee,
		"c2": &fakeCommitteeClient{err: errors.New("boom")},
	}
	n := f.repo.addDraft("p1", []string{"c1", "c2"})

	_, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1", "c2"}, false)
	require.NoError(t, err)
	f.svc.Drain(ctx)

	waitForAudienceStatus(t, f, n.ID, model.AudienceFailed)
	f.audiences.mu.Lock()
	defer f.audiences.mu.Unlock()
	assert.Empty(t, f.audiences.members[n.ID], "no partial snapshot")
}

// committeeByUID routes each committee to its own fake.
type committeeByUID map[string]*fakeCommitteeClient

func (c committeeByUID) ListMembers(ctx context.Context, uid string) (model.CommitteeMembers, error) {
	return c[uid].ListMembers(ctx, uid)
}

func TestAudienceResolve_FailsWhenMembersCannotBeStored(t *testing.T) {
	ctx := context.Background()
	f := newAudienceFixture(t, &fakeCommitteeClient{members: map[string][]model.CommitteeMember{"c1": {{Email: "a@example.com"}}}})
	f.audiences.replaceErr = errors.New("db down")
	n := f.repo.addDraft("p1", []string{"c1"})

	_, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1"}, false)
	require.NoError(t, err)
	f.svc.Drain(ctx)

	waitForAudienceStatus(t, f, n.ID, model.AudienceFailed)
}

func TestAudienceResolve_Validation(t *testing.T) {
	ctx := context.Background()
	f := newAudienceFixture(t, &fakeCommitteeClient{})
	n := f.repo.addDraft("p1", []string{"c1"})

	t.Run("empty project", func(t *testing.T) {
		_, err := f.svc.Resolve(ctx, "  ", n.ID, []string{"c1"}, false)
		assert.ErrorIs(t, err, domain.ErrInvalidRequest)
	})
	t.Run("no committees", func(t *testing.T) {
		_, err := f.svc.Resolve(ctx, "p1", n.ID, nil, false)
		assert.ErrorIs(t, err, domain.ErrInvalidRequest)
	})
	t.Run("blank committee", func(t *testing.T) {
		_, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1", " "}, false)
		assert.ErrorIs(t, err, domain.ErrInvalidRequest)
	})
	t.Run("unknown newsletter", func(t *testing.T) {
		_, err := f.svc.Resolve(ctx, "p1", uuid.New(), []string{"c1"}, false)
		assert.ErrorIs(t, err, domain.ErrNotFound)
	})
	t.Run("newsletter in another project", func(t *testing.T) {
		_, err := f.svc.Resolve(ctx, "p2", n.ID, []string{"c1"}, false)
		assert.ErrorIs(t, err, domain.ErrNotFound)
	})
	assert.Empty(t, f.audiences.begins, "nothing was claimed")
}

func TestAudienceResolve_RejectsNewslettersThatAreNotDrafts(t *testing.T) {
	tests := []struct {
		status model.Status
		want   error
	}{
		{model.StatusSending, domain.ErrSendInProgress},
		{model.StatusScheduled, domain.ErrScheduled},
		{model.StatusSent, domain.ErrAlreadySent},
	}
	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			ctx := context.Background()
			f := newAudienceFixture(t, &fakeCommitteeClient{})
			n := f.repo.addDraft("p1", []string{"c1"})
			f.repo.mu.Lock()
			f.repo.drafts[n.ID].Status = tc.status
			f.repo.mu.Unlock()

			_, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1"}, false)
			assert.ErrorIs(t, err, tc.want)
			assert.Empty(t, f.audiences.begins)
		})
	}
}

func TestAudienceResolve_CommitteeListIsTrimmedDedupedAndSorted(t *testing.T) {
	ctx := context.Background()
	f := newAudienceFixture(t, &fakeCommitteeClient{})
	n := f.repo.addDraft("p1", []string{"c2", "c1"})

	_, err := f.svc.Resolve(ctx, "p1", n.ID, []string{" c2 ", "c1", "c2"}, false)
	require.NoError(t, err)
	f.svc.Drain(ctx)

	require.Len(t, f.audiences.begins, 1)
	assert.Equal(t, []string{"c1", "c2"}, f.audiences.begins[0].CommitteeUIDs)
	assert.ElementsMatch(t, []string{"c1", "c2"}, f.committee.calledWith())
}

func TestAudienceGet(t *testing.T) {
	ctx := context.Background()
	f := newAudienceFixture(t, &fakeCommitteeClient{})
	n := f.repo.addDraft("p1", []string{"c1"})

	_, err := f.svc.Get(ctx, "p1", n.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound, "never resolved")

	f.audiences.put(n, []string{"c1"}, model.AudienceReady, []model.CommitteeMember{{Email: "a@example.com"}})
	got, err := f.svc.Get(ctx, "p1", n.ID)
	require.NoError(t, err)
	assert.Equal(t, model.AudienceReady, got.Status)

	_, err = f.svc.Get(ctx, "p2", n.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound, "another project cannot read it")

	_, err = f.svc.Get(ctx, " ", n.ID)
	assert.ErrorIs(t, err, domain.ErrInvalidRequest)
}

func TestAudienceStaleSweepFailsProcessingRows(t *testing.T) {
	ctx := context.Background()
	f := newAudienceFixture(t, &fakeCommitteeClient{})
	stale := f.repo.addDraft("p1", []string{"c1"})
	fresh := f.repo.addDraft("p1", []string{"c1"})
	ready := f.repo.addDraft("p1", []string{"c1"})
	f.audiences.put(stale, []string{"c1"}, model.AudienceProcessing, nil)
	f.audiences.put(fresh, []string{"c1"}, model.AudienceProcessing, nil)
	f.audiences.put(ready, []string{"c1"}, model.AudienceReady, nil)
	f.audiences.audiences[stale.ID].UpdatedAt = time.Now().Add(-time.Hour)

	n, err := f.audiences.FailStale(ctx, 40*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	a, _ := f.audiences.Get(ctx, stale.ID)
	assert.Equal(t, model.AudienceFailed, a.Status)
	a, _ = f.audiences.Get(ctx, fresh.ID)
	assert.Equal(t, model.AudienceProcessing, a.Status)
	a, _ = f.audiences.Get(ctx, ready.ID)
	assert.Equal(t, model.AudienceReady, a.Status)

	// A stale row can be resolved again.
	_, err = f.svc.Resolve(ctx, "p1", stale.ID, []string{"c1"}, false)
	require.NoError(t, err)
	f.svc.Drain(ctx)
}

func TestAudienceDrain_ReturnsFalseWhenJobsOutlastContext(t *testing.T) {
	ctx := context.Background()
	inner := &fakeCommitteeClient{}
	block := newBlockingCommittee(inner)
	f := newAudienceFixture(t, inner)
	f.svc.committee = block
	n := f.repo.addDraft("p1", []string{"c1"})

	_, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1"}, false)
	require.NoError(t, err)

	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	assert.False(t, f.svc.Drain(short))

	close(block.release)
	assert.True(t, f.svc.Drain(ctx))
}

func TestAudienceJob_SurvivesRequestCancellation(t *testing.T) {
	inner := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{"c1": {{Email: "a@example.com"}}}}
	block := newBlockingCommittee(inner)
	f := newAudienceFixture(t, inner)
	f.svc.committee = block
	n := f.repo.addDraft("p1", []string{"c1"})

	reqCtx, cancel := context.WithCancel(context.Background())
	_, err := f.svc.Resolve(reqCtx, "p1", n.ID, []string{"c1"}, false)
	require.NoError(t, err)
	cancel()
	close(block.release)
	f.svc.Drain(context.Background())

	waitForAudienceStatus(t, f, n.ID, model.AudienceReady)
}

// slowPersistAudienceRepo is a fakeAudienceRepo whose ReplaceMembers runs until
// its deadline passes, and whose MarkFailed, like Postgres, cannot run on an
// expired context.
type slowPersistAudienceRepo struct {
	*fakeAudienceRepo
}

func (r *slowPersistAudienceRepo) ReplaceMembers(ctx context.Context, _ uuid.UUID, _ []model.CommitteeMember) error {
	<-ctx.Done()
	return ctx.Err()
}

func (r *slowPersistAudienceRepo) MarkFailed(ctx context.Context, id uuid.UUID, msg string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.fakeAudienceRepo.MarkFailed(ctx, id, msg)
}

func TestAudienceResolve_MarksFailedAfterAPersistTimeout(t *testing.T) {
	prev := audiencePersistTimeout
	audiencePersistTimeout = 50 * time.Millisecond
	t.Cleanup(func() { audiencePersistTimeout = prev })

	ctx := context.Background()
	committee := &fakeCommitteeClient{members: map[string][]model.CommitteeMember{"c1": {{Email: "a@example.com"}}}}
	f := newAudienceFixture(t, committee)
	slow := &slowPersistAudienceRepo{fakeAudienceRepo: f.audiences}
	f.svc = NewAudienceService(AudienceServiceConfig{
		Newsletters:  f.repo,
		Audiences:    slow,
		Committee:    committee,
		Unsubscribed: f.repo,
		JobTimeout:   time.Minute,
	})
	n := f.repo.addDraft("p1", []string{"c1"})

	_, err := f.svc.Resolve(ctx, "p1", n.ID, []string{"c1"}, false)
	require.NoError(t, err)
	f.svc.Drain(ctx)

	waitForAudienceStatus(t, f, n.ID, model.AudienceFailed)
}
