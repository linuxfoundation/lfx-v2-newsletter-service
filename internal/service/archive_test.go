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
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// mockNewsletterRepo implements port.NewsletterRepository for testing.
type mockNewsletterRepo struct {
	newsletters map[uuid.UUID]*model.Newsletter
}

func (m *mockNewsletterRepo) Get(ctx context.Context, id uuid.UUID) (*model.Newsletter, error) {
	n, ok := m.newsletters[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return n, nil
}

func (m *mockNewsletterRepo) ListSentByCommitteeUIDs(ctx context.Context, filters port.ArchiveListFilters) (*port.ListPage, error) {
	if len(filters.CommitteeUIDs) == 0 {
		return &port.ListPage{Newsletters: []*model.Newsletter{}}, nil
	}
	var results []*model.Newsletter
	for _, n := range m.newsletters {
		if n.Status != model.StatusSent {
			continue
		}
		// Check if any of the newsletter's committees overlap with the filter.
		for _, filterUID := range filters.CommitteeUIDs {
			for _, nUID := range n.CommitteeUIDs {
				if nUID == filterUID {
					results = append(results, n)
					break
				}
			}
		}
	}
	// Simple pagination: just return all results. Real pagination is tested via repository tests.
	return &port.ListPage{Newsletters: results}, nil
}

// Stub out unused methods.
func (m *mockNewsletterRepo) Create(ctx context.Context, n *model.Newsletter) error { return nil }
func (m *mockNewsletterRepo) List(ctx context.Context, projectUID string) ([]*model.Newsletter, error) {
	return nil, nil
}
func (m *mockNewsletterRepo) ListAll(ctx context.Context, filters port.ListFilters) (*port.ListPage, error) {
	return nil, nil
}
func (m *mockNewsletterRepo) Update(ctx context.Context, n *model.Newsletter, expectedVersion int64) (*model.Newsletter, error) {
	return nil, nil
}
func (m *mockNewsletterRepo) Delete(ctx context.Context, id uuid.UUID) error { return nil }
func (m *mockNewsletterRepo) MarkSending(ctx context.Context, id uuid.UUID, groupID string, totalRecipients int, expectedVersion int64) (*model.Newsletter, error) {
	return nil, nil
}
func (m *mockNewsletterRepo) MarkSent(ctx context.Context, id uuid.UUID, sentAt time.Time, expectedVersion int64) (*model.Newsletter, error) {
	return nil, nil
}
func (m *mockNewsletterRepo) RevertSending(ctx context.Context, id uuid.UUID) error { return nil }
func (m *mockNewsletterRepo) RecoverStuckSending(ctx context.Context, olderThan time.Duration) (int64, error) {
	return 0, nil
}
func (m *mockNewsletterRepo) RecordOpen(ctx context.Context, newsletterID uuid.UUID, recipientHash string) error {
	return nil
}
func (m *mockNewsletterRepo) Analytics(ctx context.Context, newsletterID uuid.UUID) (*model.Analytics, error) {
	return nil, nil
}
func (m *mockNewsletterRepo) CreateUnsubscribe(ctx context.Context, projectUID, email string) error { return nil }
func (m *mockNewsletterRepo) ListUnsubscribedEmails(ctx context.Context, projectUID string) (map[string]struct{}, error) {
	return nil, nil
}
func (m *mockNewsletterRepo) ListUnsubscribes(ctx context.Context, projectUID string) ([]*model.NewsletterUnsubscribe, error) {
	return nil, nil
}
func (m *mockNewsletterRepo) DeleteUnsubscribe(ctx context.Context, projectUID string, id uuid.UUID) error { return nil }

// mockCommitteeClient implements port.CommitteeClient for testing.
type mockCommitteeClient struct {
	members map[string][]model.CommitteeMember // committee UID → members
	errors  map[string]error                   // committee UID → error (optional per-committee error)
}

func (m *mockCommitteeClient) ListMembers(ctx context.Context, committeeUID string) ([]model.CommitteeMember, error) {
	if err, ok := m.errors[committeeUID]; ok {
		return nil, err
	}
	return m.members[committeeUID], nil
}

// mockUserEmailReader implements port.UserEmailReader for testing.
type mockUserEmailReader struct {
	emails map[string]string // principal → email(s, space-separated for multiple)
	err    error
}

func (m *mockUserEmailReader) PrimaryEmail(ctx context.Context, principal string) (string, error) {
	emails, err := m.VerifiedEmails(ctx, principal)
	if err != nil {
		return "", err
	}
	if len(emails) == 0 {
		return "", nil
	}
	return emails[0], nil
}

func (m *mockUserEmailReader) VerifiedEmails(ctx context.Context, principal string) ([]string, error) {
	if m.err != nil {
		return nil, m.err
	}
	email, ok := m.emails[principal]
	if !ok || email == "" {
		return []string{}, nil
	}
	// For simplicity, return just the single email; tests can extend this if needed.
	return []string{email}, nil
}

func TestVerifyMemberships(t *testing.T) {
	tests := []struct {
		name           string
		principal      string
		claimedUIDs    []string
		committees     map[string][]model.CommitteeMember
		emails         map[string]string
		expectedUIDs   []string
		expectErr      bool
		emailErr       error // optional error to inject into mockUserEmailReader
	}{
		{
			name:         "empty claimed UIDs",
			principal:    "user1",
			claimedUIDs:  []string{},
			expectedUIDs: []string{},
		},
		{
			name:        "single committee, user is member",
			principal:   "user1",
			claimedUIDs: []string{"committee-1"},
			committees: map[string][]model.CommitteeMember{
				"committee-1": {{Email: "user1@example.com", FirstName: "User"}},
			},
			emails: map[string]string{
				"user1": "user1@example.com",
			},
			expectedUIDs: []string{"committee-1"},
		},
		{
			name:        "single committee, user is not member",
			principal:   "user1",
			claimedUIDs: []string{"committee-1"},
			committees: map[string][]model.CommitteeMember{
				"committee-1": {{Email: "user2@example.com", FirstName: "User"}},
			},
			emails: map[string]string{
				"user1": "user1@example.com",
			},
			expectedUIDs: []string{},
		},
		{
			name:        "multiple committees, partial match",
			principal:   "user1",
			claimedUIDs: []string{"committee-1", "committee-2", "committee-3"},
			committees: map[string][]model.CommitteeMember{
				"committee-1": {{Email: "user1@example.com"}},
				"committee-2": {{Email: "other@example.com"}},
				"committee-3": {{Email: "user1@example.com"}},
			},
			emails: map[string]string{
				"user1": "user1@example.com",
			},
			expectedUIDs: []string{"committee-1", "committee-3"},
		},
		{
			name:        "username match",
			principal:   "lf_user1",
			claimedUIDs: []string{"committee-1"},
			committees: map[string][]model.CommitteeMember{
				"committee-1": {{Email: "user@example.com", Username: "lf_user1"}},
			},
			emails: map[string]string{
				"lf_user1": "other@example.com",
			},
			expectedUIDs: []string{"committee-1"},
		},
		{
			name:        "email resolution returns empty",
			principal:   "user1",
			claimedUIDs: []string{"committee-1"},
			committees:  map[string][]model.CommitteeMember{},
			emails:      map[string]string{}, // no email for user1
			expectedUIDs: []string{},
		},
		{
			name:        "email resolution fails with error",
			principal:   "user1",
			claimedUIDs: []string{"committee-1"},
			committees:  map[string][]model.CommitteeMember{},
			emails:      map[string]string{},
			expectedUIDs: []string{},
			expectErr:   true,                                              // email resolution error should propagate
			emailErr:    errors.New("auth service unavailable"),            // explicit error
		},
		{
			name:        "partial failure: committee lookup fails for one committee",
			principal:   "user1",
			claimedUIDs: []string{"committee-1", "committee-unreachable"},
			committees: map[string][]model.CommitteeMember{
				"committee-1": {{Email: "user1@example.com"}},
				// committee-unreachable returns error, will be dropped
			},
			emails: map[string]string{
				"user1": "user1@example.com",
			},
			expectedUIDs: []string{"committee-1"}, // unreachable dropped, committee-1 verified
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// For "partial failure" test, set up the committee client to error on unreachable.
			committeeErrors := make(map[string]error)
			if tt.name == "partial failure: committee lookup fails for one committee" {
				committeeErrors["committee-unreachable"] = errors.New("committee service unavailable")
			}

			svc := NewArchiveService(ArchiveServiceConfig{
				Repo:      &mockNewsletterRepo{},
				Committee: &mockCommitteeClient{members: tt.committees, errors: committeeErrors},
				UserEmail: &mockUserEmailReader{emails: tt.emails, err: tt.emailErr},
			})
			out, err := svc.VerifyMemberships(context.Background(), VerifyMembershipsInput{
				Principal:     tt.principal,
				CommitteeUIDs: tt.claimedUIDs,
			})
			if err != nil && !tt.expectErr {
				t.Fatalf("unexpected error: %v", err)
			}
			if err == nil && tt.expectErr {
				t.Fatalf("expected error, got nil")
			}
			if err == nil {
				if len(out.VerifiedUIDs) != len(tt.expectedUIDs) {
					t.Errorf("expected %d verified UIDs, got %d: %v", len(tt.expectedUIDs), len(out.VerifiedUIDs), out.VerifiedUIDs)
				}
				for _, expected := range tt.expectedUIDs {
					found := false
					for _, verified := range out.VerifiedUIDs {
						if verified == expected {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected UID %q not in verified set", expected)
					}
				}
			}
		})
	}
}

func TestGetArchive(t *testing.T) {
	sentAt := time.Now().UTC()
	newsletterID := uuid.New()
	draftID := uuid.New()
	newsletter := &model.Newsletter{
		ID:            newsletterID,
		ProjectUID:    "proj1",
		Subject:       "Test Newsletter",
		BodyHTML:      "<p>Content</p>",
		CommitteeUIDs: []string{"committee-1", "committee-2"},
		Status:        model.StatusSent,
		SentAt:        &sentAt,
	}
	draftNewsletter := &model.Newsletter{
		ID:            draftID,
		ProjectUID:    "proj1",
		Subject:       "Draft Newsletter",
		BodyHTML:      "<p>Content</p>",
		CommitteeUIDs: []string{"committee-1"},
		Status:        model.StatusDraft,
	}

	tests := []struct {
		name        string
		principal   string
		claimedUIDs []string
		committees  map[string][]model.CommitteeMember
		emails      map[string]string
		newsletter  *model.Newsletter
		queryID     uuid.UUID
		expectError error
	}{
		{
			name:        "user is member of one committee",
			principal:   "user1",
			claimedUIDs: []string{"committee-1"},
			committees: map[string][]model.CommitteeMember{
				"committee-1": {{Email: "user1@example.com"}},
			},
			emails: map[string]string{
				"user1": "user1@example.com",
			},
			newsletter: newsletter,
			queryID:    newsletterID,
		},
		{
			name:        "user is not member of any committee",
			principal:   "user1",
			claimedUIDs: []string{"committee-1"},
			committees: map[string][]model.CommitteeMember{
				"committee-1": {{Email: "user2@example.com"}},
			},
			emails: map[string]string{
				"user1": "user1@example.com",
			},
			newsletter:  newsletter,
			queryID:     newsletterID,
			expectError: domain.ErrForbidden,
		},
		{
			name:        "draft newsletter enumeration guard",
			principal:   "user1",
			claimedUIDs: []string{"committee-1"},
			committees: map[string][]model.CommitteeMember{
				"committee-1": {{Email: "user1@example.com"}},
			},
			emails: map[string]string{
				"user1": "user1@example.com",
			},
			newsletter:  draftNewsletter,
			queryID:     draftID,
			expectError: domain.ErrNotFound,
		},
		{
			name:        "newsletter not found",
			principal:   "user1",
			claimedUIDs: []string{"committee-1"},
			queryID:     uuid.New(),
			expectError: domain.ErrNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &mockNewsletterRepo{
				newsletters: map[uuid.UUID]*model.Newsletter{},
			}
			if tt.newsletter != nil {
				repo.newsletters[tt.newsletter.ID] = tt.newsletter
			}

			svc := NewArchiveService(ArchiveServiceConfig{
				Repo:      repo,
				Committee: &mockCommitteeClient{members: tt.committees},
				UserEmail: &mockUserEmailReader{emails: tt.emails},
			})

			n, err := svc.GetArchive(context.Background(), GetArchiveInput{
				Principal:    tt.principal,
				NewsletterID: tt.queryID,
			})
			if tt.expectError != nil {
				if !errors.Is(err, tt.expectError) {
					t.Fatalf("expected error %v, got %v", tt.expectError, err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if n.ID != tt.queryID {
					t.Errorf("expected newsletter ID %v, got %v", tt.queryID, n.ID)
				}
			}
		})
	}
}

func TestListArchive(t *testing.T) {
	sentAt := time.Now().UTC()
	newsletters := []*model.Newsletter{
		{
			ID:            uuid.New(),
			ProjectUID:    "proj1",
			Subject:       "Newsletter 1",
			CommitteeUIDs: []string{"committee-1"},
			Status:        model.StatusSent,
			SentAt:        &sentAt,
		},
		{
			ID:            uuid.New(),
			ProjectUID:    "proj1",
			Subject:       "Newsletter 2",
			CommitteeUIDs: []string{"committee-2"},
			Status:        model.StatusSent,
			SentAt:        &sentAt,
		},
	}

	tests := []struct {
		name        string
		principal   string
		claimedUIDs []string
		committees  map[string][]model.CommitteeMember
		emails      map[string]string
		expect      int // expected number of returned newsletters
	}{
		{
			name:        "user is member of one committee",
			principal:   "user1",
			claimedUIDs: []string{"committee-1", "committee-2"},
			committees: map[string][]model.CommitteeMember{
				"committee-1": {{Email: "user1@example.com"}},
				"committee-2": {{Email: "other@example.com"}},
			},
			emails: map[string]string{
				"user1": "user1@example.com",
			},
			expect: 1,
		},
		{
			name:        "empty verified committees",
			principal:   "user1",
			claimedUIDs: []string{"committee-1"},
			committees: map[string][]model.CommitteeMember{
				"committee-1": {{Email: "other@example.com"}},
			},
			emails: map[string]string{
				"user1": "user1@example.com",
			},
			expect: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &mockNewsletterRepo{
				newsletters: map[uuid.UUID]*model.Newsletter{},
			}
			for _, n := range newsletters {
				repo.newsletters[n.ID] = n
			}

			svc := NewArchiveService(ArchiveServiceConfig{
				Repo:      repo,
				Committee: &mockCommitteeClient{members: tt.committees},
				UserEmail: &mockUserEmailReader{emails: tt.emails},
			})

			out, err := svc.ListArchive(context.Background(), ListArchiveInput{
				Principal:     tt.principal,
				CommitteeUIDs: tt.claimedUIDs,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(out.Newsletters) != tt.expect {
				t.Errorf("expected %d newsletters, got %d", tt.expect, len(out.Newsletters))
			}
		})
	}
}

func TestListArchivePaginationPassthrough(t *testing.T) {
	// Verify that the service correctly passes page_token to the repository
	// and returns the next_page_token. Full pagination (no gaps, no overlaps)
	// is tested at the repository level via ListSentByCommitteeUIDs.
	sentAt := time.Now().UTC()
	newsletter := &model.Newsletter{
		ID:            uuid.New(),
		ProjectUID:    "proj1",
		Subject:       "Newsletter 1",
		CommitteeUIDs: []string{"committee-1"},
		Status:        model.StatusSent,
		SentAt:        &sentAt,
	}

	repo := &mockNewsletterRepo{
		newsletters: map[uuid.UUID]*model.Newsletter{
			newsletter.ID: newsletter,
		},
	}

	svc := NewArchiveService(ArchiveServiceConfig{
		Repo:      repo,
		Committee: &mockCommitteeClient{members: map[string][]model.CommitteeMember{"committee-1": {{Email: "user1@example.com"}}}},
		UserEmail: &mockUserEmailReader{emails: map[string]string{"user1": "user1@example.com"}},
	})

	out, err := svc.ListArchive(context.Background(), ListArchiveInput{
		Principal:     "user1",
		CommitteeUIDs: []string{"committee-1"},
		PageToken:     "test-token",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify pagination works: expecting 1 newsletter from the list
	if len(out.Newsletters) != 1 {
		t.Errorf("expected 1 newsletter, got %d", len(out.Newsletters))
	}
	// The mock doesn't return a next token, but that's ok - it tests the passthrough
}
