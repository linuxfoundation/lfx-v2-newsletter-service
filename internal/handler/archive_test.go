// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// mockNewsletterRepo for handler tests
type handlerMockNewsletterRepo struct {
	newsletters map[uuid.UUID]*model.Newsletter
}

func (m *handlerMockNewsletterRepo) Get(ctx context.Context, id uuid.UUID) (*model.Newsletter, error) {
	n, ok := m.newsletters[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return n, nil
}

func (m *handlerMockNewsletterRepo) ListSentByCommitteeUIDs(ctx context.Context, filters port.ArchiveListFilters) (*port.ListPage, error) {
	if len(filters.CommitteeUIDs) == 0 {
		return &port.ListPage{Newsletters: []*model.Newsletter{}}, nil
	}
	var results []*model.Newsletter
	for _, n := range m.newsletters {
		if n.Status != model.StatusSent {
			continue
		}
		for _, filterUID := range filters.CommitteeUIDs {
			for _, nUID := range n.CommitteeUIDs {
				if nUID == filterUID {
					results = append(results, n)
					break
				}
			}
		}
	}
	return &port.ListPage{Newsletters: results}, nil
}

// Stub out unused methods.
func (m *handlerMockNewsletterRepo) Create(ctx context.Context, n *model.Newsletter) error {
	return nil
}
func (m *handlerMockNewsletterRepo) List(ctx context.Context, projectUID string) ([]*model.Newsletter, error) {
	return nil, nil
}
func (m *handlerMockNewsletterRepo) ListAll(ctx context.Context, filters port.ListFilters) (*port.ListPage, error) {
	return nil, nil
}
func (m *handlerMockNewsletterRepo) Update(ctx context.Context, n *model.Newsletter, expectedVersion int64) (*model.Newsletter, error) {
	return nil, nil
}
func (m *handlerMockNewsletterRepo) Delete(ctx context.Context, id uuid.UUID) error { return nil }
func (m *handlerMockNewsletterRepo) MarkSending(ctx context.Context, id uuid.UUID, groupID string, totalRecipients int, expectedVersion int64) (*model.Newsletter, error) {
	return nil, nil
}
func (m *handlerMockNewsletterRepo) MarkSent(ctx context.Context, id uuid.UUID, sentAt time.Time, expectedVersion int64) (*model.Newsletter, error) {
	return nil, nil
}
func (m *handlerMockNewsletterRepo) RevertSending(ctx context.Context, id uuid.UUID) error {
	return nil
}
func (m *handlerMockNewsletterRepo) RecoverStuckSending(ctx context.Context, olderThan time.Duration) (int64, error) {
	return 0, nil
}
func (m *handlerMockNewsletterRepo) RecordOpen(ctx context.Context, newsletterID uuid.UUID, recipientHash string) error {
	return nil
}
func (m *handlerMockNewsletterRepo) Analytics(ctx context.Context, newsletterID uuid.UUID) (*model.Analytics, error) {
	return nil, nil
}
func (m *handlerMockNewsletterRepo) CreateUnsubscribe(ctx context.Context, projectUID, email string) error {
	return nil
}
func (m *handlerMockNewsletterRepo) ListUnsubscribedEmails(ctx context.Context, projectUID string) (map[string]struct{}, error) {
	return nil, nil
}
func (m *handlerMockNewsletterRepo) ListUnsubscribes(ctx context.Context, projectUID string) ([]*model.NewsletterUnsubscribe, error) {
	return nil, nil
}
func (m *handlerMockNewsletterRepo) DeleteUnsubscribe(ctx context.Context, projectUID string, id uuid.UUID) error {
	return nil
}

// mockCommitteeClient for handler tests
type handlerMockCommitteeClient struct {
	members map[string][]model.CommitteeMember
	errors  map[string]error
}

func (m *handlerMockCommitteeClient) ListMembers(ctx context.Context, committeeUID string) ([]model.CommitteeMember, error) {
	if err, ok := m.errors[committeeUID]; ok {
		return nil, err
	}
	return m.members[committeeUID], nil
}

// mockUserEmailReader for handler tests
type handlerMockUserEmailReader struct {
	emails map[string]string
	err    error
}

func (m *handlerMockUserEmailReader) PrimaryEmail(ctx context.Context, principal string) (string, error) {
	emails, err := m.VerifiedEmails(ctx, principal)
	if err != nil {
		return "", err
	}
	if len(emails) == 0 {
		return "", nil
	}
	return emails[0], nil
}

func (m *handlerMockUserEmailReader) VerifiedEmails(ctx context.Context, principal string) ([]string, error) {
	if m.err != nil {
		return nil, m.err
	}
	email, ok := m.emails[principal]
	if !ok || email == "" {
		return []string{}, nil
	}
	return []string{email}, nil
}

// TestArchiveNewslettersHandler tests the HTTP handler for archive list.
func TestArchiveNewslettersHandler(t *testing.T) {
	sentAt := time.Now().UTC()
	newsletterID := uuid.New()
	newsletter := &model.Newsletter{
		ID:            newsletterID,
		ProjectUID:    "proj1",
		Subject:       "Test Newsletter",
		BodyHTML:      "<p>Content</p>",
		CommitteeUIDs: []string{"committee-1"},
		Status:        model.StatusSent,
		SentAt:        &sentAt,
	}

	repo := &handlerMockNewsletterRepo{
		newsletters: map[uuid.UUID]*model.Newsletter{
			newsletterID: newsletter,
		},
	}

	svc := service.NewArchiveService(service.ArchiveServiceConfig{
		Repo:      repo,
		Committee: &handlerMockCommitteeClient{members: map[string][]model.CommitteeMember{"committee-1": {{Email: "user1@example.com"}}}},
		UserEmail: &handlerMockUserEmailReader{emails: map[string]string{"user1": "user1@example.com"}},
	})

	h := &Handler{}
	h.SetArchiveService(svc)

	tests := []struct {
		name           string
		principal      string
		committeeUIDs  string
		expectStatus   int
		expectCount    int
		checkDTOFields bool
	}{
		{
			name:          "missing principal → 401",
			principal:     "",
			committeeUIDs: "committee-1",
			expectStatus:  http.StatusUnauthorized,
		},
		{
			name:           "valid request → 200 with list",
			principal:      "user1",
			committeeUIDs:  "committee-1",
			expectStatus:   http.StatusOK,
			expectCount:    1,
			checkDTOFields: true,
		},
		{
			name:          "non-member → 200 empty list",
			principal:     "user2",
			committeeUIDs: "committee-1",
			expectStatus:  http.StatusOK,
			expectCount:   0,
		},
		{
			name:      "too many committees → 400",
			principal: "user1",
			committeeUIDs: func() string {
				// Create 55 unique committee IDs to exceed the 50-limit
				var committees []string
				for i := 0; i < 55; i++ {
					committees = append(committees, fmt.Sprintf("committee-%d", i))
				}
				return strings.Join(committees, ",")
			}(),
			expectStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/newsletters/archive?committee_uids="+url.QueryEscape(tt.committeeUIDs), nil)
			if tt.principal != "" {
				req = req.WithContext(ContextWithUser(context.Background(), tt.principal))
			}
			w := httptest.NewRecorder()

			h.ArchiveNewsletters(w, req)

			if w.Code != tt.expectStatus {
				t.Errorf("expected status %d, got %d", tt.expectStatus, w.Code)
			}

			if tt.expectStatus == http.StatusOK {
				var resp publicapi.ArchiveNewsletterListResponse
				if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
					t.Fatalf("failed to decode response: %v", err)
				}
				if len(resp.Newsletters) != tt.expectCount {
					t.Errorf("expected %d newsletters, got %d", tt.expectCount, len(resp.Newsletters))
				}
				if tt.checkDTOFields && len(resp.Newsletters) > 0 {
					n := resp.Newsletters[0]
					if n.ID == "" {
						t.Error("newsletter ID is empty")
					}
					if n.SentAt.IsZero() {
						t.Error("newsletter SentAt is zero")
					}
				}
			}
		})
	}
}

// TestGetArchiveNewsletterHandler tests the HTTP handler for archive detail.
func TestGetArchiveNewsletterHandler(t *testing.T) {
	sentAt := time.Now().UTC()
	newsletterID := uuid.New()
	newsletter := &model.Newsletter{
		ID:            newsletterID,
		ProjectUID:    "proj1",
		Subject:       "Test Newsletter",
		BodyHTML:      "<p>Content</p>",
		CommitteeUIDs: []string{"committee-1"},
		Status:        model.StatusSent,
		SentAt:        &sentAt,
	}

	repo := &handlerMockNewsletterRepo{
		newsletters: map[uuid.UUID]*model.Newsletter{
			newsletterID: newsletter,
		},
	}

	svc := service.NewArchiveService(service.ArchiveServiceConfig{
		Repo:      repo,
		Committee: &handlerMockCommitteeClient{members: map[string][]model.CommitteeMember{"committee-1": {{Email: "user1@example.com"}}}},
		UserEmail: &handlerMockUserEmailReader{emails: map[string]string{"user1": "user1@example.com"}},
	})

	h := &Handler{}
	h.SetArchiveService(svc)

	tests := []struct {
		name         string
		principal    string
		newsletterID string
		expectStatus int
		checkBody    bool
	}{
		{
			name:         "missing principal → 401",
			principal:    "",
			newsletterID: newsletterID.String(),
			expectStatus: http.StatusUnauthorized,
		},
		{
			name:         "bad UUID → 400",
			principal:    "user1",
			newsletterID: "not-a-uuid",
			expectStatus: http.StatusBadRequest,
		},
		{
			name:         "newsletter not found → 404",
			principal:    "user1",
			newsletterID: uuid.New().String(),
			expectStatus: http.StatusNotFound,
		},
		{
			name:         "non-member → 403",
			principal:    "user2",
			newsletterID: newsletterID.String(),
			expectStatus: http.StatusForbidden,
		},
		{
			name:         "member → 200 with detail",
			principal:    "user1",
			newsletterID: newsletterID.String(),
			expectStatus: http.StatusOK,
			checkBody:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/newsletters/archive/"+tt.newsletterID, nil)
			// Simulate mux setting the path value.
			req.SetPathValue("newsletter_uid", tt.newsletterID)
			if tt.principal != "" {
				req = req.WithContext(ContextWithUser(context.Background(), tt.principal))
			}
			w := httptest.NewRecorder()

			h.GetArchiveNewsletter(w, req)

			if w.Code != tt.expectStatus {
				t.Errorf("expected status %d, got %d", tt.expectStatus, w.Code)
			}

			if tt.expectStatus == http.StatusOK && tt.checkBody {
				var resp publicapi.ArchiveNewsletter
				if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
					t.Fatalf("failed to decode response: %v", err)
				}
				if resp.ID == "" {
					t.Error("newsletter ID is empty")
				}
				if resp.BodyHTML == "" {
					t.Error("newsletter BodyHTML is empty")
				}
				if resp.SentAt.IsZero() {
					t.Error("newsletter SentAt is zero")
				}
			}
		})
	}
}

// TestSentAtNilHandling verifies that nil SentAt is handled defensively without panic.
func TestSentAtNilHandling(t *testing.T) {
	// Create a sent newsletter with nil SentAt (shouldn't happen in practice due to DB constraint,
	// but we handle it defensively).
	newsletterID := uuid.New()
	newsletter := &model.Newsletter{
		ID:            newsletterID,
		ProjectUID:    "proj1",
		Subject:       "Test Newsletter",
		BodyHTML:      "<p>Content</p>",
		CommitteeUIDs: []string{"committee-1"},
		Status:        model.StatusSent,
		SentAt:        nil, // nil despite status='sent'
	}

	repo := &handlerMockNewsletterRepo{
		newsletters: map[uuid.UUID]*model.Newsletter{
			newsletterID: newsletter,
		},
	}

	svc := service.NewArchiveService(service.ArchiveServiceConfig{
		Repo:      repo,
		Committee: &handlerMockCommitteeClient{members: map[string][]model.CommitteeMember{"committee-1": {{Email: "user1@example.com"}}}},
		UserEmail: &handlerMockUserEmailReader{emails: map[string]string{"user1": "user1@example.com"}},
	})

	h := &Handler{}
	h.SetArchiveService(svc)

	t.Run("list with nil SentAt", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/newsletters/archive?committee_uids=committee-1", nil).WithContext(ContextWithUser(context.Background(), "user1"))
		w := httptest.NewRecorder()

		h.ArchiveNewsletters(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}

		var resp publicapi.ArchiveNewsletterListResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if len(resp.Newsletters) != 1 {
			t.Errorf("expected 1 newsletter, got %d", len(resp.Newsletters))
		}
		// SentAt should be zero value, not cause a panic.
		if !resp.Newsletters[0].SentAt.IsZero() {
			t.Error("expected zero SentAt for nil input, got non-zero")
		}
	})

	t.Run("detail with nil SentAt", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/newsletters/archive/"+newsletterID.String(), nil).WithContext(ContextWithUser(context.Background(), "user1"))
		req.SetPathValue("newsletter_uid", newsletterID.String())
		w := httptest.NewRecorder()

		h.GetArchiveNewsletter(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}

		var resp publicapi.ArchiveNewsletter
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		// SentAt should be zero value, not cause a panic.
		if !resp.SentAt.IsZero() {
			t.Error("expected zero SentAt for nil input, got non-zero")
		}
	})
}
