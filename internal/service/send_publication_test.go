// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

func publicationSendOrchestrator(repo *fakeNewsletterRepo, email *fakeEmailDispatcher, pubs *fakePublicationRepo) *SendOrchestrator {
	return NewSendOrchestrator(SendOrchestratorConfig{
		Repo:                  repo,
		Committee:             &fakeCommitteeClient{members: map[string][]model.CommitteeMember{"c1": {{Email: "alice@example.com"}}}},
		Project:               &fakeProjectClient{},
		Email:                 email,
		UserMetadata:          &fakeUserMetadataReader{name: "A Sender"},
		UserEmail:             &fakeUserEmailReader{email: "sender@example.com"},
		Unsubscribe:           NewUnsubscribeService(repo, []byte("k"), "https://api.example"),
		Publications:          pubs,
		Concurrency:           2,
		FanoutEnabled:         true,
		ReplyToAllowedDomains: []string{"example.com"},
	})
}

func sendToPublication(t *testing.T, pub *model.Publication, pubs *fakePublicationRepo) (*fakeEmailDispatcher, error) {
	t.Helper()
	repo := newFakeRepo()
	email := &fakeEmailDispatcher{}
	orch := publicationSendOrchestrator(repo, email, pubs)
	draft := repo.addDraft("p1", []string{"c1"})
	repo.mu.Lock()
	repo.drafts[draft.ID].PublicationUID = &pub.ID
	repo.mu.Unlock()
	_, err := orch.SendNewsletter(context.Background(), SendNewsletterInput{ProjectUID: "p1", NewsletterID: draft.ID, Principal: "auth0|sender"})
	orch.Drain(context.Background())
	return email, err
}

func TestSendUsesPublicationSender(t *testing.T) {
	pub := &model.Publication{ID: uuid.New(), ProjectUID: "p1", FromName: ptr("The Weekly"), ReplyToEmail: ptr("weekly@example.com")}
	email, err := sendToPublication(t, pub, newFakePublicationRepo(pub))
	if err != nil {
		t.Fatalf("SendNewsletter: %v", err)
	}
	if email.sends[0].FromDisplayName != "The Weekly" || email.sends[0].ReplyTo != "weekly@example.com" {
		t.Errorf("From=%q ReplyTo=%q", email.sends[0].FromDisplayName, email.sends[0].ReplyTo)
	}
}

func TestSendImplicitPublicationKeepsResolvedSender(t *testing.T) {
	pub := &model.Publication{ID: uuid.New(), ProjectUID: "p1", Implicit: true}
	email, err := sendToPublication(t, pub, newFakePublicationRepo(pub))
	if err != nil {
		t.Fatalf("SendNewsletter: %v", err)
	}
	if email.sends[0].FromDisplayName != "A Sender" || email.sends[0].ReplyTo != "sender@example.com" {
		t.Errorf("From=%q ReplyTo=%q; implicit publications must not change the sender", email.sends[0].FromDisplayName, email.sends[0].ReplyTo)
	}
}

func TestSendSkipsDisallowedPublicationReplyTo(t *testing.T) {
	pub := &model.Publication{ID: uuid.New(), ProjectUID: "p1", ReplyToEmail: ptr("weekly@not-allowed.test")}
	email, err := sendToPublication(t, pub, newFakePublicationRepo(pub))
	if err != nil {
		t.Fatalf("SendNewsletter: %v", err)
	}
	if email.sends[0].ReplyTo != "sender@example.com" {
		t.Errorf("ReplyTo=%q, want the sender-resolved address", email.sends[0].ReplyTo)
	}
}

func TestSendPublicationLookupFailureSendsNothing(t *testing.T) {
	pub := &model.Publication{ID: uuid.New(), ProjectUID: "p1"}
	pubs := newFakePublicationRepo(pub)
	pubs.err = errors.New("database down")
	email, err := sendToPublication(t, pub, pubs)
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(email.sends) != 0 {
		t.Fatalf("sent %d emails despite a failed publication lookup", len(email.sends))
	}
}
