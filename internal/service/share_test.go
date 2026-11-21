// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

type shareNewsletterFake struct {
	rows map[uuid.UUID]*model.Newsletter
}

func (f *shareNewsletterFake) GetMeta(ctx context.Context, id uuid.UUID) (*model.Newsletter, error) {
	return f.Get(ctx, id)
}

func (f *shareNewsletterFake) Get(_ context.Context, id uuid.UUID) (*model.Newsletter, error) {
	n, ok := f.rows[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return n, nil
}

type shareRepoFake struct {
	recorded    []*model.NewsletterShare
	counts      map[uuid.UUID]int
	countsErr   error
	gotIDs      []uuid.UUID
	deletedHash string
}

func (f *shareRepoFake) RecordShare(_ context.Context, s *model.NewsletterShare) error {
	f.recorded = append(f.recorded, s)
	return nil
}

func (f *shareRepoFake) ShareCounts(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]int, error) {
	f.gotIDs = ids
	if f.countsErr != nil {
		return nil, f.countsErr
	}
	out := map[uuid.UUID]int{}
	for _, id := range ids {
		if c, ok := f.counts[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

func (f *shareRepoFake) ShareStats(context.Context, uuid.UUID) (*model.ShareStats, error) {
	return &model.ShareStats{}, nil
}

func (f *shareRepoFake) DeleteSharesByUser(_ context.Context, userHash string) (int64, error) {
	f.deletedHash = userHash
	return 2, nil
}

func TestShareService_RecordShare(t *testing.T) {
	sentID, draftID, otherID := uuid.New(), uuid.New(), uuid.New()
	newsletters := &shareNewsletterFake{rows: map[uuid.UUID]*model.Newsletter{
		sentID:  {ID: sentID, ProjectUID: "p1", Status: model.StatusSent},
		draftID: {ID: draftID, ProjectUID: "p1", Status: model.StatusDraft},
		otherID: {ID: otherID, ProjectUID: "p2", Status: model.StatusSent},
	}}

	tests := []struct {
		name    string
		in      RecordShareInput
		wantErr error
	}{
		{"sent newsletter records", RecordShareInput{ProjectUID: "p1", NewsletterID: sentID, User: "alice", Channel: model.ShareChannelLinkedIn}, nil},
		{"copy link records", RecordShareInput{ProjectUID: "p1", NewsletterID: sentID, User: "alice", Channel: model.ShareChannelCopyLink}, nil},
		{"unknown channel", RecordShareInput{ProjectUID: "p1", NewsletterID: sentID, User: "alice", Channel: "myspace"}, domain.ErrInvalidRequest},
		{"no user", RecordShareInput{ProjectUID: "p1", NewsletterID: sentID, User: "  ", Channel: model.ShareChannelX}, domain.ErrInvalidRequest},
		{"anonymous principal", RecordShareInput{ProjectUID: "p1", NewsletterID: sentID, User: AnonymousPrincipal, Channel: model.ShareChannelX}, domain.ErrInvalidRequest},
		{"draft is not shareable", RecordShareInput{ProjectUID: "p1", NewsletterID: draftID, User: "alice", Channel: model.ShareChannelX}, domain.ErrNotFound},
		{"other project", RecordShareInput{ProjectUID: "p1", NewsletterID: otherID, User: "alice", Channel: model.ShareChannelX}, domain.ErrNotFound},
		{"unknown newsletter", RecordShareInput{ProjectUID: "p1", NewsletterID: uuid.New(), User: "alice", Channel: model.ShareChannelX}, domain.ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &shareRepoFake{}
			err := NewShareService(newsletters, repo).RecordShare(context.Background(), tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if len(repo.recorded) != 0 {
					t.Fatalf("recorded %d shares on error, want 0", len(repo.recorded))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if len(repo.recorded) != 1 {
				t.Fatalf("recorded %d shares, want 1", len(repo.recorded))
			}
			got := repo.recorded[0]
			if got.NewsletterID != tc.in.NewsletterID || got.Channel != tc.in.Channel {
				t.Errorf("recorded %+v, want newsletter %s channel %s", got, tc.in.NewsletterID, tc.in.Channel)
			}
			if got.UserHash != hashUserPrincipal("alice") || got.UserHash == "alice" || len(got.UserHash) != 64 {
				t.Errorf("user hash = %q, want the 64-char sha256 of the principal", got.UserHash)
			}
		})
	}
}

func TestShareService_ShareCounts(t *testing.T) {
	sharedID, unsharedID, draftID := uuid.New(), uuid.New(), uuid.New()
	repo := &shareRepoFake{counts: map[uuid.UUID]int{sharedID: 3}}
	counts, err := NewShareService(&shareNewsletterFake{}, repo).ShareCounts(context.Background(), []*model.Newsletter{
		{ID: sharedID, Status: model.StatusSent},
		{ID: unsharedID, Status: model.StatusSent},
		{ID: draftID, Status: model.StatusDraft},
	})
	if err != nil {
		t.Fatalf("ShareCounts: %v", err)
	}
	if counts[sharedID] != 3 {
		t.Errorf("shared count = %d, want 3", counts[sharedID])
	}
	if c, ok := counts[unsharedID]; !ok || c != 0 {
		t.Errorf("unshared count = %d (present %v), want 0 and present", c, ok)
	}
	if _, ok := counts[draftID]; ok {
		t.Error("draft has a share count, want it absent")
	}
	if len(repo.gotIDs) != 2 {
		t.Errorf("queried %d ids, want only the 2 sent newsletters", len(repo.gotIDs))
	}

	repo.countsErr = errors.New("db down")
	if _, err := NewShareService(&shareNewsletterFake{}, repo).ShareCounts(context.Background(), []*model.Newsletter{{ID: sharedID, Status: model.StatusSent}}); err == nil {
		t.Error("ShareCounts swallowed a repository error")
	}
}

func TestShareService_DeleteUserShares(t *testing.T) {
	repo := &shareRepoFake{}
	svc := NewShareService(&shareNewsletterFake{}, repo)

	n, err := svc.DeleteUserShares(context.Background(), "  alice ")
	if err != nil || n != 2 {
		t.Fatalf("DeleteUserShares = %d, %v; want 2, nil", n, err)
	}
	if repo.deletedHash != hashUserPrincipal("alice") {
		t.Errorf("deleted hash = %q, want the same hash RecordShare stores for the trimmed principal", repo.deletedHash)
	}

	if _, err := svc.DeleteUserShares(context.Background(), " "); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Errorf("blank principal: err = %v, want ErrInvalidRequest", err)
	}
}
