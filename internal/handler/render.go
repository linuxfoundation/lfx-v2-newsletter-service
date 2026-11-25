// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"net/http"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service/render/declarative"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// RenderPreview handles POST /projects/{project_uid}/newsletters/render-preview.
//
// It is stateless: the newsletter service binds the supplied layout against the
// embedded declarative templates and returns the rendered email-safe HTML.
// Nothing is persisted — this is what the editor's live email preview calls.
//
// Template selection, the render itself, and the derived-output size policy all
// live behind NewsletterService.RenderPreview, the SAME path create/update use,
// so failures classify identically (unknown template_key / render failure →
// 422; a present-but-unparseable library → 500; oversized output → 422). The
// handler only decodes the request, hands the deployment's unsubscribe signal in
// (the same signal the send path uses), and writes the response.
func (h *Handler) RenderPreview(w http.ResponseWriter, r *http.Request) {
	var body publicapi.RenderPreviewRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}

	layout := toEmitterLayout(body.BodyLayout)
	html, err := h.newsletter.RenderPreview(r.Context(), &layout, body.WrapperContent, h.unsub.Enabled())
	if err != nil {
		writeError(r.Context(), w, err)
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
