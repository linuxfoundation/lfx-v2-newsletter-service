// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// TestListTemplates asserts the template catalog route returns 200 with every
// embedded template key and a human-readable label, sorted by key.
func TestListTemplates(t *testing.T) {
	h := &Handler{}

	req := httptest.NewRequest(http.MethodGet, "/projects/proj-1/newsletters/templates", nil)
	w := httptest.NewRecorder()
	h.ListTemplates(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
	}
	var resp publicapi.TemplatesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v. body=%s", err, w.Body.String())
	}
	want := []publicapi.TemplateSummary{
		{Key: "aaif-member", Label: "AAIF Member"},
		{Key: "aaif-user-community", Label: "AAIF User Community"},
		{Key: "aaif-weekly-agentic", Label: "AAIF Weekly Agentic"},
	}
	if len(resp.Templates) != len(want) {
		t.Fatalf("expected %d templates, got %d (%+v)", len(want), len(resp.Templates), resp.Templates)
	}
	for i, w := range want {
		if resp.Templates[i] != w {
			t.Errorf("templates[%d] = %+v, want %+v", i, resp.Templates[i], w)
		}
	}
}

// TestGetTemplateManifest asserts the manifest route returns 200 with the
// palette, the wrapper and the starter blocks for each embedded key. A set's
// bricks share one palette with its blocks, so the user community count is its
// 22 blocks plus 4 bricks.
func TestGetTemplateManifest(t *testing.T) {
	h := &Handler{}

	cases := []struct {
		key          string
		blocks       int
		starters     int
		firstStarter string
	}{
		{"aaif-user-community", 26, 9, "intro_paragraph"},
		{"aaif-weekly-agentic", 12, 10, "lead_commentary"},
		{"aaif-member", 19, 19, "intro_letter"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/projects/proj-1/newsletters/templates/"+tc.key+"/manifest", nil)
			req.SetPathValue("template_key", tc.key)
			w := httptest.NewRecorder()
			h.GetTemplateManifest(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
			}
			var manifest publicapi.TemplateManifest
			if err := json.Unmarshal(w.Body.Bytes(), &manifest); err != nil {
				t.Fatalf("unmarshal manifest: %v", err)
			}
			if manifest.WrapperKey != "default" {
				t.Errorf("wrapper_key = %q, want default", manifest.WrapperKey)
			}
			if len(manifest.Blocks) != tc.blocks {
				t.Errorf("expected %d palette entries, got %d", tc.blocks, len(manifest.Blocks))
			}
			if manifest.Wrapper == "" {
				t.Errorf("expected a non-empty wrapper body")
			}
			if len(manifest.StarterBlocks) != tc.starters {
				t.Errorf("expected %d starter blocks, got %d", tc.starters, len(manifest.StarterBlocks))
			}
			if len(manifest.StarterBlocks) > 0 && manifest.StarterBlocks[0].BlockType != tc.firstStarter {
				t.Errorf("first starter block = %q, want %q", manifest.StarterBlocks[0].BlockType, tc.firstStarter)
			}
		})
	}
}

// TestGetTemplateManifest_UnknownKey asserts an unknown key is a 404 client
// error with the standard error payload.
func TestGetTemplateManifest_UnknownKey(t *testing.T) {
	h := &Handler{}

	req := httptest.NewRequest(http.MethodGet, "/projects/proj-1/newsletters/templates/nope/manifest", nil)
	req.SetPathValue("template_key", "nope")
	w := httptest.NewRecorder()
	h.GetTemplateManifest(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404. body=%s", w.Code, w.Body.String())
	}
	var payload errorPayload
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Error != "not_found" {
		t.Errorf("error code = %q, want not_found", payload.Error)
	}
}
