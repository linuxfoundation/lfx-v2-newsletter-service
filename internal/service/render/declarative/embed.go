// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
)

// embeddedTemplates carries the declarative template sets baked into the
// binary at build time, one directory per template key:
//
//	templates/<key>/{wrappers,blocks,bricks}/*.html
//
// Embedding them means the emitter has its templates at runtime with no
// filesystem dependency — production never reads a templates directory off
// disk. The per-key layout is the Phase 1 "hard-coded templates" model; a
// later phase can swap this source for git- or database-backed template sets
// behind the same functions.
//
//go:embed templates
var embeddedTemplates embed.FS

// RenderTemplateKey selects which embedded template set the render paths
// (render-on-write and render-preview) use. It is the superset template —
// every block type the editor can offer across all manifests — so any
// composed layout renders regardless of which manifest the editor loaded.
// Per-newsletter template selection (a template_key on the newsletter)
// replaces this constant in a later phase.
const RenderTemplateKey = "aaif-user-community"

// EmbeddedTemplateKeys returns the sorted keys of every template set compiled
// into the binary.
func EmbeddedTemplateKeys() ([]string, error) {
	entries, err := fs.ReadDir(embeddedTemplates, "templates")
	if err != nil {
		return nil, fmt.Errorf("declarative: read embedded template keys: %w", err)
	}
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			keys = append(keys, e.Name())
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// LoadEmbeddedTemplate parses one embedded template set by key into a
// Templates value. It is the production entry point for obtaining render
// templates; LoadTemplates (filesystem) remains available for tests and local
// iteration.
func LoadEmbeddedTemplate(key string) (Templates, error) {
	root := "templates/" + key
	if _, err := fs.Stat(embeddedTemplates, root); err != nil {
		return Templates{}, fmt.Errorf("declarative: embedded template %q not found", key)
	}
	return loadFromFS(embeddedTemplates, root)
}

// BuildEmbeddedManifest builds the editor manifest for one embedded template
// set by key.
func BuildEmbeddedManifest(key string) (Manifest, error) {
	root := "templates/" + key
	if _, err := fs.Stat(embeddedTemplates, root); err != nil {
		return Manifest{}, fmt.Errorf("declarative: embedded template %q not found", key)
	}
	return buildManifestFromFS(embeddedTemplates, root)
}
