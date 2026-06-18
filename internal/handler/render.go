// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service/render/declarative"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// embeddedTemplates holds the declarative templates baked into the binary,
// loaded once on first render-preview request. Parsing the embedded FS is
// deterministic and side-effect free, so a process-wide singleton is safe and
// avoids re-parsing on every request.
var (
	embeddedTemplatesOnce sync.Once
	embeddedTemplates     declarative.Templates
	embeddedTemplatesErr  error
)

// loadRenderTemplates returns the process-wide embedded Templates, parsing them
// on first use. A parse failure is a deployment defect (the templates ship with
// the binary), so it surfaces as a 500 rather than a client error.
func loadRenderTemplates() (declarative.Templates, error) {
	embeddedTemplatesOnce.Do(func() {
		embeddedTemplates, embeddedTemplatesErr = declarative.LoadEmbedded()
	})
	return embeddedTemplates, embeddedTemplatesErr
}

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

	templates, err := loadRenderTemplates()
	if err != nil {
		// Template load failure is a server-side defect, not a client error.
		writeError(r.Context(), w, fmt.Errorf("load render templates: %w", err))
		return
	}

	html, err := declarative.Render(toEmitterLayout(body.BodyLayout), templates, body.WrapperContent)
	if err != nil {
		// The layout parsed but could not be rendered — 422. Wrap the emitter
		// error so the message reaches the client while the status maps via the
		// domain sentinel.
		writeError(r.Context(), w, fmt.Errorf("%w: %v", domain.ErrUnprocessable, err))
		return
	}

	writeJSON(r.Context(), w, http.StatusOK, publicapi.RenderPreviewResponse{BodyHTML: html})
}

// toEmitterLayout converts the public-API layout DTO into the emitter's
// internal Layout, keeping the internal type out of the public contract.
func toEmitterLayout(l publicapi.NewsletterLayout) declarative.Layout {
	return declarative.Layout{
		WrapperKey: l.WrapperKey,
		Blocks:     toEmitterBlocks(l.Blocks),
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
