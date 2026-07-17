// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
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

	html, err := declarative.Render(r.Context(), toEmitterLayout(body.BodyLayout), templates, body.WrapperContent)
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
