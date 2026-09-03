// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// TestPublicationRoutesRequireAuth exercises the publication paths through
// Routes() rather than by calling the handler methods directly. The direct
// calls prove the handler logic but say nothing about whether the route was
// registered behind withAuth — a route wired outside the auth wrapper would
// pass every existing publication test while being publicly reachable.
func TestPublicationRoutesRequireAuth(t *testing.T) {
	const (
		listPath = "/projects/project-1/newsletter-publications"
		onePath  = "/projects/project-1/newsletter-publications/" + fixedPublicationUID
	)

	routes := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"create", http.MethodPost, listPath, `{"slug":"pub","name":"Pub"}`},
		{"list", http.MethodGet, listPath, ""},
		{"get", http.MethodGet, onePath, ""},
		{"update", http.MethodPut, onePath, `{"name":"Renamed"}`},
	}

	for _, rt := range routes {
		t.Run(rt.name+" rejects an unauthenticated request", func(t *testing.T) {
			srv := New(Config{
				RequireUserAuth: true,
				Publication:     service.NewPublicationService(NewMockPublicationRepository()),
				Newsletter:      service.NewNewsletterService(NewMockNewsletterRepository(), nil),
			}).Routes()

			var body *bytes.Reader
			if rt.body != "" {
				body = bytes.NewReader([]byte(rt.body))
			} else {
				body = bytes.NewReader(nil)
			}
			req := httptest.NewRequest(rt.method, rt.path, body)
			req.Header.Set("Content-Type", "application/json")
			// Deliberately no Authorization header.

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			// The point is that the request does not reach the handler. A 200/201
			// here would mean the route dodged withAuth entirely.
			if rec.Code == http.StatusOK || rec.Code == http.StatusCreated {
				t.Fatalf("%s %s returned %d without an Authorization header — the route is not behind withAuth",
					rt.method, rt.path, rec.Code)
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: status = %d, want 401", rt.method, rt.path, rec.Code)
			}
		})
	}

	// The routes must also actually exist: a 404 for every path would satisfy
	// the assertions above for the wrong reason.
	t.Run("paths are registered", func(t *testing.T) {
		srv := New(Config{
			RequireUserAuth: false,
			Publication:     service.NewPublicationService(NewMockPublicationRepository()),
			Newsletter:      service.NewNewsletterService(NewMockNewsletterRepository(), nil),
		}).Routes()

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, listPath, nil))
		if rec.Code == http.StatusNotFound {
			t.Fatalf("GET %s returned 404 — the route is not registered, so the auth assertions above prove nothing", listPath)
		}
	})
}

// TestUpdatePublicationRouteClearsNullableFields drives a real PUT through
// Routes() with explicit JSON null for each nullable field, proving the
// three-state decode survives the full HTTP path — body decode, presence flag,
// service merge — and not just the parser in isolation.
func TestUpdatePublicationRouteClearsNullableFields(t *testing.T) {
	repo := NewMockPublicationRepository()
	tmpl, sender, view := "aaif-template", "ed@example.org", "https://example.org/n"
	pub := &model.NewsletterPublication{
		ID:             uuid.MustParse(fixedPublicationUID),
		ProjectUID:     "project-1",
		Slug:           "clearable",
		Name:           "Clearable",
		WrapperContent: []byte("{}"),
		CreatedBy:      "user",
		EditorType:     model.EditorTypeClassic,
		TemplateSetID:  &tmpl,
		SenderEmail:    &sender,
		ViewOnlineBase: &view,
	}
	if err := repo.Create(t.Context(), pub); err != nil {
		t.Fatalf("seed publication: %v", err)
	}

	srv := New(Config{
		Publication: service.NewPublicationService(repo),
		Newsletter:  service.NewNewsletterService(NewMockNewsletterRepository(), nil),
	}).Routes()

	// Explicit nulls, not omitted keys — this is the state a *string would have
	// collapsed into "leave it alone".
	body := `{"template_set_id":null,"sender_email":null,"view_online_base":null}`
	req := httptest.NewRequest(http.MethodPut,
		"/projects/project-1/newsletter-publications/"+fixedPublicationUID,
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"1"`)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200. body: %s", rec.Code, rec.Body.String())
	}

	var got publicapi.NewsletterPublication
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.TemplateSetID != nil {
		t.Errorf("template_set_id = %q, want cleared", *got.TemplateSetID)
	}
	if got.SenderEmail != nil {
		t.Errorf("sender_email = %q, want cleared", *got.SenderEmail)
	}
	if got.ViewOnlineBase != nil {
		t.Errorf("view_online_base = %q, want cleared", *got.ViewOnlineBase)
	}

	// And the clear reached the store, not just the response body.
	stored, err := repo.Get(t.Context(), "project-1", pub.ID)
	if err != nil {
		t.Fatalf("re-read publication: %v", err)
	}
	if stored.TemplateSetID != nil || stored.SenderEmail != nil || stored.ViewOnlineBase != nil {
		t.Errorf("stored values not cleared: template=%v sender=%v view=%v",
			stored.TemplateSetID, stored.SenderEmail, stored.ViewOnlineBase)
	}
}

// TestUpdatePublicationRouteOmittedKeysPreserve is the counterpart: a PUT that
// mentions only `name` must leave all three nullable fields untouched.
func TestUpdatePublicationRouteOmittedKeysPreserve(t *testing.T) {
	repo := NewMockPublicationRepository()
	tmpl, sender, view := "aaif-template", "ed@example.org", "https://example.org/n"
	pub := &model.NewsletterPublication{
		ID:             uuid.MustParse(fixedPublicationUID),
		ProjectUID:     "project-1",
		Slug:           "preserved",
		Name:           "Preserved",
		WrapperContent: []byte("{}"),
		CreatedBy:      "user",
		EditorType:     model.EditorTypeClassic,
		TemplateSetID:  &tmpl,
		SenderEmail:    &sender,
		ViewOnlineBase: &view,
	}
	if err := repo.Create(t.Context(), pub); err != nil {
		t.Fatalf("seed publication: %v", err)
	}

	srv := New(Config{
		Publication: service.NewPublicationService(repo),
		Newsletter:  service.NewNewsletterService(NewMockNewsletterRepository(), nil),
	}).Routes()

	req := httptest.NewRequest(http.MethodPut,
		"/projects/project-1/newsletter-publications/"+fixedPublicationUID,
		bytes.NewReader([]byte(`{"name":"Renamed"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"1"`)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200. body: %s", rec.Code, rec.Body.String())
	}

	stored, err := repo.Get(t.Context(), "project-1", pub.ID)
	if err != nil {
		t.Fatalf("re-read publication: %v", err)
	}
	if stored.Name != "Renamed" {
		t.Errorf("name = %q, want %q", stored.Name, "Renamed")
	}
	if stored.TemplateSetID == nil || *stored.TemplateSetID != tmpl {
		t.Errorf("template_set_id = %v, want it preserved as %q", stored.TemplateSetID, tmpl)
	}
	if stored.SenderEmail == nil || *stored.SenderEmail != sender {
		t.Errorf("sender_email = %v, want it preserved as %q", stored.SenderEmail, sender)
	}
	if stored.ViewOnlineBase == nil || *stored.ViewOnlineBase != view {
		t.Errorf("view_online_base = %v, want it preserved as %q", stored.ViewOnlineBase, view)
	}
}

// fixedPublicationUID keeps the seeded id and the request path in sync.
const fixedPublicationUID = "11111111-2222-3333-4444-555555555555"
