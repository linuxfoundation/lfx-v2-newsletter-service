// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// MockPublicationRepository is a simple mock for testing handlers.
type MockPublicationRepository struct {
	publications map[uuid.UUID]*model.NewsletterPublication
	defaults     map[string]*model.NewsletterPublication
}

func NewMockPublicationRepository() *MockPublicationRepository {
	return &MockPublicationRepository{
		publications: make(map[uuid.UUID]*model.NewsletterPublication),
		defaults:     make(map[string]*model.NewsletterPublication),
	}
}

func (m *MockPublicationRepository) Create(ctx context.Context, pub *model.NewsletterPublication) error {
	if pub.ID == uuid.Nil {
		pub.ID = uuid.New()
	}
	if pub.Version == 0 {
		pub.Version = 1
	}
	if pub.CreatedAt.IsZero() {
		pub.CreatedAt = time.Now()
	}
	if pub.UpdatedAt.IsZero() {
		pub.UpdatedAt = time.Now()
	}
	m.publications[pub.ID] = pub
	if pub.IsDefault {
		m.defaults[pub.ProjectUID] = pub
	}
	return nil
}

func (m *MockPublicationRepository) Get(ctx context.Context, projectUID string, id uuid.UUID) (*model.NewsletterPublication, error) {
	pub, ok := m.publications[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if pub.ProjectUID != projectUID {
		return nil, domain.ErrNotFound
	}
	return pub, nil
}

func (m *MockPublicationRepository) List(ctx context.Context, projectUID string) ([]*model.NewsletterPublication, error) {
	var result []*model.NewsletterPublication
	for _, pub := range m.publications {
		if pub.ProjectUID == projectUID {
			result = append(result, pub)
		}
	}
	return result, nil
}

func (m *MockPublicationRepository) Update(ctx context.Context, pub *model.NewsletterPublication, expectedVersion int64) error {
	existing, ok := m.publications[pub.ID]
	if !ok {
		return domain.ErrNotFound
	}
	if existing.Version != expectedVersion {
		return domain.ErrVersionMismatch
	}
	pub.Version = expectedVersion + 1
	pub.UpdatedAt = time.Now()
	m.publications[pub.ID] = pub
	return nil
}

func (m *MockPublicationRepository) GetDefault(ctx context.Context, projectUID string) (*model.NewsletterPublication, error) {
	pub, ok := m.defaults[projectUID]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return pub, nil
}

// MockNewsletterRepository is a simple mock for testing handlers.
type MockNewsletterRepository struct {
	newsletters map[uuid.UUID]*model.Newsletter
}

func NewMockNewsletterRepository() *MockNewsletterRepository {
	return &MockNewsletterRepository{
		newsletters: make(map[uuid.UUID]*model.Newsletter),
	}
}

func (m *MockNewsletterRepository) ListAll(ctx context.Context, filters port.ListFilters) (*port.ListPage, error) {
	var result []*model.Newsletter
	for _, n := range m.newsletters {
		if n.ProjectUID == filters.ProjectUID {
			if filters.PublicationID != nil {
				if n.PublicationID == nil || *n.PublicationID != *filters.PublicationID {
					continue
				}
			}
			result = append(result, n)
		}
	}
	return &port.ListPage{Newsletters: result}, nil
}

func (m *MockNewsletterRepository) Create(ctx context.Context, n *model.Newsletter) error {
	if n.ID == uuid.Nil {
		n.ID = uuid.New()
	}
	if n.Version == 0 {
		n.Version = 1
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now()
	}
	if n.UpdatedAt.IsZero() {
		n.UpdatedAt = time.Now()
	}
	m.newsletters[n.ID] = n
	return nil
}

func (m *MockNewsletterRepository) Get(ctx context.Context, id uuid.UUID) (*model.Newsletter, error) {
	return nil, nil
}

func (m *MockNewsletterRepository) List(ctx context.Context, projectUID string) ([]*model.Newsletter, error) {
	return nil, nil
}

func (m *MockNewsletterRepository) ListSentByCommittee(ctx context.Context, committeeUID string, pageToken string) (*port.ListPage, error) {
	return nil, nil
}

func (m *MockNewsletterRepository) Update(ctx context.Context, n *model.Newsletter, expectedVersion int64) (*model.Newsletter, error) {
	return nil, nil
}

func (m *MockNewsletterRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return nil
}

func (m *MockNewsletterRepository) MarkSending(ctx context.Context, id uuid.UUID, groupID, sendProvider string, totalRecipients int, expectedVersion int64, scheduledAt *time.Time, batchID string) (*model.Newsletter, error) {
	return nil, nil
}

func (m *MockNewsletterRepository) MarkScheduled(ctx context.Context, id uuid.UUID, expectedVersion int64) (*model.Newsletter, error) {
	return nil, nil
}

func (m *MockNewsletterRepository) RevertScheduled(ctx context.Context, id uuid.UUID, expectedVersion int64, batchID *string) (*model.Newsletter, error) {
	return nil, nil
}

func (m *MockNewsletterRepository) SettleDueScheduled(ctx context.Context, now time.Time) (int64, error) {
	return 0, nil
}

func (m *MockNewsletterRepository) MarkSent(ctx context.Context, id uuid.UUID, sentAt time.Time, expectedVersion int64) (*model.Newsletter, error) {
	return nil, nil
}

func (m *MockNewsletterRepository) RevertSending(ctx context.Context, id uuid.UUID) error {
	return nil
}

func (m *MockNewsletterRepository) RecoverStuckSending(ctx context.Context, olderThan time.Duration) (int64, error) {
	return 0, nil
}

func (m *MockNewsletterRepository) RecordOpen(ctx context.Context, newsletterID uuid.UUID, recipientHash string) error {
	return nil
}

func (m *MockNewsletterRepository) Analytics(ctx context.Context, newsletterID uuid.UUID) (*model.Analytics, error) {
	return nil, nil
}

func (m *MockNewsletterRepository) CreateUnsubscribe(ctx context.Context, projectUID, email string) error {
	return nil
}

func (m *MockNewsletterRepository) ListUnsubscribedEmails(ctx context.Context, projectUID string) (map[string]struct{}, error) {
	return nil, nil
}

func (m *MockNewsletterRepository) ListUnsubscribes(ctx context.Context, projectUID string) ([]*model.NewsletterUnsubscribe, error) {
	return nil, nil
}

func (m *MockNewsletterRepository) DeleteUnsubscribe(ctx context.Context, projectUID string, id uuid.UUID) error {
	return nil
}

func TestCreatePublication(t *testing.T) {
	h := &Handler{
		publication: service.NewPublicationService(NewMockPublicationRepository()),
		newsletter:  service.NewNewsletterService(NewMockNewsletterRepository(), nil),
	}

	body := publicapi.CreatePublicationRequest{
		Slug: "test-publication",
		Name: "Test Publication",
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest("POST", "/projects/project-1/newsletter-publications", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("project_uid", "project-1")

	rec := httptest.NewRecorder()
	h.CreatePublication(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("CreatePublication status: got %d, want %d", rec.Code, http.StatusCreated)
	}

	var result publicapi.NewsletterPublication
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if result.Slug != "test-publication" {
		t.Errorf("slug: got %s, want test-publication", result.Slug)
	}
	if result.Name != "Test Publication" {
		t.Errorf("name: got %s, want Test Publication", result.Name)
	}
}

func TestGetPublication(t *testing.T) {
	mockRepo := NewMockPublicationRepository()
	pub := &model.NewsletterPublication{
		ProjectUID:     "project-1",
		Slug:           "test-pub",
		Name:           "Test Publication",
		WrapperContent: []byte("{}"),
		CreatedBy:      "test-user",
	}
	if err := mockRepo.Create(context.Background(), pub); err != nil {
		t.Fatalf("seed publication: %v", err)
	}

	h := &Handler{
		publication: service.NewPublicationService(mockRepo),
		newsletter:  service.NewNewsletterService(NewMockNewsletterRepository(), nil),
	}

	req := httptest.NewRequest("GET", "/projects/project-1/newsletter-publications/"+pub.ID.String(), nil)
	rec := httptest.NewRecorder()

	// Manually set path value (httptest doesn't parse path values)
	req.SetPathValue("project_uid", "project-1")
	req.SetPathValue("publication_uid", pub.ID.String())

	h.GetPublication(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GetPublication status: got %d, want %d", rec.Code, http.StatusOK)
	}

	var result publicapi.NewsletterPublication
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if result.ID != pub.ID.String() {
		t.Errorf("ID: got %s, want %s", result.ID, pub.ID.String())
	}
}

func TestListPublications(t *testing.T) {
	mockRepo := NewMockPublicationRepository()
	pub1 := &model.NewsletterPublication{
		ProjectUID:     "project-1",
		Slug:           "pub-1",
		Name:           "Pub 1",
		WrapperContent: []byte("{}"),
		CreatedBy:      "test-user",
	}
	pub2 := &model.NewsletterPublication{
		ProjectUID:     "project-1",
		Slug:           "pub-2",
		Name:           "Pub 2",
		WrapperContent: []byte("{}"),
		CreatedBy:      "test-user",
	}
	if err := mockRepo.Create(context.Background(), pub1); err != nil {
		t.Fatalf("seed publication: %v", err)
	}
	if err := mockRepo.Create(context.Background(), pub2); err != nil {
		t.Fatalf("seed publication: %v", err)
	}

	h := &Handler{
		publication: service.NewPublicationService(mockRepo),
		newsletter:  service.NewNewsletterService(NewMockNewsletterRepository(), nil),
	}

	req := httptest.NewRequest("GET", "/projects/project-1/newsletter-publications", nil)
	req.SetPathValue("project_uid", "project-1")
	rec := httptest.NewRecorder()

	h.ListPublications(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("ListPublications status: got %d, want %d", rec.Code, http.StatusOK)
	}

	var result publicapi.PublicationListResponse
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(result.Publications) != 2 {
		t.Errorf("publication count: got %d, want 2", len(result.Publications))
	}
}

func TestUpdatePublication(t *testing.T) {
	mockRepo := NewMockPublicationRepository()
	pub := &model.NewsletterPublication{
		ProjectUID:     "project-1",
		Slug:           "test-pub",
		Name:           "Test Publication",
		WrapperContent: []byte("{}"),
		CreatedBy:      "test-user",
	}
	if err := mockRepo.Create(context.Background(), pub); err != nil {
		t.Fatalf("seed publication: %v", err)
	}

	h := &Handler{
		publication: service.NewPublicationService(mockRepo),
		newsletter:  service.NewNewsletterService(NewMockNewsletterRepository(), nil),
	}

	updateBody := publicapi.UpdatePublicationRequest{
		Name: stringPtr("Updated Name"),
	}
	bodyBytes, _ := json.Marshal(updateBody)

	req := httptest.NewRequest("PUT", "/projects/project-1/newsletter-publications/"+pub.ID.String(), bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"1"`)
	req.SetPathValue("project_uid", "project-1")
	req.SetPathValue("publication_uid", pub.ID.String())

	rec := httptest.NewRecorder()
	h.UpdatePublication(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("UpdatePublication status: got %d, want %d", rec.Code, http.StatusOK)
	}

	var result publicapi.NewsletterPublication
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if result.Name != "Updated Name" {
		t.Errorf("name: got %s, want Updated Name", result.Name)
	}
	if result.Version != 2 {
		t.Errorf("version: got %d, want 2", result.Version)
	}
}

func stringPtr(s string) *string {
	return &s
}
