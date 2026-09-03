// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// TestListNewslettersPublicationFilter proves the editions view's filter works:
// GET .../newsletters?publication_id=X scopes the list to that publication, and
// a malformed value is a 400.
func TestListNewslettersPublicationFilter(t *testing.T) {
	pubA := uuid.New()
	pubB := uuid.New()
	repo := NewMockNewsletterRepository()
	// Two editions in pubA, one in pubB, one unlinked — all in project-1.
	seed := []*model.Newsletter{
		{ID: uuid.New(), ProjectUID: "project-1", Subject: "A1", Status: model.StatusSent, PublicationID: &pubA},
		{ID: uuid.New(), ProjectUID: "project-1", Subject: "A2", Status: model.StatusDraft, PublicationID: &pubA},
		{ID: uuid.New(), ProjectUID: "project-1", Subject: "B1", Status: model.StatusSent, PublicationID: &pubB},
		{ID: uuid.New(), ProjectUID: "project-1", Subject: "none", Status: model.StatusDraft},
	}
	for _, n := range seed {
		_ = repo.Create(t.Context(), n)
	}
	h := &Handler{newsletter: service.NewNewsletterService(repo, nil)}

	list := func(query string) publicapi.NewsletterListResponse {
		req := httptest.NewRequest("GET", "/projects/project-1/newsletters"+query, nil)
		req.SetPathValue("project_uid", "project-1")
		rec := httptest.NewRecorder()
		h.ListNewsletters(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
		}
		var out publicapi.NewsletterListResponse
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	if got := list("?publication_id=" + pubA.String()); len(got.Newsletters) != 2 {
		t.Errorf("publication_id=pubA: got %d newsletters, want 2", len(got.Newsletters))
	}
	if got := list("?publication_id=" + pubB.String()); len(got.Newsletters) != 1 {
		t.Errorf("publication_id=pubB: got %d newsletters, want 1", len(got.Newsletters))
	}
	if got := list(""); len(got.Newsletters) != 4 {
		t.Errorf("no filter: got %d newsletters, want 4 (all)", len(got.Newsletters))
	}

	// Malformed publication_id → 400.
	req := httptest.NewRequest("GET", "/projects/project-1/newsletters?publication_id=not-a-uuid", nil)
	req.SetPathValue("project_uid", "project-1")
	rec := httptest.NewRecorder()
	h.ListNewsletters(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed publication_id: status = %d, want 400", rec.Code)
	}
}
