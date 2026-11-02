// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

// fakeAudienceRepo is an in-memory port.AudienceRepository with the same state
// rules as the Postgres one: BeginResolve claims only when the row is not
// processing, and the terminal writes match only a processing row.
type fakeAudienceRepo struct {
	mu        sync.Mutex
	audiences map[uuid.UUID]*model.Audience
	members   map[uuid.UUID][]model.CommitteeMember

	// begins records the arguments of every BeginResolve call.
	begins []beginCall
	// replaceErr, when set, is returned by ReplaceMembers.
	replaceErr error
	// readErr, when set, is returned by Get.
	readErr error
}

type beginCall struct {
	NewsletterID  uuid.UUID
	ProjectUID    string
	CommitteeUIDs []string
}

func newFakeAudienceRepo() *fakeAudienceRepo {
	return &fakeAudienceRepo{
		audiences: make(map[uuid.UUID]*model.Audience),
		members:   make(map[uuid.UUID][]model.CommitteeMember),
	}
}

func (r *fakeAudienceRepo) BeginResolve(_ context.Context, id uuid.UUID, projectUID string, committeeUIDs []string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.begins = append(r.begins, beginCall{NewsletterID: id, ProjectUID: projectUID, CommitteeUIDs: append([]string(nil), committeeUIDs...)})
	if a, ok := r.audiences[id]; ok && a.Status == model.AudienceProcessing {
		return false, nil
	}
	now := time.Now()
	r.audiences[id] = &model.Audience{
		NewsletterID:  id,
		ProjectUID:    projectUID,
		CommitteeUIDs: append([]string(nil), committeeUIDs...),
		Status:        model.AudienceProcessing,
		RequestedAt:   now,
		UpdatedAt:     now,
	}
	return true, nil
}

func (r *fakeAudienceRepo) ReplaceMembers(_ context.Context, id uuid.UUID, members []model.CommitteeMember) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.replaceErr != nil {
		return r.replaceErr
	}
	a, ok := r.audiences[id]
	if !ok || a.Status != model.AudienceProcessing {
		return domain.ErrNotFound
	}
	r.members[id] = append([]model.CommitteeMember(nil), members...)
	return nil
}

func (r *fakeAudienceRepo) MarkReady(_ context.Context, id uuid.UUID, count, skipped int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.audiences[id]
	if !ok || a.Status != model.AudienceProcessing {
		return domain.ErrNotFound
	}
	now := time.Now()
	a.Status = model.AudienceReady
	a.RecipientCount = count
	a.SkippedNoEmail = skipped
	a.ResolvedAt = &now
	a.UpdatedAt = now
	return nil
}

func (r *fakeAudienceRepo) MarkFailed(_ context.Context, id uuid.UUID, msg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.audiences[id]
	if !ok || a.Status != model.AudienceProcessing {
		return domain.ErrNotFound
	}
	a.Status = model.AudienceFailed
	a.Error = msg
	a.UpdatedAt = time.Now()
	return nil
}

func (r *fakeAudienceRepo) Get(_ context.Context, id uuid.UUID) (*model.Audience, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.readErr != nil {
		return nil, r.readErr
	}
	a, ok := r.audiences[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *a
	return &cp, nil
}

func (r *fakeAudienceRepo) ListMembers(_ context.Context, id uuid.UUID) ([]model.CommitteeMember, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]model.CommitteeMember(nil), r.members[id]...)
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	return out, nil
}

func (r *fakeAudienceRepo) FailStale(_ context.Context, olderThan time.Duration) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, a := range r.audiences {
		if a.Status == model.AudienceProcessing && time.Since(a.UpdatedAt) > olderThan {
			a.Status = model.AudienceFailed
			a.Error = "audience resolution did not finish"
			n++
		}
	}
	return n, nil
}

// put stores a ready audience with the given members, as a finished job would.
func (r *fakeAudienceRepo) put(n *model.Newsletter, committeeUIDs []string, status model.AudienceStatus, members []model.CommitteeMember) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	a := &model.Audience{
		NewsletterID:   n.ID,
		ProjectUID:     n.ProjectUID,
		CommitteeUIDs:  canonicalCommitteeUIDs(committeeUIDs),
		Status:         status,
		RecipientCount: len(members),
		RequestedAt:    now,
		UpdatedAt:      now,
	}
	if status == model.AudienceReady {
		a.ResolvedAt = &now
	}
	r.audiences[n.ID] = a
	r.members[n.ID] = append([]model.CommitteeMember(nil), members...)
}

// committeeSnapshot is a port.AudienceReader that builds a ready audience on
// demand from a fakeCommitteeClient, for tests of the send path that predate
// snapshots. It reads the newsletter's current committee list, so the snapshot
// always matches it.
type committeeSnapshot struct {
	repo      *fakeNewsletterRepo
	committee *fakeCommitteeClient
}

func newCommitteeSnapshot(repo *fakeNewsletterRepo, committee *fakeCommitteeClient) *committeeSnapshot {
	return &committeeSnapshot{repo: repo, committee: committee}
}

func (s *committeeSnapshot) members(ctx context.Context, n model.Newsletter) []model.CommitteeMember {
	lists := make([]model.CommitteeMembers, 0, len(n.CommitteeUIDs))
	for _, uid := range n.CommitteeUIDs {
		list, _ := s.committee.ListMembers(ctx, uid)
		lists = append(lists, list)
	}
	return buildAudienceMembers(lists, nil).Members
}

func (s *committeeSnapshot) Get(ctx context.Context, id uuid.UUID) (*model.Audience, error) {
	s.repo.mu.Lock()
	n, ok := s.repo.drafts[id]
	var cp model.Newsletter
	if ok {
		cp = *n
	}
	s.repo.mu.Unlock()
	if !ok {
		return nil, domain.ErrNotFound
	}
	now := time.Now()
	return &model.Audience{
		NewsletterID:   id,
		ProjectUID:     cp.ProjectUID,
		CommitteeUIDs:  canonicalCommitteeUIDs(cp.CommitteeUIDs),
		Status:         model.AudienceReady,
		RecipientCount: len(s.members(ctx, cp)),
		RequestedAt:    now,
		ResolvedAt:     &now,
		UpdatedAt:      now,
	}, nil
}

func (s *committeeSnapshot) ListMembers(ctx context.Context, id uuid.UUID) ([]model.CommitteeMember, error) {
	s.repo.mu.Lock()
	n, ok := s.repo.drafts[id]
	var cp model.Newsletter
	if ok {
		cp = *n
	}
	s.repo.mu.Unlock()
	if !ok {
		return nil, domain.ErrNotFound
	}
	return s.members(ctx, cp), nil
}
