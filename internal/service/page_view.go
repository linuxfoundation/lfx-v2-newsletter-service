// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// PageViewService records unique web page views of sent newsletters and reads
// back view counts. A view is counted once per (newsletter, user), ever.
type PageViewService struct {
	newsletters port.NewsletterGetter
	views       port.PageViewRepository
}

// NewPageViewService wires a PageViewService.
func NewPageViewService(newsletters port.NewsletterGetter, views port.PageViewRepository) *PageViewService {
	return &PageViewService{newsletters: newsletters, views: views}
}

// RecordPageViewInput is the input to RecordPageView.
type RecordPageViewInput struct {
	ProjectUID   string
	NewsletterID uuid.UUID
	// User is the authenticated caller's principal, stored only as the same
	// SHA-256 digest shares use (a pseudonym, not an anonymization).
	User string
}

// RecordPageView records that the caller viewed a sent newsletter's web page.
// Repeat views by the same user are silent no-ops. Returns ErrInvalidRequest
// for a missing or anonymous user (every anonymous visitor shares one
// principal, so it can't identify a unique viewer), and ErrNotFound when the
// newsletter doesn't exist, belongs to another project, or hasn't been sent.
func (s *PageViewService) RecordPageView(ctx context.Context, in RecordPageViewInput) error {
	user := strings.TrimSpace(in.User)
	if user == "" || user == AnonymousPrincipal {
		return fmt.Errorf("%w: an authenticated user is required to record a page view", domain.ErrInvalidRequest)
	}
	if err := validateProjectUID(in.ProjectUID); err != nil {
		return err
	}
	n, err := s.newsletters.Get(ctx, in.NewsletterID)
	if err != nil {
		return err
	}
	if n.ProjectUID != in.ProjectUID || n.Status != model.StatusSent {
		return domain.ErrNotFound
	}
	return s.views.RecordPageView(ctx, &model.NewsletterPageView{
		NewsletterID: n.ID,
		UserHash:     hashShareUser(user),
	})
}

// PageViewCounts returns the unique page-view count for each sent newsletter
// in the list. Unsent newsletters are skipped (they have no readable page),
// and sent newsletters with no views map to 0.
func (s *PageViewService) PageViewCounts(ctx context.Context, newsletters []*model.Newsletter) (map[uuid.UUID]int, error) {
	ids := make([]uuid.UUID, 0, len(newsletters))
	for _, n := range newsletters {
		if n.Status == model.StatusSent {
			ids = append(ids, n.ID)
		}
	}
	counts, err := s.views.PageViewCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, ok := counts[id]; !ok {
			counts[id] = 0
		}
	}
	return counts, nil
}

// DeleteUserPageViews erases every page view recorded for a user, across all
// newsletters, given their principal (hashed the same way RecordPageView
// hashes it). It returns how many rows were removed.
func (s *PageViewService) DeleteUserPageViews(ctx context.Context, user string) (int64, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return 0, fmt.Errorf("%w: a user principal is required to erase page views", domain.ErrInvalidRequest)
	}
	return s.views.DeletePageViewsByUser(ctx, hashShareUser(user))
}
