// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// defaultMembershipConcurrency caps in-flight committee member lookups during
// verification. Mirrors send_orchestrator's pattern for concurrent I/O.
const defaultMembershipConcurrency = 5

// ArchiveService handles recipient-facing newsletter archive access,
// including membership verification to ensure callers only see newsletters
// sent to committees they belong to.
type ArchiveService struct {
	repo          port.NewsletterRepository
	committee     port.CommitteeClient
	userEmail     port.UserEmailReader
	concurrency   int
}

// ArchiveServiceConfig configures an ArchiveService.
type ArchiveServiceConfig struct {
	Repo       port.NewsletterRepository
	Committee  port.CommitteeClient
	UserEmail  port.UserEmailReader
	Concurrency int
}

// NewArchiveService wires an ArchiveService.
func NewArchiveService(cfg ArchiveServiceConfig) *ArchiveService {
	c := cfg.Concurrency
	if c <= 0 {
		c = defaultMembershipConcurrency
	}
	return &ArchiveService{
		repo:        cfg.Repo,
		committee:   cfg.Committee,
		userEmail:   cfg.UserEmail,
		concurrency: c,
	}
}

// VerifyMembershipsInput holds the parameters for VerifyMemberships.
type VerifyMembershipsInput struct {
	Principal     string   // The authenticated user's principal (from JWT)
	CommitteeUIDs []string // The claimed committee UIDs to verify membership in
}

// VerifyMembershipsOutput returns the verified committees the caller belongs to.
type VerifyMembershipsOutput struct {
	VerifiedUIDs []string // The subset of input committee UIDs where caller is a member
}

// VerifyMemberships verifies that the caller is a member of the claimed
// committees. For each claimed committee, it:
//
// 1. Fetches the member list from committee-service via NATS
// 2. Resolves the caller's primary email from auth-service
// 3. Matches the caller's email (and username if available) against the member list
// 4. Returns the verified subset of committees, dropping unreachable ones (logged as warnings)
//
// Empty input → empty output (no DB queries). Empty output → 200 with empty list.
func (s *ArchiveService) VerifyMemberships(ctx context.Context, in VerifyMembershipsInput) (*VerifyMembershipsOutput, error) {
	if len(in.CommitteeUIDs) == 0 {
		return &VerifyMembershipsOutput{VerifiedUIDs: []string{}}, nil
	}

	// Resolve the caller's email once; reuse for all committee membership checks.
	callerEmail, err := s.userEmail.PrimaryEmail(ctx, in.Principal)
	if err != nil {
		return nil, fmt.Errorf("resolve caller email: %w", err)
	}
	callerEmail = strings.ToLower(strings.TrimSpace(callerEmail))
	if callerEmail == "" {
		// No email means no membership anywhere. Return empty rather than error.
		slog.WarnContext(ctx, "caller email resolution returned empty", "principal", in.Principal)
		return &VerifyMembershipsOutput{VerifiedUIDs: []string{}}, nil
	}

	// Bounded concurrency: spawn workers to verify each committee in parallel.
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(s.concurrency)

	var mu sync.Mutex
	verified := make([]string, 0, len(in.CommitteeUIDs))

	for _, committeeUID := range in.CommitteeUIDs {
		committeeUID := committeeUID // Capture for the closure.
		eg.Go(func() error {
			if isMember, err := s.verifyCommitteeMembership(egCtx, committeeUID, callerEmail, in.Principal); err != nil {
				slog.WarnContext(egCtx, "committee membership verification failed",
					"committee_uid", committeeUID,
					"principal", in.Principal,
					"error", err)
				// Drop the committee; don't propagate the error.
				return nil
			} else if isMember {
				mu.Lock()
				verified = append(verified, committeeUID)
				mu.Unlock()
			}
			return nil
		})
	}
	_ = eg.Wait() // Bounded concurrency always succeeds; errors are logged and dropped.

	return &VerifyMembershipsOutput{VerifiedUIDs: verified}, nil
}

// verifyCommitteeMembership checks whether callerEmail is a member of the
// given committee. Returns true if found in the member list, false if not
// found, and an error if the lookup fails.
func (s *ArchiveService) verifyCommitteeMembership(ctx context.Context, committeeUID string, callerEmail string, principal string) (bool, error) {
	members, err := s.committee.ListMembers(ctx, committeeUID)
	if err != nil {
		return false, err
	}

	for _, member := range members {
		memberEmail := strings.ToLower(strings.TrimSpace(member.Email))
		if memberEmail == callerEmail {
			return true, nil
		}
		// Also match against username if present.
		memberUsername := strings.ToLower(strings.TrimSpace(member.Username))
		principalLower := strings.ToLower(strings.TrimSpace(principal))
		if memberUsername != "" && memberUsername == principalLower {
			return true, nil
		}
	}
	return false, nil
}

// ListArchiveInput holds the parameters for ListArchive.
type ListArchiveInput struct {
	Principal     string   // The authenticated user's principal (from JWT)
	CommitteeUIDs []string // The claimed committee UIDs (from query param)
	PageToken     string   // Pagination cursor
}

// ListArchiveOutput returns a page of sent newsletters accessible to the caller.
type ListArchiveOutput struct {
	Newsletters   []*model.Newsletter
	NextPageToken string
}

// ListArchive returns a page of sent newsletters the caller has access to.
// The caller is first verified against the claimed committees; unreachable/
// failed committees are dropped. An empty verified set yields a 200 with
// empty results (no DB query). Otherwise, the DB returns newsletters whose
// committee_uids overlap with the verified set.
func (s *ArchiveService) ListArchive(ctx context.Context, in ListArchiveInput) (*ListArchiveOutput, error) {
	// Verify membership; drop unreachable committees (logged as warnings).
	verify, err := s.VerifyMemberships(ctx, VerifyMembershipsInput{
		Principal:     in.Principal,
		CommitteeUIDs: in.CommitteeUIDs,
	})
	if err != nil {
		return nil, err
	}

	// Empty verified set → 200 with empty list (no DB query).
	if len(verify.VerifiedUIDs) == 0 {
		return &ListArchiveOutput{
			Newsletters:   []*model.Newsletter{},
			NextPageToken: "",
		}, nil
	}

	// Query sent newsletters with array overlap.
	page, err := s.repo.ListSentByCommitteeUIDs(ctx, port.ArchiveListFilters{
		CommitteeUIDs: verify.VerifiedUIDs,
		PageToken:     in.PageToken,
	})
	if err != nil {
		return nil, err
	}

	return &ListArchiveOutput{
		Newsletters:   page.Newsletters,
		NextPageToken: page.NextPageToken,
	}, nil
}

// GetArchiveInput holds the parameters for GetArchive.
type GetArchiveInput struct {
	Principal    string    // The authenticated user's principal (from JWT)
	NewsletterID uuid.UUID
}

// GetArchive returns a single sent newsletter iff the caller is a member of
// at least one of its committees. Returns domain.ErrNotFound if the newsletter
// doesn't exist or has status != 'sent' (prevents draft enumeration).
// Returns domain.ErrForbidden if the caller is not a member of any of its
// committees.
func (s *ArchiveService) GetArchive(ctx context.Context, in GetArchiveInput) (*model.Newsletter, error) {
	// Fetch the newsletter. 404 if missing or not sent.
	n, err := s.repo.Get(ctx, in.NewsletterID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	if n.Status != model.StatusSent {
		return nil, domain.ErrNotFound
	}

	// Verify membership in at least one of the newsletter's committees.
	verify, err := s.VerifyMemberships(ctx, VerifyMembershipsInput{
		Principal:     in.Principal,
		CommitteeUIDs: n.CommitteeUIDs,
	})
	if err != nil {
		return nil, err
	}

	if len(verify.VerifiedUIDs) == 0 {
		return nil, domain.ErrForbidden
	}

	return n, nil
}
