// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
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
		// The preview MERGES the client's wrapper_content over the send-path
		// footer defaults: the unsubscribe and My Newsletters sentinels come
		// from the send path for size parity, while client-owned bindings like
		// edition.date and the sender-line display values are honored.
		WrapperContent: map[string]any{
			"edition": map[string]any{
				"date":         "January 1, 2026",
				"sender_name":  "Preview Sender",
				"project_name": "Preview Project",
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
	// The wrapper's sender line binds the client-supplied display values.
	if !strings.Contains(resp.BodyHTML, "Sent by <strong>Preview Sender</strong> on behalf of <strong>Preview Project</strong>") {
		t.Errorf("expected the bound sender line in the preview footer:\n%s", resp.BodyHTML)
	}
	// Client-supplied edition.date IS rendered (in the header): the merge honors
	// client-owned (non-footer) wrapper_content bindings.
	if !strings.Contains(resp.BodyHTML, "January 1, 2026") {
		t.Errorf("expected client edition.date in the merged preview:\n%s", resp.BodyHTML)
	}
}

// TestRenderPreviewMergesWrapperContent asserts the merge honors client-owned
// non-footer bindings (edition.date) while a client attempt to override a
// footer sentinel (edition.unsubscribe_url) is ignored — the send-path sentinel is
// preserved so the preview's footer size matches the sent email.
func TestRenderPreviewMergesWrapperContent(t *testing.T) {
	h := &Handler{unsub: service.NewUnsubscribeService(nil, []byte("k"), "https://api.example")}

	reqBody := publicapi.RenderPreviewRequest{
		BodyLayout: publicapi.NewsletterLayout{
			Blocks: []publicapi.LayoutBlock{
				{BlockType: "intro_paragraph", Content: map[string]any{"text": "<p>Hi</p>"}},
			},
		},
		WrapperContent: map[string]any{
			"edition": map[string]any{
				"date":            "Preview Date",
				"unsubscribe_url": "https://evil.example/ClientSpoofedUnsub",
			},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/projects/p/newsletters/render-preview", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.RenderPreview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
	}
	var resp publicapi.RenderPreviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v. body=%s", err, w.Body.String())
	}
	// Client-owned non-footer binding is honored.
	if !strings.Contains(resp.BodyHTML, "Preview Date") {
		t.Errorf("expected client edition.date in preview:\n%s", resp.BodyHTML)
	}
	// Footer sentinel is preserved; the client's spoofed value must not appear.
	if !strings.Contains(resp.BodyHTML, "%%UNSUBSCRIBE_URL%%") {
		t.Errorf("expected send-path unsubscribe sentinel to survive the merge:\n%s", resp.BodyHTML)
	}
	if strings.Contains(resp.BodyHTML, "ClientSpoofedUnsub") {
		t.Errorf("client must not override the unsubscribe_url footer sentinel:\n%s", resp.BodyHTML)
	}
}

// TestRenderPreviewInjectsUnsubscribeFooter asserts that when the deployment has
// an enabled unsubscribe service, render-preview injects the send-time
// unsubscribe row (the %%UNSUBSCRIBE_URL%% sentinel) so the preview's footer —
// and its byte size — matches the email that will be sent.
func TestRenderPreviewInjectsUnsubscribeFooter(t *testing.T) {
	h := &Handler{unsub: service.NewUnsubscribeService(nil, []byte("k"), "https://api.example")}

	reqBody := publicapi.RenderPreviewRequest{
		BodyLayout: publicapi.NewsletterLayout{
			Blocks: []publicapi.LayoutBlock{
				{BlockType: "intro_paragraph", Content: map[string]any{"text": "<p>Hi</p>"}},
			},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/projects/p/newsletters/render-preview", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.RenderPreview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
	}
	var resp publicapi.RenderPreviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v. body=%s", err, w.Body.String())
	}
	if !strings.Contains(resp.BodyHTML, "%%UNSUBSCRIBE_URL%%") {
		t.Errorf("expected the unsubscribe sentinel in the preview footer when unsub is enabled:\n%s", resp.BodyHTML)
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

// TestRenderPreview_TemplateKey_RendersFromSelectedLibrary asserts a block that
// exists in the selected library renders 200 under that library.
func TestRenderPreview_TemplateKey_RendersFromSelectedLibrary(t *testing.T) {
	h := &Handler{}

	reqBody := publicapi.RenderPreviewRequest{
		BodyLayout: publicapi.NewsletterLayout{
			TemplateKey: "aaif-user-community", // intro_paragraph exists in this set
			Blocks: []publicapi.LayoutBlock{
				{BlockType: "intro_paragraph", Content: map[string]any{"text": "<p>Hi from the library</p>"}},
			},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/projects/p/newsletters/render-preview", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.RenderPreview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
	}
	var resp publicapi.RenderPreviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v. body=%s", err, w.Body.String())
	}
	if !strings.Contains(resp.BodyHTML, "Hi from the library") {
		t.Errorf("expected bound content in output:\n%s", resp.BodyHTML)
	}
}

// TestRenderPreview_UnknownTemplateKey_Is422 asserts an unknown library key is a
// client error (422) that names the key.
func TestRenderPreview_UnknownTemplateKey_Is422(t *testing.T) {
	h := &Handler{}

	reqBody := publicapi.RenderPreviewRequest{
		BodyLayout: publicapi.NewsletterLayout{
			TemplateKey: "no-such-library",
			Blocks:      []publicapi.LayoutBlock{{BlockType: "intro_paragraph", Content: map[string]any{"text": "hi"}}},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/projects/p/newsletters/render-preview", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.RenderPreview(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422. body=%s", w.Code, w.Body.String())
	}
	var payload errorPayload
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v. body=%s", err, w.Body.String())
	}
	if !strings.Contains(payload.Message, "no-such-library") {
		t.Errorf("expected message to name the unknown library, got: %q", payload.Message)
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

// TestRenderPreviewLeavesOnlyTheUnsubscribeSentinel renders a one-block layout
// for a named template library. The preview shows the block text, today's date,
// and no sentinel except the per-recipient unsubscribe one.
func TestRenderPreviewLeavesOnlyTheUnsubscribeSentinel(t *testing.T) {
	h := &Handler{unsub: service.NewUnsubscribeService(nil, []byte("k"), "https://api.example")}

	body := `{"body_layout":{"wrapper_key":"default","template_key":"aaif-user-community","blocks":[{"block_type":"intro_paragraph","content":{"text":"hi"}}]}}`
	req := httptest.NewRequest(http.MethodPost, "/projects/p/newsletters/render-preview", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.RenderPreview(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body=%s", w.Code, w.Body.String())
	}
	var resp publicapi.RenderPreviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !strings.Contains(resp.BodyHTML, "hi") {
		t.Errorf("preview missing the block text:\n%s", resp.BodyHTML)
	}
	for _, token := range regexp.MustCompile(`%%[A-Z_]+%%`).FindAllString(resp.BodyHTML, -1) {
		if token != "%%UNSUBSCRIBE_URL%%" {
			t.Errorf("preview carries the sentinel %s", token)
		}
	}
	if !strings.Contains(resp.BodyHTML, "%%UNSUBSCRIBE_URL%%") {
		t.Errorf("preview is missing the unsubscribe sentinel")
	}
}

// TestRenderPreviewUnknownBlockForLibrary asserts a block the named library does
// not have is a 422, e.g. hot_take in the member library.
func TestRenderPreviewUnknownBlockForLibrary(t *testing.T) {
	h := &Handler{}

	body := `{"body_layout":{"template_key":"aaif-member","blocks":[{"block_type":"hot_take"}]}}`
	req := httptest.NewRequest(http.MethodPost, "/projects/p/newsletters/render-preview", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.RenderPreview(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422. body=%s", w.Code, w.Body.String())
	}
	var payload errorPayload
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Error != "unprocessable_entity" {
		t.Errorf("error code = %q, want unprocessable_entity", payload.Error)
	}
}
