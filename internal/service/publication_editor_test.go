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
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// editorTypeRepo is a minimal PublicationRepository that records what Create
// and Update persisted, so the tests below assert on the stored value rather
// than on the input they just passed in.
type editorTypeRepo struct {
	stored *model.NewsletterPublication
}

func (r *editorTypeRepo) Create(_ context.Context, pub *model.NewsletterPublication) error {
	pub.ID = uuid.New()
	pub.Version = 1
	r.stored = pub
	return nil
}

func (r *editorTypeRepo) Get(context.Context, string, uuid.UUID) (*model.NewsletterPublication, error) {
	if r.stored == nil {
		return nil, domain.ErrNotFound
	}
	return r.stored, nil
}

func (r *editorTypeRepo) List(context.Context, port.PublicationListFilters) (*port.PublicationListPage, error) {
	return &port.PublicationListPage{}, nil
}

func (r *editorTypeRepo) Update(_ context.Context, pub *model.NewsletterPublication, _ int64) error {
	r.stored = pub
	return nil
}

func (r *editorTypeRepo) GetDefault(context.Context, string) (*model.NewsletterPublication, error) {
	return nil, domain.ErrNotFound
}

func strPtr(s string) *string { return &s }

// TestCreatePublicationEditorType covers the default and the validation
// boundary. The default matters for backward compatibility: a publication
// created by a caller that predates the block editor must open in Classic.
func TestCreatePublicationEditorType(t *testing.T) {
	base := CreatePublicationInput{
		ProjectUID: "project-1",
		Slug:       "pub",
		Name:       "Pub",
		CreatedBy:  "user",
	}

	t.Run("omitted defaults to classic", func(t *testing.T) {
		repo := &editorTypeRepo{}
		svc := NewPublicationService(repo)
		pub, err := svc.CreatePublication(context.Background(), base)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pub.EditorType != model.EditorTypeClassic {
			t.Errorf("editor type = %q, want %q", pub.EditorType, model.EditorTypeClassic)
		}
	})

	t.Run("blocks is persisted", func(t *testing.T) {
		repo := &editorTypeRepo{}
		svc := NewPublicationService(repo)
		in := base
		in.EditorType = strPtr(model.EditorTypeBlocks)
		in.SenderEmail = strPtr("ed@example.org")
		pub, err := svc.CreatePublication(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pub.EditorType != model.EditorTypeBlocks {
			t.Errorf("editor type = %q, want %q", pub.EditorType, model.EditorTypeBlocks)
		}
		if pub.SenderEmail == nil || *pub.SenderEmail != "ed@example.org" {
			t.Errorf("sender email not persisted: %v", pub.SenderEmail)
		}
	})

	// Rejected in the service rather than left to the schema CHECK constraint,
	// which would surface as a 500 instead of a 400.
	t.Run("unknown value is rejected", func(t *testing.T) {
		repo := &editorTypeRepo{}
		svc := NewPublicationService(repo)
		in := base
		in.EditorType = strPtr("puck")
		if _, err := svc.CreatePublication(context.Background(), in); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("want ErrInvalidRequest, got %v", err)
		}
	})
}

// TestUpdatePublicationEditorType proves the update path validates too, and
// that omitting the field leaves the stored value alone.
func TestUpdatePublicationEditorType(t *testing.T) {
	repo := &editorTypeRepo{}
	svc := NewPublicationService(repo)
	pub, err := svc.CreatePublication(context.Background(), CreatePublicationInput{
		ProjectUID: "project-1",
		Slug:       "pub",
		Name:       "Pub",
		CreatedBy:  "user",
		EditorType: strPtr(model.EditorTypeBlocks),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	t.Run("unknown value is rejected", func(t *testing.T) {
		_, err := svc.UpdatePublication(context.Background(), "project-1", pub.ID, 1, UpdatePublicationInput{
			EditorType: strPtr("nope"),
		})
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("want ErrInvalidRequest, got %v", err)
		}
	})

	t.Run("omitted preserves the current editor type", func(t *testing.T) {
		updated, err := svc.UpdatePublication(context.Background(), "project-1", pub.ID, 1, UpdatePublicationInput{
			Name: strPtr("Renamed"),
		})
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if updated.EditorType != model.EditorTypeBlocks {
			t.Errorf("editor type = %q, want it preserved as %q", updated.EditorType, model.EditorTypeBlocks)
		}
	})
}
