// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRoutes_RenderPreviewTemplatesRequireAuth asserts that when RequireUserAuth
// is enabled, the render-preview and templates route registrations reject
// requests with no bearer token via withAuth — proving the routing itself is
// protected, not just the handler methods exercised directly elsewhere.
func TestRoutes_RenderPreviewTemplatesRequireAuth(t *testing.T) {
	h := &Handler{requireUserAuth: true}
	routes := h.Routes()

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"render-preview", http.MethodPost, "/projects/proj-1/newsletters/render-preview"},
		{"list-templates", http.MethodGet, "/projects/proj-1/newsletters/templates"},
		{"get-template-manifest", http.MethodGet, "/projects/proj-1/newsletters/templates/default/manifest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			routes.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (missing bearer). body=%s", w.Code, w.Body.String())
			}
		})
	}
}

// TestRoutes_RenderPreviewTemplatesRouting asserts the three route
// registrations dispatch to the correct handler for their exact path
// patterns, in dev mode (RequireUserAuth false) so no JWT validator is
// needed to reach the handler.
func TestRoutes_RenderPreviewTemplatesRouting(t *testing.T) {
	h := &Handler{}
	routes := h.Routes()

	t.Run("render-preview", func(t *testing.T) {
		body := `{"body_layout":{"blocks":[{"block_type":"intro_paragraph","content":{"text":"hi"}}]}}`
		req := httptest.NewRequest(http.MethodPost, "/projects/proj-1/newsletters/render-preview", strings.NewReader(body))
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("list-templates", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/projects/proj-1/newsletters/templates", nil)
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("get-template-manifest", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/projects/proj-1/newsletters/templates/aaif-user-community/manifest", nil)
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
		}
	})
}
