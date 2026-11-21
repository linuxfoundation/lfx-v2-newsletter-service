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

type pageViewRepoFake struct {
	recorded    []*model.NewsletterPageView
	counts      map[uuid.UUID]int
	countsErr   error
	gotIDs      []uuid.UUID
	deletedHash string
}

func (f *pageViewRepoFake) RecordPageView(_ context.Context, v *model.NewsletterPageView) error {
	f.recorded = append(f.recorded, v)
	return nil
}

func (f *pageViewRepoFake) PageViewCounts(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]int, error) {
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

func (f *pageViewRepoFake) DeletePageViewsByUser(_ context.Context, userHash string) (int64, error) {
	f.deletedHash = userHash
	return 3, nil
}

func TestPageViewService_RecordPageView(t *testing.T) {
	sentID, draftID, otherID := uuid.New(), uuid.New(), uuid.New()
	newsletters := &shareNewsletterFake{rows: map[uuid.UUID]*model.Newsletter{
		sentID:  {ID: sentID, ProjectUID: "p1", Status: model.StatusSent},
		draftID: {ID: draftID, ProjectUID: "p1", Status: model.StatusDraft},
		otherID: {ID: otherID, ProjectUID: "p2", Status: model.StatusSent},
	}}

	tests := []struct {
		name    string
		in      RecordPageViewInput
		wantErr error
	}{
		{"sent newsletter records", RecordPageViewInput{ProjectUID: "p1", NewsletterID: sentID, User: "alice"}, nil},
		{"no user", RecordPageViewInput{ProjectUID: "p1", NewsletterID: sentID, User: "  "}, domain.ErrInvalidRequest},
		{"anonymous principal", RecordPageViewInput{ProjectUID: "p1", NewsletterID: sentID, User: AnonymousPrincipal}, domain.ErrInvalidRequest},
		{"draft has no page", RecordPageViewInput{ProjectUID: "p1", NewsletterID: draftID, User: "alice"}, domain.ErrNotFound},
		{"other project", RecordPageViewInput{ProjectUID: "p1", NewsletterID: otherID, User: "alice"}, domain.ErrNotFound},
		{"unknown newsletter", RecordPageViewInput{ProjectUID: "p1", NewsletterID: uuid.New(), User: "alice"}, domain.ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &pageViewRepoFake{}
			err := NewPageViewService(newsletters, repo).RecordPageView(context.Background(), tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if len(repo.recorded) != 0 {
					t.Fatalf("recorded %d views on error, want 0", len(repo.recorded))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if len(repo.recorded) != 1 {
				t.Fatalf("recorded %d views, want 1", len(repo.recorded))
			}
			got := repo.recorded[0]
			if got.NewsletterID != tc.in.NewsletterID {
				t.Errorf("recorded newsletter %s, want %s", got.NewsletterID, tc.in.NewsletterID)
			}
			if got.UserHash != hashUserPrincipal("alice") || len(got.UserHash) != 64 {
				t.Errorf("user hash = %q, want the 64-char sha256 of the principal", got.UserHash)
			}
		})
	}
}

func TestPageViewService_PageViewCounts(t *testing.T) {
	viewedID, unviewedID, draftID := uuid.New(), uuid.New(), uuid.New()
	repo := &pageViewRepoFake{counts: map[uuid.UUID]int{viewedID: 5}}
	svc := NewPageViewService(&shareNewsletterFake{}, repo)
	counts, err := svc.PageViewCounts(context.Background(), []*model.Newsletter{
		{ID: viewedID, Status: model.StatusSent},
		{ID: unviewedID, Status: model.StatusSent},
		{ID: draftID, Status: model.StatusDraft},
	})
	if err != nil {
		t.Fatalf("PageViewCounts: %v", err)
	}
	if counts[viewedID] != 5 {
		t.Errorf("viewed count = %d, want 5", counts[viewedID])
	}
	if c, ok := counts[unviewedID]; !ok || c != 0 {
		t.Errorf("unviewed count = %d (present %v), want 0 and present", c, ok)
	}
	if _, ok := counts[draftID]; ok {
		t.Error("draft has a page-view count, want it absent")
	}
	if len(repo.gotIDs) != 2 {
		t.Errorf("queried %d ids, want only the 2 sent newsletters", len(repo.gotIDs))
	}

	repo.countsErr = errors.New("db down")
	if _, err := svc.PageViewCounts(context.Background(), []*model.Newsletter{{ID: viewedID, Status: model.StatusSent}}); err == nil {
		t.Error("PageViewCounts swallowed a repository error")
	}
}

func TestPageViewService_DeleteUserPageViews(t *testing.T) {
	repo := &pageViewRepoFake{}
	svc := NewPageViewService(&shareNewsletterFake{}, repo)

	n, err := svc.DeleteUserPageViews(context.Background(), " alice ")
	if err != nil || n != 3 {
		t.Fatalf("DeleteUserPageViews = %d, %v; want 3, nil", n, err)
	}
	if repo.deletedHash != hashUserPrincipal("alice") {
		t.Errorf("deleted hash = %q, want the same hash RecordPageView stores for the trimmed principal", repo.deletedHash)
	}
	if _, err := svc.DeleteUserPageViews(context.Background(), " "); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Errorf("blank principal: err = %v, want ErrInvalidRequest", err)
	}
}
