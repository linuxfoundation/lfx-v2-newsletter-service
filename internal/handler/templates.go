// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"net/http"
	"sync"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service/render/declarative"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// templateCatalog caches the embedded template keys and their built manifests.
// Both derive entirely from the FS compiled into the binary, so building once
// per process is safe; a failure to build is a packaging defect and surfaces
// as a 500 on every request rather than being retried.
var (
	templateCatalogOnce sync.Once
	templateManifests   map[string]declarative.Manifest
	templateKeys        []string
	templateCatalogErr  error
)

func loadTemplateCatalog() ([]string, map[string]declarative.Manifest, error) {
	templateCatalogOnce.Do(func() {
		keys, err := declarative.EmbeddedTemplateKeys()
		if err != nil {
			templateCatalogErr = err
			return
		}
		manifests := make(map[string]declarative.Manifest, len(keys))
		for _, key := range keys {
			manifest, buildErr := declarative.BuildEmbeddedManifest(key)
			if buildErr != nil {
				templateCatalogErr = buildErr
				return
			}
			manifests[key] = manifest
		}
		templateKeys = keys
		templateManifests = manifests
	})
	return templateKeys, templateManifests, templateCatalogErr
}

// ListTemplates handles GET /projects/{project_uid}/newsletters/templates.
//
// It returns the keys and labels of the template sets compiled into the
// binary. The manifest for a key is fetched separately, so the list stays
// small enough for a template picker.
func (h *Handler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	keys, _, err := loadTemplateCatalog()
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	resp := publicapi.TemplatesResponse{Templates: make([]publicapi.TemplateSummary, 0, len(keys))}
	for _, key := range keys {
		resp.Templates = append(resp.Templates, publicapi.TemplateSummary{Key: key, Label: declarative.HumanizeKey(key)})
	}
	writeJSON(r.Context(), w, http.StatusOK, resp)
}

// GetTemplateManifest handles
// GET /projects/{project_uid}/newsletters/templates/{template_key}/manifest.
//
// It returns the editor manifest (palette of block types with schemas and
// renderable templates, plus the page-chrome wrapper) for one template set.
// An unknown key is a client error, not a defect.
//
// The response is the public publicapi.TemplateManifest DTO (pinned by
// docs/newsletter-service-contract.md and mirrored by NewsletterTemplateManifest
// in @lfx-one/shared), mapped from the internal renderer manifest by
// toAPITemplateManifest so the wire contract stays independent of the render type.
func (h *Handler) GetTemplateManifest(w http.ResponseWriter, r *http.Request) {
	_, manifests, err := loadTemplateCatalog()
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	key := r.PathValue("template_key")
	manifest, ok := manifests[key]
	if !ok {
		writeJSON(r.Context(), w, http.StatusNotFound, errorPayload{
			Error:   "not_found",
			Message: "unknown template key",
		})
		return
	}
	writeJSON(r.Context(), w, http.StatusOK, toAPITemplateManifest(manifest))
}

// toAPITemplateManifest maps the internal renderer manifest to the public
// TemplateManifest DTO, keeping the render type (declarative.Manifest) out of
// the wire contract. The JSON shapes are identical by design — the mapping is
// the seam that lets the two evolve independently.
func toAPITemplateManifest(m declarative.Manifest) publicapi.TemplateManifest {
	blocks := make([]publicapi.TemplateManifestBlock, 0, len(m.Blocks))
	for _, b := range m.Blocks {
		blocks = append(blocks, publicapi.TemplateManifestBlock{
			BlockType:   b.BlockType,
			Label:       b.Label,
			Category:    b.Category,
			Schema:      b.Schema,
			IsContainer: b.IsContainer,
			Template:    b.Template,
		})
	}
	return publicapi.TemplateManifest{
		WrapperKey: m.WrapperKey,
		Blocks:     blocks,
		Wrapper:    m.Wrapper,
	}
}
