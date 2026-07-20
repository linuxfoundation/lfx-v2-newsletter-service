// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service/render/declarative"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// maxRenderedHTMLBytes caps the render-preview output, mirroring the persist
// path's body_html ceiling (service.maxBodyHTMLLength, 100 KB) so a pathological
// layout can't return an unbounded document.
const maxRenderedHTMLBytes = 100_000

// RenderPreview handles POST /projects/{project_uid}/newsletters/render-preview.
//
// It is stateless: it binds the supplied layout against the embedded
// declarative templates and returns the rendered email-safe HTML. Nothing is
// persisted — this is what the editor's live email preview calls.
//
// A layout that cannot be bound or compiled (unknown block_type, malformed
// markup, mjml compile failure) is a well-formed request the renderer cannot
// process, so it returns 422 Unprocessable Entity. A template-loading failure
// is a deployment defect and surfaces as 500.
func (h *Handler) RenderPreview(w http.ResponseWriter, r *http.Request) {
	var body publicapi.RenderPreviewRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}

	templates, err := declarative.LoadEmbeddedTemplateCached(body.BodyLayout.TemplateKey)
	if err != nil {
		if errors.Is(err, declarative.ErrTemplateNotFound) {
			// An unknown client-selected library can't be rendered — 422.
			writeError(r.Context(), w, fmt.Errorf("%w: unknown template_key %q", domain.ErrUnprocessable, strings.TrimSpace(body.BodyLayout.TemplateKey)))
			return
		}
		// A present library that failed to parse is a server-side defect, not a client error.
		writeError(r.Context(), w, fmt.Errorf("load render templates: %w", err))
		return
	}

	// Build the wrapper binding context by MERGING the client's wrapper_content
	// over the SAME send-path footer defaults (service.LayoutWrapperContent).
	// The defaults supply the send-time footer sentinels — the unsubscribe /
	// sender / project rows the send path substitutes per recipient — so the
	// preview's footer structure and byte size match the email that will be
	// sent. The client's wrapper_content then supplies the non-footer bindings
	// the contract says it owns (edition.date, view_online_link, …) so a
	// client-supplied edition.date IS honored in the preview.
	//
	// render-preview is stateless (no persisted newsletter): reply_email is
	// taken from the client's wrapper_content when supplied (else the send-path
	// default), and the unsubscribe row tracks this deployment's unsubscribe
	// config (h.unsub.Enabled(), the same signal renderLayout uses). The draft's
	// persisted ed_reply_email is the only residual gap between preview and send.
	wrapperContent := buildPreviewWrapperContent(body.WrapperContent, h.unsub.Enabled())

	html, err := declarative.Render(r.Context(), toEmitterLayout(body.BodyLayout), templates, wrapperContent)
	if err != nil {
		// The layout parsed but could not be rendered — 422. Wrap the emitter
		// error so the message reaches the client while the status maps via the
		// domain sentinel.
		writeError(r.Context(), w, fmt.Errorf("%w: %v", domain.ErrUnprocessable, err))
		return
	}

	if len(html) > maxRenderedHTMLBytes {
		// Parity with the persist path's body_html cap: a pathological layout (e.g.
		// many each= items) can expand under MJML into an oversized document. Input
		// is already 1 MiB-capped by decodeJSON; this bounds the OUTPUT.
		writeError(r.Context(), w, fmt.Errorf("%w: rendered HTML exceeds %d bytes", domain.ErrUnprocessable, maxRenderedHTMLBytes))
		return
	}

	writeJSON(r.Context(), w, http.StatusOK, publicapi.RenderPreviewResponse{BodyHTML: html})
}

// footerSentinelKeys are the edition.* fields the send path substitutes per
// recipient (unsubscribe URL, sender/project name) or forces empty
// (manage_subscriptions_url — there is no preferences surface yet). A client's
// wrapper_content must NOT override these — the preview keeps the send-path
// values so its footer structure and byte size match the sent email (and it
// can't preview a working "Manage subscription" link that won't exist on send).
var footerSentinelKeys = map[string]struct{}{
	"unsubscribe_url":          {},
	"sender_name":              {},
	"project_name":             {},
	"manage_subscriptions_url": {},
}

// buildPreviewWrapperContent merges a client-supplied wrapper_content over the
// send-path footer defaults for the stateless render-preview.
//
// It starts from service.LayoutWrapperContent (the send-path defaults: footer
// sentinels plus reply_email taken from the client when supplied) and overlays
// the client's edition.* bindings on top, so client-owned fields like
// edition.date and edition.view_online_link are honored while the footer
// sentinels (unsubscribe / sender / project) and the already-applied reply_email
// are preserved. Only the edition sub-map is deep-merged; that is the only shape
// the wrapper contract defines.
func buildPreviewWrapperContent(clientWC map[string]any, unsubEnabled bool) map[string]any {
	base := service.LayoutWrapperContent(replyEmailFromWrapperContent(clientWC), unsubEnabled)

	clientEdition, ok := clientWC["edition"].(map[string]any)
	if !ok {
		return base
	}
	baseEdition, ok := base["edition"].(map[string]any)
	if !ok {
		return base
	}
	for k, v := range clientEdition {
		if _, sentinel := footerSentinelKeys[k]; sentinel {
			// Footer sentinel: keep the send-path value so preview size matches.
			continue
		}
		if k == "reply_email" {
			// Already applied via LayoutWrapperContent (trimmed); don't re-overlay.
			continue
		}
		baseEdition[k] = v
	}
	return base
}

// replyEmailFromWrapperContent best-effort extracts edition.reply_email from a
// client-supplied wrapper_content map (shape: {"edition": {"reply_email": ...}}).
// render-preview is stateless, so this is the only place the reply-to address
// can come from; anything missing or mis-typed yields "" and the preview simply
// omits the reply row (the wrapper's if= guard drops it).
func replyEmailFromWrapperContent(wc map[string]any) string {
	edition, ok := wc["edition"].(map[string]any)
	if !ok {
		return ""
	}
	replyEmail, _ := edition["reply_email"].(string)
	return replyEmail
}

// toEmitterLayoutPtr converts an optional public-API layout into an optional
// emitter Layout pointer. nil in → nil out, so the service's "layout absent"
// (legacy body_html) branch is preserved when the request omits body_layout.
func toEmitterLayoutPtr(l *publicapi.NewsletterLayout) *declarative.Layout {
	if l == nil {
		return nil
	}
	layout := toEmitterLayout(*l)
	return &layout
}

// toEmitterLayout converts the public-API layout DTO into the emitter's
// internal Layout, keeping the internal type out of the public contract.
func toEmitterLayout(l publicapi.NewsletterLayout) declarative.Layout {
	return declarative.Layout{
		WrapperKey:  l.WrapperKey,
		TemplateKey: l.TemplateKey,
		Blocks:      toEmitterBlocks(l.Blocks),
	}
}

// toEmitterBlocks recursively converts public-API blocks into emitter blocks.
func toEmitterBlocks(blocks []publicapi.LayoutBlock) []declarative.Block {
	if blocks == nil {
		return nil
	}
	out := make([]declarative.Block, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, declarative.Block{
			BlockType: b.BlockType,
			Content:   b.Content,
			Blocks:    toEmitterBlocks(b.Blocks),
		})
	}
	return out
}
