// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

type stubUnsubRepo struct {
	created []string
}

func (s *stubUnsubRepo) CreateUnsubscribe(_ context.Context, projectUID, email string) error {
	s.created = append(s.created, projectUID+"|"+email)
	return nil
}
func (s *stubUnsubRepo) ListUnsubscribedEmails(_ context.Context, _ string) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}
func (s *stubUnsubRepo) ListUnsubscribes(_ context.Context, _ string) ([]*model.NewsletterUnsubscribe, error) {
	return nil, nil
}
func (s *stubUnsubRepo) DeleteUnsubscribe(_ context.Context, _ string, _ uuid.UUID) error {
	return nil
}

type stubProjectClient struct{}

func (stubProjectClient) Name(_ context.Context, _ string) (string, error) { return "CNCF", nil }
func (stubProjectClient) Slug(_ context.Context, _ string) (string, error) { return "cncf", nil }

func TestUnsubscribeHandlerSuccess(t *testing.T) {
	repo := &stubUnsubRepo{}
	unsub := service.NewUnsubscribeService(repo, []byte("k"), "http://localhost")
	h := &Handler{unsub: unsub, project: stubProjectClient{}}

	url := unsub.BuildURL("proj-1", "alice@example.com")
	token := url[strings.Index(url, "?t=")+3:]

	req := httptest.NewRequest(http.MethodGet, "/newsletters/unsubscribe?t="+token, nil)
	w := httptest.NewRecorder()
	h.Unsubscribe(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "alice@example.com") || !strings.Contains(body, "CNCF") {
		t.Errorf("body missing email or project name: %s", body)
	}
	if len(repo.created) != 1 || repo.created[0] != "proj-1|alice@example.com" {
		t.Errorf("repo.created = %v, want [proj-1|alice@example.com]", repo.created)
	}
}

func TestUnsubscribeHandlerHEADIsNoOp(t *testing.T) {
	repo := &stubUnsubRepo{}
	unsub := service.NewUnsubscribeService(repo, []byte("k"), "http://localhost")
	h := &Handler{unsub: unsub, project: stubProjectClient{}}

	url := unsub.BuildURL("proj-1", "alice@example.com")
	token := url[strings.Index(url, "?t=")+3:]

	req := httptest.NewRequest(http.MethodHead, "/newsletters/unsubscribe?t="+token, nil)
	w := httptest.NewRecorder()
	h.Unsubscribe(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if len(repo.created) != 0 {
		t.Errorf("HEAD must not record an unsubscribe; repo.created = %v", repo.created)
	}
}

func TestUnsubscribeHandlerInvalidToken(t *testing.T) {
	unsub := service.NewUnsubscribeService(&stubUnsubRepo{}, []byte("k"), "http://localhost")
	h := &Handler{unsub: unsub, project: stubProjectClient{}}

	req := httptest.NewRequest(http.MethodGet, "/newsletters/unsubscribe?t=garbage", nil)
	w := httptest.NewRecorder()
	h.Unsubscribe(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid") && !strings.Contains(w.Body.String(), "Invalid") {
		t.Errorf("body should mention invalid link: %s", w.Body.String())
	}
}

func TestListOptOutsSuccess(t *testing.T) {
	repo := &stubUnsubRepo{}
	unsub := service.NewUnsubscribeService(repo, []byte("k"), "http://localhost")
	h := &Handler{unsub: unsub}

	req := httptest.NewRequest(http.MethodGet, "/projects/proj-1/newsletter-opt-outs", nil)
	// Simulate path parameter extraction via Go 1.22+ ServeMux
	req.SetPathValue("project_uid", "proj-1")
	w := httptest.NewRecorder()
	h.ListOptOuts(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "opt_outs") {
		t.Errorf("body missing opt_outs field: %s", body)
	}
}

func TestListOptOutsInvalidProject(t *testing.T) {
	repo := &stubUnsubRepo{}
	unsub := service.NewUnsubscribeService(repo, []byte("k"), "http://localhost")
	h := &Handler{unsub: unsub}

	// Create a valid request, but override the path value with whitespace
	req := httptest.NewRequest(http.MethodGet, "/projects/valid-uid/newsletter-opt-outs", nil)
	req.SetPathValue("project_uid", "  ") // whitespace-only projectUID
	w := httptest.NewRecorder()
	h.ListOptOuts(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid_request") {
		t.Errorf("body should mention invalid_request: %s", w.Body.String())
	}
}

func TestListOptOutsWithData(t *testing.T) {
	aliceID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	bobID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	repo := &testOptOutRepo{
		optOuts: []*model.NewsletterUnsubscribe{
			{ID: aliceID, Email: "alice@example.com", CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
			{ID: bobID, Email: "bob@example.com", CreatedAt: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)},
		},
	}
	unsub := service.NewUnsubscribeService(repo, []byte("k"), "http://localhost")
	h := &Handler{unsub: unsub}

	req := httptest.NewRequest(http.MethodGet, "/projects/proj-1/newsletter-opt-outs", nil)
	req.SetPathValue("project_uid", "proj-1")
	w := httptest.NewRecorder()
	h.ListOptOuts(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "alice@example.com") || !strings.Contains(body, "bob@example.com") {
		t.Errorf("body missing expected emails: %s", body)
	}
	if !strings.Contains(body, "2024-01-01") || !strings.Contains(body, "2024-01-02") {
		t.Errorf("body missing expected timestamps: %s", body)
	}
	// The id is the contract clients use with the DELETE endpoint — decode
	// and assert it round-trips per entry, not just that some id appears.
	var decoded publicapi.OptOutListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(decoded.OptOuts) != 2 {
		t.Fatalf("len(opt_outs) = %d, want 2", len(decoded.OptOuts))
	}
	wantIDs := map[string]string{
		"alice@example.com": aliceID.String(),
		"bob@example.com":   bobID.String(),
	}
	for _, o := range decoded.OptOuts {
		if o.ID != wantIDs[o.Email] {
			t.Errorf("id for %s = %q, want %q", o.Email, o.ID, wantIDs[o.Email])
		}
	}
	// Verify the requested project_uid was passed to the repository
	if repo.lastProjectUID != "proj-1" {
		t.Errorf("repo.lastProjectUID = %q, want proj-1", repo.lastProjectUID)
	}
}

func TestListOptOutsRepositoryError(t *testing.T) {
	// Use a non-domain error to simulate a real database/persistence failure,
	// not a validation error.
	repo := &testOptOutRepo{
		returnErr: errors.New("postgres connection failed"),
	}
	unsub := service.NewUnsubscribeService(repo, []byte("k"), "http://localhost")
	h := &Handler{unsub: unsub}

	req := httptest.NewRequest(http.MethodGet, "/projects/proj-1/newsletter-opt-outs", nil)
	req.SetPathValue("project_uid", "proj-1")
	w := httptest.NewRecorder()
	h.ListOptOuts(w, req)

	// Persistence failures should return 500 without leaking details
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for persistence error, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "internal_error") {
		t.Errorf("body missing internal_error code: %s", body)
	}
	if strings.Contains(body, "postgres") || strings.Contains(body, "connection failed") {
		t.Errorf("body leaked error details: %s", body)
	}
	// Verify the requested project_uid was passed even when error occurs
	if repo.lastProjectUID != "proj-1" {
		t.Errorf("repo.lastProjectUID = %q, want proj-1", repo.lastProjectUID)
	}
}

func TestListOptOutsProjectScope(t *testing.T) {
	// Verify that each call passes its own project_uid to the repository,
	// so a regression that queries the wrong project would be caught.
	repo := &testOptOutRepo{
		optOuts: []*model.NewsletterUnsubscribe{
			{Email: "proj1user@example.com", CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
	}
	unsub := service.NewUnsubscribeService(repo, []byte("k"), "http://localhost")
	h := &Handler{unsub: unsub}

	// Request for proj-1
	req1 := httptest.NewRequest(http.MethodGet, "/projects/proj-1/newsletter-opt-outs", nil)
	req1.SetPathValue("project_uid", "proj-1")
	w1 := httptest.NewRecorder()
	h.ListOptOuts(w1, req1)

	if w1.Code != http.StatusOK {
		t.Fatalf("proj-1 request failed: status = %d, body=%s", w1.Code, w1.Body.String())
	}
	if repo.lastProjectUID != "proj-1" {
		t.Errorf("proj-1: repo.lastProjectUID = %q, want proj-1", repo.lastProjectUID)
	}

	// Request for proj-2
	req2 := httptest.NewRequest(http.MethodGet, "/projects/proj-2/newsletter-opt-outs", nil)
	req2.SetPathValue("project_uid", "proj-2")
	w2 := httptest.NewRecorder()
	h.ListOptOuts(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("proj-2 request failed: status = %d, body=%s", w2.Code, w2.Body.String())
	}
	if repo.lastProjectUID != "proj-2" {
		t.Errorf("proj-2: repo.lastProjectUID = %q, want proj-2", repo.lastProjectUID)
	}
}

// testOptOutRepo is a test stub that tracks the projectUID requested and returns canned opt-out data.
type testOptOutRepo struct {
	optOuts              []*model.NewsletterUnsubscribe
	lastProjectUID       string
	lastDeleteProjectUID string
	lastDeleteID         uuid.UUID
	returnErr            error
}

func (t *testOptOutRepo) CreateUnsubscribe(_ context.Context, projectUID, email string) error {
	return nil
}
func (t *testOptOutRepo) ListUnsubscribedEmails(_ context.Context, _ string) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}
func (t *testOptOutRepo) ListUnsubscribes(ctx context.Context, projectUID string) ([]*model.NewsletterUnsubscribe, error) {
	t.lastProjectUID = projectUID
	if t.returnErr != nil {
		return nil, t.returnErr
	}
	return t.optOuts, nil
}
func (t *testOptOutRepo) DeleteUnsubscribe(_ context.Context, projectUID string, id uuid.UUID) error {
	t.lastDeleteProjectUID = projectUID
	t.lastDeleteID = id
	if t.returnErr != nil {
		return t.returnErr
	}
	return nil
}

// TestDeleteOptOutSuccess tests the happy path: a valid UUID and project_uid
// returns 204 No Content.
func TestDeleteOptOutSuccess(t *testing.T) {
	repo := &testOptOutRepo{}
	unsub := service.NewUnsubscribeService(repo, []byte("k"), "http://localhost")
	h := &Handler{unsub: unsub}

	testID := uuid.New()
	req := httptest.NewRequest(http.MethodDelete, "/projects/proj-1/newsletter-opt-outs/"+testID.String(), nil)
	req.SetPathValue("project_uid", "proj-1")
	req.SetPathValue("opt_out_id", testID.String())
	w := httptest.NewRecorder()
	h.DeleteOptOut(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204, body=%s", w.Code, w.Body.String())
	}
	// Project scoping is the boundary that stops one project from deleting
	// another project's opt-out — assert both values reach the repository.
	if repo.lastDeleteProjectUID != "proj-1" {
		t.Errorf("repo.lastDeleteProjectUID = %q, want proj-1", repo.lastDeleteProjectUID)
	}
	if repo.lastDeleteID != testID {
		t.Errorf("repo.lastDeleteID = %s, want %s", repo.lastDeleteID, testID)
	}
}

// TestDeleteOptOutNotFound tests that a non-existent opt-out id returns 404.
func TestDeleteOptOutNotFound(t *testing.T) {
	repo := &testOptOutRepo{
		returnErr: domain.ErrNotFound,
	}
	unsub := service.NewUnsubscribeService(repo, []byte("k"), "http://localhost")
	h := &Handler{unsub: unsub}

	testID := uuid.New().String()
	req := httptest.NewRequest(http.MethodDelete, "/projects/proj-1/newsletter-opt-outs/"+testID, nil)
	req.SetPathValue("project_uid", "proj-1")
	req.SetPathValue("opt_out_id", testID)
	w := httptest.NewRecorder()
	h.DeleteOptOut(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "not_found") {
		t.Errorf("body should contain not_found code: %s", w.Body.String())
	}
}

// TestDeleteOptOutRepositoryError tests that a persistence failure returns 500
// without leaking error details.
func TestDeleteOptOutRepositoryError(t *testing.T) {
	repo := &testOptOutRepo{
		returnErr: errors.New("postgres connection failed"),
	}
	unsub := service.NewUnsubscribeService(repo, []byte("k"), "http://localhost")
	h := &Handler{unsub: unsub}

	testID := uuid.New().String()
	req := httptest.NewRequest(http.MethodDelete, "/projects/proj-1/newsletter-opt-outs/"+testID, nil)
	req.SetPathValue("project_uid", "proj-1")
	req.SetPathValue("opt_out_id", testID)
	w := httptest.NewRecorder()
	h.DeleteOptOut(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for persistence error, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "internal_error") {
		t.Errorf("body missing internal_error code: %s", body)
	}
	if strings.Contains(body, "postgres") || strings.Contains(body, "connection failed") {
		t.Errorf("body leaked error details: %s", body)
	}
}

// TestDeleteOptOutMalformedUUID tests that a malformed UUID returns 400.
func TestDeleteOptOutMalformedUUID(t *testing.T) {
	repo := &testOptOutRepo{}
	unsub := service.NewUnsubscribeService(repo, []byte("k"), "http://localhost")
	h := &Handler{unsub: unsub}

	req := httptest.NewRequest(http.MethodDelete, "/projects/proj-1/newsletter-opt-outs/not-a-uuid", nil)
	req.SetPathValue("project_uid", "proj-1")
	req.SetPathValue("opt_out_id", "not-a-uuid")
	w := httptest.NewRecorder()
	h.DeleteOptOut(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for malformed UUID, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid_request") {
		t.Errorf("body should mention invalid_request: %s", w.Body.String())
	}
}

// TestDeleteOptOutBlankProjectUID tests that a blank project_uid returns 400.
func TestDeleteOptOutBlankProjectUID(t *testing.T) {
	repo := &testOptOutRepo{}
	unsub := service.NewUnsubscribeService(repo, []byte("k"), "http://localhost")
	h := &Handler{unsub: unsub}

	testID := uuid.New().String()
	req := httptest.NewRequest(http.MethodDelete, "/projects/proj-1/newsletter-opt-outs/"+testID, nil)
	req.SetPathValue("project_uid", "  ") // whitespace-only projectUID
	req.SetPathValue("opt_out_id", testID)
	w := httptest.NewRecorder()
	h.DeleteOptOut(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for blank project_uid", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid_request") {
		t.Errorf("body should mention invalid_request: %s", w.Body.String())
	}
}
