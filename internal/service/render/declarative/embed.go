// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

// ErrTemplateNotFound is returned when a template-set key names no embedded
// library. It lets callers distinguish a client-selected unknown key (a 422
// unrenderable layout) from a library that IS embedded but fails to parse (a
// 500 deployment defect) — collapsing the two would mask a real deploy failure.
var ErrTemplateNotFound = errors.New("declarative: embedded template not found")

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

// RenderTemplateKey is the DEFAULT render library: the set the render paths
// (render-on-write and render-preview) use when a layout carries no
// template_key. It is the superset template — every block type the editor can
// offer across all manifests — so a layout composed without an explicit library
// (or saved before per-newsletter selection) renders regardless of which
// manifest the editor loaded. A layout WITH a template_key renders from that
// library instead (see loadRenderTemplates).
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
		// Missing directory: the key names no embedded library. Wrap the sentinel
		// so callers can map it to 422 (vs a parse failure of a present library,
		// which stays an untyped 500 deployment defect).
		return Templates{}, fmt.Errorf("%w: %q", ErrTemplateNotFound, key)
	}
	return loadFromFS(embeddedTemplates, root)
}

// renderTemplateCache memoizes parsed template sets by key. Only successful
// parses are cached, so a stream of unknown client keys can't grow the map
// unbounded. Parsing happens OUTSIDE the lock (a cold parse of one library must
// not block renders of others, and cache hits take the lock only for a map
// read); a concurrent duplicate parse of the same key is harmless since parsing
// is deterministic and idempotent.
var (
	renderTemplateCacheMu sync.Mutex
	renderTemplateCache   = map[string]Templates{}
)

// LoadEmbeddedTemplateCached returns the parsed template set for a library key,
// memoizing successful parses process-wide. An empty (or whitespace) key falls
// back to RenderTemplateKey — the default/superset library — so layouts without
// an explicit library still render. A not-found key surfaces ErrTemplateNotFound.
func LoadEmbeddedTemplateCached(key string) (Templates, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		key = RenderTemplateKey
	}

	renderTemplateCacheMu.Lock()
	cached, ok := renderTemplateCache[key]
	renderTemplateCacheMu.Unlock()
	if ok {
		return cached, nil
	}

	t, err := LoadEmbeddedTemplate(key)
	if err != nil {
		return Templates{}, err
	}

	renderTemplateCacheMu.Lock()
	renderTemplateCache[key] = t
	renderTemplateCacheMu.Unlock()
	return t, nil
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
