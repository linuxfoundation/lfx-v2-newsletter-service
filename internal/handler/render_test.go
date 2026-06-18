// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// TestRenderPreviewSuccess renders a valid layout against the embedded
// templates and asserts a 200 with non-empty, email-safe (table-based) HTML
// that carries the bound content.
func TestRenderPreviewSuccess(t *testing.T) {
	h := &Handler{}

	reqBody := publicapi.RenderPreviewRequest{
		BodyLayout: publicapi.NewsletterLayout{
			Blocks: []publicapi.LayoutBlock{
				{
					BlockType: "intro_paragraph",
					Content: map[string]any{
						"text": "<p>Welcome to <strong>this week's</strong> edition.</p>",
					},
				},
			},
		},
		WrapperContent: map[string]any{
			"edition": map[string]any{
				"date": "January 1, 2026",
			},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/projects/proj-1/newsletters/render-preview", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.RenderPreview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
	}

	var resp publicapi.RenderPreviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v. body=%s", err, w.Body.String())
	}
	if resp.BodyHTML == "" {
		t.Fatal("expected non-empty body_html")
	}
	if !strings.Contains(resp.BodyHTML, "<table") {
		t.Errorf("expected table-based email HTML, got:\n%s", resp.BodyHTML)
	}
	// Bound richtext must survive into the compiled HTML.
	if !strings.Contains(resp.BodyHTML, "this week's") {
		t.Errorf("expected bound richtext in output:\n%s", resp.BodyHTML)
	}
	// Supplied wrapper content (edition.date) must render.
	if !strings.Contains(resp.BodyHTML, "January 1, 2026") {
		t.Errorf("expected edition.date in output:\n%s", resp.BodyHTML)
	}
}

// TestRenderPreviewUnprocessable asserts that a layout referencing an unknown
// block_type — a well-formed request the renderer cannot bind — returns 422
// with the error surfaced in the body.
func TestRenderPreviewUnprocessable(t *testing.T) {
	h := &Handler{}

	reqBody := publicapi.RenderPreviewRequest{
		BodyLayout: publicapi.NewsletterLayout{
			Blocks: []publicapi.LayoutBlock{
				{
					BlockType: "does_not_exist",
					Content:   map[string]any{},
				},
			},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/projects/proj-1/newsletters/render-preview", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.RenderPreview(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422. body=%s", w.Code, w.Body.String())
	}

	var payload errorPayload
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v. body=%s", err, w.Body.String())
	}
	if payload.Error != "unprocessable_entity" {
		t.Errorf("error code = %q, want unprocessable_entity", payload.Error)
	}
	// The emitter's "block template not found" detail should reach the client.
	if !strings.Contains(payload.Message, "does_not_exist") {
		t.Errorf("expected message to name the unknown block, got: %q", payload.Message)
	}
}

// TestRenderPreviewBadJSON asserts a malformed body is a 400 (decode error),
// distinct from the 422 render-failure path.
func TestRenderPreviewBadJSON(t *testing.T) {
	h := &Handler{}

	req := httptest.NewRequest(http.MethodPost, "/projects/proj-1/newsletters/render-preview", strings.NewReader("{not json"))
	w := httptest.NewRecorder()
	h.RenderPreview(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. body=%s", w.Code, w.Body.String())
	}
}
