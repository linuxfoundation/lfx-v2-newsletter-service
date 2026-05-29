// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
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
