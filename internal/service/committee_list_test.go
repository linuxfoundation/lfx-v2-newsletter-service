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
)

// addSent seeds the fake repo with an already-sent newsletter so committee
// list tests don't have to drive the full draft → send transition.
func (r *fakeNewsletterRepo) addSent(projectUID string, committeeUIDs []string, sentAt time.Time) *model.Newsletter {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := &model.Newsletter{
		ID:            uuid.New(),
		ProjectUID:    projectUID,
		Subject:       "Sent update",
		BodyHTML:      "<p>Body</p>",
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: committeeUIDs,
		Status:        model.StatusSent,
		SentAt:        &sentAt,
		Version:       1,
	}
	r.drafts[n.ID] = n
	cp := *n
	return &cp
}

func TestListCommitteeNewsletters_RequiresCommitteeUID(t *testing.T) {
	svc := NewNewsletterService(newFakeRepo(), true)

	for _, uid := range []string{"", "   "} {
		_, err := svc.ListCommitteeNewsletters(context.Background(), ListCommitteeNewslettersInput{CommitteeUID: uid})
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Errorf("committee_uid %q: got %v, want ErrInvalidRequest", uid, err)
		}
	}
}

func TestListCommitteeNewsletters_ReturnsOnlySentForCommittee(t *testing.T) {
	repo := newFakeRepo()
	svc := NewNewsletterService(repo, true)

	sentAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	want := repo.addSent("project-1", []string{"committee-a", "committee-b"}, sentAt)
	repo.addSent("project-1", []string{"committee-c"}, sentAt) // other committee
	repo.addDraft("project-1", []string{"committee-a"})        // draft — member-invisible
	repo.addSent("project-2", []string{"committee-a"}, sentAt) // other project, same committee — visible

	page, err := svc.ListCommitteeNewsletters(context.Background(), ListCommitteeNewslettersInput{CommitteeUID: "committee-a"})
	if err != nil {
		t.Fatalf("ListCommitteeNewsletters: %v", err)
	}
	if len(page.Newsletters) != 2 {
		t.Fatalf("got %d newsletters, want 2", len(page.Newsletters))
	}
	found := false
	for _, n := range page.Newsletters {
		if n.ID == want.ID {
			found = true
			if n.Status != model.StatusSent {
				t.Errorf("status: got %s, want sent", n.Status)
			}
			if n.SentAt == nil || !n.SentAt.Equal(sentAt) {
				t.Errorf("sent_at: got %v, want %v", n.SentAt, sentAt)
			}
		}
		if n.Status != model.StatusSent {
			t.Errorf("newsletter %s: got status %s, want only sent rows", n.ID, n.Status)
		}
	}
	if !found {
		t.Errorf("expected newsletter %s in the committee-a page", want.ID)
	}
}
