// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package service hosts the business logic for newsletter draft CRUD and send
// orchestration. The service layer depends only on the domain interfaces in
// internal/domain/port; concrete implementations are wired by cmd/newsletter-api.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

const (
	maxSubjectLength      = 200
	maxBodyHTMLLength     = 100_000
	maxCommitteesPerDraft = 50
)

// NewsletterService implements business logic for draft management.
type NewsletterService struct {
	repo port.NewsletterRepository
}

// NewNewsletterService wires a NewsletterService over the given repository.
func NewNewsletterService(repo port.NewsletterRepository) *NewsletterService {
	return &NewsletterService{repo: repo}
}

// CreateDraftInput is the typed input for CreateDraft.
type CreateDraftInput struct {
	ContextType   model.ContextType
	ContextUID    string
	Subject       string
	BodyHTML      string
	EDReplyEmail  string
	CommitteeUIDs []string
	CreatedBy     string
}

// UpdateDraftInput is the typed input for UpdateDraft.
type UpdateDraftInput struct {
	ID              uuid.UUID
	ExpectedVersion int64
	Subject         string
	BodyHTML        string
	EDReplyEmail    string
	CommitteeUIDs   []string
}

// CreateDraft validates the input and inserts a new draft row.
func (s *NewsletterService) CreateDraft(ctx context.Context, in CreateDraftInput) (*model.Newsletter, error) {
	if err := validateContextType(in.ContextType); err != nil {
		return nil, err
	}
	if in.ContextUID == "" {
		return nil, fmt.Errorf("%w: contextUid is required", domain.ErrInvalidRequest)
	}
	if err := validateSubject(in.Subject); err != nil {
		return nil, err
	}
	if err := validateBodyHTML(in.BodyHTML); err != nil {
		return nil, err
	}
	if err := validateEDReplyEmail(in.EDReplyEmail); err != nil {
		return nil, err
	}
	if err := validateCommitteeUIDs(in.CommitteeUIDs); err != nil {
		return nil, err
	}
	if in.CreatedBy == "" {
		return nil, fmt.Errorf("%w: createdBy is required", domain.ErrInvalidRequest)
	}

	n := &model.Newsletter{
		ContextType:   in.ContextType,
		ContextUID:    in.ContextUID,
		Subject:       strings.TrimSpace(in.Subject),
		BodyHTML:      in.BodyHTML,
		EDReplyEmail:  strings.TrimSpace(in.EDReplyEmail),
		CommitteeUIDs: dedupeStrings(in.CommitteeUIDs),
		Status:        model.StatusDraft,
		CreatedBy:     in.CreatedBy,
	}

	if err := s.repo.Create(ctx, n); err != nil {
		return nil, err
	}
	return n, nil
}

// GetDraft fetches a draft by id. Returns domain.ErrNotFound if not found.
func (s *NewsletterService) GetDraft(ctx context.Context, id uuid.UUID) (*model.Newsletter, error) {
	return s.repo.Get(ctx, id)
}

// ListDrafts returns all drafts for the given context, newest first.
func (s *NewsletterService) ListDrafts(ctx context.Context, contextType model.ContextType, contextUID string) ([]*model.Newsletter, error) {
	if err := validateContextType(contextType); err != nil {
		return nil, err
	}
	if contextUID == "" {
		return nil, fmt.Errorf("%w: contextUid is required", domain.ErrInvalidRequest)
	}
	return s.repo.List(ctx, contextType, contextUID)
}

// ListNewslettersInput is the typed input for ListNewsletters.
type ListNewslettersInput struct {
	ContextType model.ContextType
	ContextUID  string
	Status      model.Status // optional; "" means both drafts and sent
	PageToken   string
}

// ListNewsletters returns a page of newsletters for the given context. When
// Status is empty, both drafts and sent newsletters are returned, ordered by
// most-recently-updated first.
func (s *NewsletterService) ListNewsletters(ctx context.Context, in ListNewslettersInput) (*port.ListPage, error) {
	if err := validateContextType(in.ContextType); err != nil {
		return nil, err
	}
	if in.ContextUID == "" {
		return nil, fmt.Errorf("%w: contextUid is required", domain.ErrInvalidRequest)
	}
	if in.Status != "" {
		switch in.Status {
		case model.StatusDraft, model.StatusSent:
		default:
			return nil, fmt.Errorf("%w: status must be 'draft' or 'sent'", domain.ErrInvalidRequest)
		}
	}
	return s.repo.ListAll(ctx, port.ListFilters{
		ContextType: in.ContextType,
		ContextUID:  in.ContextUID,
		Status:      in.Status,
		PageToken:   in.PageToken,
	})
}

// Analytics returns aggregated engagement metrics for the given newsletter.
// Returns ErrNotFound if the newsletter doesn't exist.
func (s *NewsletterService) Analytics(ctx context.Context, id uuid.UUID) (*model.Analytics, error) {
	return s.repo.Analytics(ctx, id)
}

// RecordOpenWithHash records a single open event using an already-hashed
// recipient token (e.g. the hash carried in a tracking-pixel URL).
//
// Returns ErrNotFound only if the newsletter doesn't exist. An empty hash is
// treated as a silent no-op so a tracking pixel that lost its query string
// (e.g. via an email forward) still 200s with the GIF body instead of erroring.
func (s *NewsletterService) RecordOpenWithHash(ctx context.Context, newsletterID uuid.UUID, recipientHash string) error {
	// Verify the newsletter exists so we can return ErrNotFound for genuinely
	// bad IDs; we deliberately don't enforce status='sent' so test/preview
	// pipelines can light up the same tracking.
	if _, err := s.repo.Get(ctx, newsletterID); err != nil {
		return err
	}
	hash := strings.TrimSpace(recipientHash)
	if hash == "" {
		return nil
	}
	return s.repo.RecordOpen(ctx, newsletterID, hash)
}

// HashRecipient lowercases and SHA-256-hashes an email address. Exposed so
// other layers (e.g. handler) can emit the same token-shape when constructing
// tracking pixel URLs.
func HashRecipient(email string) string {
	clean := strings.ToLower(strings.TrimSpace(email))
	if clean == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(clean))
	return hex.EncodeToString(sum[:])
}

// UpdateDraft mutates an existing draft, gated by optimistic locking.
func (s *NewsletterService) UpdateDraft(ctx context.Context, in UpdateDraftInput) (*model.Newsletter, error) {
	if err := validateSubject(in.Subject); err != nil {
		return nil, err
	}
	if err := validateBodyHTML(in.BodyHTML); err != nil {
		return nil, err
	}
	if err := validateEDReplyEmail(in.EDReplyEmail); err != nil {
		return nil, err
	}
	if err := validateCommitteeUIDs(in.CommitteeUIDs); err != nil {
		return nil, err
	}

	existing, err := s.repo.Get(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if existing.Status == model.StatusSent {
		return nil, domain.ErrAlreadySent
	}

	existing.Subject = strings.TrimSpace(in.Subject)
	existing.BodyHTML = in.BodyHTML
	existing.EDReplyEmail = strings.TrimSpace(in.EDReplyEmail)
	existing.CommitteeUIDs = dedupeStrings(in.CommitteeUIDs)

	return s.repo.Update(ctx, existing, in.ExpectedVersion)
}

// DeleteDraft removes a draft by id. Returns domain.ErrNotFound if missing.
// Drafts that are already sent cannot be deleted.
func (s *NewsletterService) DeleteDraft(ctx context.Context, id uuid.UUID) error {
	existing, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if existing.Status == model.StatusSent {
		return domain.ErrAlreadySent
	}
	return s.repo.Delete(ctx, id)
}

// validateContextType ensures the context type is a recognized value.
func validateContextType(t model.ContextType) error {
	switch t {
	case model.ContextFoundation, model.ContextProject:
		return nil
	default:
		return fmt.Errorf("%w: contextType must be 'foundation' or 'project'", domain.ErrInvalidRequest)
	}
}

func validateSubject(subject string) error {
	trimmed := strings.TrimSpace(subject)
	if trimmed == "" {
		return fmt.Errorf("%w: subject is required", domain.ErrInvalidRequest)
	}
	if len(trimmed) > maxSubjectLength {
		return fmt.Errorf("%w: subject exceeds %d characters", domain.ErrInvalidRequest, maxSubjectLength)
	}
	return nil
}

func validateBodyHTML(bodyHTML string) error {
	if strings.TrimSpace(bodyHTML) == "" {
		return fmt.Errorf("%w: bodyHtml is required", domain.ErrInvalidRequest)
	}
	if len(bodyHTML) > maxBodyHTMLLength {
		return fmt.Errorf("%w: bodyHtml exceeds %d characters", domain.ErrInvalidRequest, maxBodyHTMLLength)
	}
	return nil
}

func validateEDReplyEmail(email string) error {
	trimmed := strings.TrimSpace(email)
	if trimmed == "" {
		return fmt.Errorf("%w: edReplyEmail is required", domain.ErrInvalidRequest)
	}
	if _, err := mail.ParseAddress(trimmed); err != nil {
		return fmt.Errorf("%w: edReplyEmail is not a valid email: %v", domain.ErrInvalidRequest, err)
	}
	return nil
}

func validateCommitteeUIDs(uids []string) error {
	if len(uids) == 0 {
		return fmt.Errorf("%w: at least one committeeUid is required", domain.ErrInvalidRequest)
	}
	if len(uids) > maxCommitteesPerDraft {
		return fmt.Errorf("%w: at most %d committeeUids allowed", domain.ErrInvalidRequest, maxCommitteesPerDraft)
	}
	for _, u := range uids {
		if strings.TrimSpace(u) == "" {
			return fmt.Errorf("%w: committeeUids contains an empty value", domain.ErrInvalidRequest)
		}
	}
	return nil
}

// dedupeStrings returns the input slice with duplicates removed, preserving order.
func dedupeStrings(in []string) []string {
	if len(in) <= 1 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// IsValidationError reports whether err is a validation/domain ErrInvalidRequest.
func IsValidationError(err error) bool {
	return errors.Is(err, domain.ErrInvalidRequest)
}
