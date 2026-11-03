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

// AnonymousPrincipal is the principal Heimdall mints for an unauthenticated
// caller (the platform's anonymous_authenticator subject). It is one identity
// shared by every anonymous visitor, so it can never record a share or a
// page view.
const AnonymousPrincipal = "_anonymous"

// ShareService records in-app newsletter share intents and reads back share
// counts. A share is counted once per (newsletter, user, channel).
type ShareService struct {
	newsletters port.NewsletterGetter
	shares      port.ShareRepository
}

// NewShareService wires a ShareService.
func NewShareService(newsletters port.NewsletterGetter, shares port.ShareRepository) *ShareService {
	return &ShareService{newsletters: newsletters, shares: shares}
}

// RecordShareInput is the input to RecordShare.
type RecordShareInput struct {
	ProjectUID   string
	NewsletterID uuid.UUID
	// User is the authenticated caller's principal. It is stored only as a
	// SHA-256 digest, used to collapse repeat shares. That digest is a
	// pseudonym, not an anonymization: principals are guessable, so a digest
	// can be matched back to a known username.
	User    string
	Channel model.ShareChannel
}

// RecordShare records that the caller shared a sent newsletter to a channel.
// Repeats by the same user to the same channel are silent no-ops. Returns
// ErrInvalidRequest for an unknown channel or a missing or anonymous user,
// and ErrNotFound
// when the newsletter doesn't exist, belongs to another project, or hasn't
// been sent (drafts aren't shareable).
func (s *ShareService) RecordShare(ctx context.Context, in RecordShareInput) error {
	if !in.Channel.Valid() {
		return fmt.Errorf("%w: unknown share channel %q", domain.ErrInvalidRequest, in.Channel)
	}
	user := strings.TrimSpace(in.User)
	if user == "" || user == AnonymousPrincipal {
		return fmt.Errorf("%w: an authenticated user is required to record a share", domain.ErrInvalidRequest)
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
	return s.shares.RecordShare(ctx, &model.NewsletterShare{
		NewsletterID: n.ID,
		UserHash:     hashUserPrincipal(user),
		Channel:      in.Channel,
	})
}

// ShareCounts returns the share count for each sent newsletter in the list.
// Unsent newsletters are skipped (they can't be shared), and sent newsletters
// with no shares map to 0.
func (s *ShareService) ShareCounts(ctx context.Context, newsletters []*model.Newsletter) (map[uuid.UUID]int, error) {
	ids := make([]uuid.UUID, 0, len(newsletters))
	for _, n := range newsletters {
		if n.Status == model.StatusSent {
			ids = append(ids, n.ID)
		}
	}
	counts, err := s.shares.ShareCounts(ctx, ids)
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

// ShareStats returns one newsletter's share count in total and per channel.
// The caller is responsible for the project-ownership check.
func (s *ShareService) ShareStats(ctx context.Context, newsletterID uuid.UUID) (*model.ShareStats, error) {
	return s.shares.ShareStats(ctx, newsletterID)
}

// DeleteUserShares erases every share recorded for a user, across all
// newsletters, given their principal (hashed here the same way RecordShare
// hashes it). It is the erasure path for a user's share data; it returns how
// many rows were removed. Share counts drop accordingly.
func (s *ShareService) DeleteUserShares(ctx context.Context, user string) (int64, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return 0, fmt.Errorf("%w: a user principal is required to erase shares", domain.ErrInvalidRequest)
	}
	return s.shares.DeleteSharesByUser(ctx, hashUserPrincipal(user))
}
