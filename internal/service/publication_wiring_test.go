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

// fakePubRepo is a minimal port.PublicationRepository for the wiring test — only
// Get is exercised (validatePublicationID); the rest satisfy the interface.
type fakePubRepo struct {
	pubs map[uuid.UUID]*model.NewsletterPublication
}

func (f *fakePubRepo) Get(_ context.Context, projectUID string, id uuid.UUID) (*model.NewsletterPublication, error) {
	p, ok := f.pubs[id]
	if !ok || p.ProjectUID != projectUID {
		return nil, domain.ErrNotFound
	}
	return p, nil
}
func (f *fakePubRepo) Create(context.Context, *model.NewsletterPublication) error { return nil }
func (f *fakePubRepo) List(context.Context, string) ([]*model.NewsletterPublication, error) {
	return nil, nil
}
func (f *fakePubRepo) Update(context.Context, *model.NewsletterPublication, int64) error { return nil }
func (f *fakePubRepo) GetDefault(context.Context, string) (*model.NewsletterPublication, error) {
	return nil, domain.ErrNotFound
}

// TestCreateDraftPublicationIDWiring proves publication_id is persisted (not
// silently dropped) and validated against the same project.
func TestCreateDraftPublicationIDWiring(t *testing.T) {
	pubID := uuid.New()
	pubs := &fakePubRepo{pubs: map[uuid.UUID]*model.NewsletterPublication{
		pubID: {ID: pubID, ProjectUID: "proj-1"},
	}}
	svc := NewNewsletterService(newFakeRepo(), pubs)

	base := CreateDraftInput{
		ProjectUID:    "proj-1",
		Subject:       "Hi",
		BodyHTML:      "<p>x</p>",
		EDReplyEmail:  "ed@example.com",
		CommitteeUIDs: []string{"c1"},
		CreatedBy:     "u1",
	}

	t.Run("valid publication in same project is persisted", func(t *testing.T) {
		in := base
		in.PublicationID = &pubID
		draft, err := svc.CreateDraft(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if draft.PublicationID == nil || *draft.PublicationID != pubID {
			t.Fatalf("publication_id not persisted: %v", draft.PublicationID)
		}
	})

	t.Run("unknown publication is rejected, not dropped", func(t *testing.T) {
		other := uuid.New()
		in := base
		in.PublicationID = &other
		if _, err := svc.CreateDraft(context.Background(), in); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("want ErrInvalidRequest, got %v", err)
		}
	})

	t.Run("publication in a different project is rejected", func(t *testing.T) {
		in := base
		in.ProjectUID = "proj-2"
		in.PublicationID = &pubID
		if _, err := svc.CreateDraft(context.Background(), in); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("want ErrInvalidRequest, got %v", err)
		}
	})

	// Unfiled is a valid resting state: a project is not given a default
	// publication automatically, and server-initiated editions (the weekly
	// brief) have no publication to pick.
	t.Run("nil publication_id leaves the edition unfiled", func(t *testing.T) {
		draft, err := svc.CreateDraft(context.Background(), base)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if draft.PublicationID != nil {
			t.Fatalf("expected unfiled edition, got %v", draft.PublicationID)
		}
	})
}
