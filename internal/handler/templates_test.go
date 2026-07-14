// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service/render/declarative"
)

// TestListTemplates asserts the template catalog route returns 200 with every
// embedded template key and a human-readable label.
func TestListTemplates(t *testing.T) {
	h := &Handler{}

	req := httptest.NewRequest(http.MethodGet, "/projects/proj-1/newsletters/templates", nil)
	w := httptest.NewRecorder()
	h.ListTemplates(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
	}
	var resp templatesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v. body=%s", err, w.Body.String())
	}
	if len(resp.Templates) != 3 {
		t.Fatalf("expected 3 templates, got %d", len(resp.Templates))
	}
	byKey := map[string]string{}
	for _, tpl := range resp.Templates {
		byKey[tpl.Key] = tpl.Label
	}
	if byKey["default"] != "Default" {
		t.Errorf("default label = %q, want Default", byKey["default"])
	}
	if byKey["aaif-user-community"] == "" {
		t.Errorf("expected a label for aaif-user-community")
	}
	if byKey["executive-director-weekly"] != "Executive Director Weekly" {
		t.Errorf("executive-director-weekly label = %q, want Executive Director Weekly", byKey["executive-director-weekly"])
	}
}

// TestGetTemplateManifest asserts the manifest route returns 200 with the full
// palette for a known key.
func TestGetTemplateManifest(t *testing.T) {
	h := &Handler{}

	req := httptest.NewRequest(http.MethodGet, "/projects/proj-1/newsletters/templates/aaif-user-community/manifest", nil)
	req.SetPathValue("template_key", "aaif-user-community")
	w := httptest.NewRecorder()
	h.GetTemplateManifest(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
	}
	var manifest declarative.Manifest
	if err := json.Unmarshal(w.Body.Bytes(), &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if manifest.WrapperKey != "default" {
		t.Errorf("wrapper_key = %q, want default", manifest.WrapperKey)
	}
	if len(manifest.Blocks) != 23 {
		t.Errorf("expected 23 palette entries, got %d", len(manifest.Blocks))
	}
	if manifest.Wrapper == "" {
		t.Errorf("expected a non-empty wrapper body")
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
