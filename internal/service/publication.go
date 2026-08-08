// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// PublicationService owns the business rules for newsletter publications:
// validation, wrapper_content normalization, and the update field-merge.
// Handlers stay transport-only and delegate here, matching the draft/newsletter
// layering. Editions are listed via the flat newsletter list filtered by
// publication_id, which lives in the newsletter service, not here.
type PublicationService struct {
	repo port.PublicationRepository
}

// NewPublicationService wires a PublicationService over the publication repository.
func NewPublicationService(repo port.PublicationRepository) *PublicationService {
	return &PublicationService{repo: repo}
}

// CreatePublicationInput is the typed input for CreatePublication.
type CreatePublicationInput struct {
	ProjectUID     string
	Slug           string
	Name           string
	WrapperContent any
	TemplateSetID  *string
	ViewOnlineBase *string
	CreatedBy      string
}

// UpdatePublicationInput is the typed input for UpdatePublication. Each pointer
// field is applied only when non-nil (a partial update); WrapperContent is
// applied only when non-nil for the same reason.
type UpdatePublicationInput struct {
	Name           *string
	WrapperContent any
	TemplateSetID  *string
	ViewOnlineBase *string
}

// marshalWrapperContent normalizes an optional wrapper_content value into the
// JSONB bytes the model persists. Nil yields the empty-object default.
func marshalWrapperContent(v any) ([]byte, error) {
	if v == nil {
		return []byte("{}"), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid wrapper_content", domain.ErrInvalidRequest)
	}
	return b, nil
}

// CreatePublication validates the input and inserts a new publication row.
func (s *PublicationService) CreatePublication(ctx context.Context, in CreatePublicationInput) (*model.NewsletterPublication, error) {
	if err := validateProjectUID(in.ProjectUID); err != nil {
		return nil, err
	}
	if in.Slug == "" {
		return nil, fmt.Errorf("%w: slug is required", domain.ErrInvalidRequest)
	}
	if in.Name == "" {
		return nil, fmt.Errorf("%w: name is required", domain.ErrInvalidRequest)
	}
	if in.CreatedBy == "" {
		return nil, fmt.Errorf("%w: createdBy is required", domain.ErrInvalidRequest)
	}

	wrapperContent, err := marshalWrapperContent(in.WrapperContent)
	if err != nil {
		return nil, err
	}

	pub := &model.NewsletterPublication{
		ProjectUID:     in.ProjectUID,
		Slug:           in.Slug,
		Name:           in.Name,
		WrapperContent: wrapperContent,
		TemplateSetID:  in.TemplateSetID,
		ViewOnlineBase: in.ViewOnlineBase,
		CreatedBy:      in.CreatedBy,
	}
	if err := s.repo.Create(ctx, pub); err != nil {
		return nil, err
	}
	return pub, nil
}

// GetPublication fetches a publication scoped to the project.
func (s *PublicationService) GetPublication(ctx context.Context, projectUID string, id uuid.UUID) (*model.NewsletterPublication, error) {
	if err := validateProjectUID(projectUID); err != nil {
		return nil, err
	}
	return s.repo.Get(ctx, projectUID, id)
}

// ListPublications returns the project's publications.
func (s *PublicationService) ListPublications(ctx context.Context, projectUID string) ([]*model.NewsletterPublication, error) {
	if err := validateProjectUID(projectUID); err != nil {
		return nil, err
	}
	return s.repo.List(ctx, projectUID)
}

// UpdatePublication applies a partial update under optimistic concurrency.
func (s *PublicationService) UpdatePublication(ctx context.Context, projectUID string, id uuid.UUID, expectedVersion int64, in UpdatePublicationInput) (*model.NewsletterPublication, error) {
	if err := validateProjectUID(projectUID); err != nil {
		return nil, err
	}

	pub, err := s.repo.Get(ctx, projectUID, id)
	if err != nil {
		return nil, err
	}

	if in.Name != nil {
		pub.Name = *in.Name
	}
	if in.TemplateSetID != nil {
		pub.TemplateSetID = in.TemplateSetID
	}
	if in.ViewOnlineBase != nil {
		pub.ViewOnlineBase = in.ViewOnlineBase
	}
	if in.WrapperContent != nil {
		wrapperContent, err := marshalWrapperContent(in.WrapperContent)
		if err != nil {
			return nil, err
		}
		pub.WrapperContent = wrapperContent
	}

	if err := s.repo.Update(ctx, pub, expectedVersion); err != nil {
		return nil, err
	}
	return pub, nil
}
