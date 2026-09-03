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

// ErrUnrenderableLayout marks a render failure attributable to the CLIENT's
// layout — an unknown wrapper key or block_type, or content the emitter cannot
// compile (e.g. richtext that yields invalid MJML). Callers map it to 422. A
// render failure NOT wrapped with it — an embedded template that will not parse
// — is a packaging defect and stays untyped so callers surface it as a 500,
// keeping the "present-but-broken library" classification real rather than
// collapsing every render failure to a client 422.
var ErrUnrenderableLayout = errors.New("declarative: layout cannot be rendered")

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

// RenderTemplateKey is the sole embedded template library and the render BLOCK
// SUPERSET: its block set contains every block type the editor can offer
// (TestRenderSupersetInvariant pins that every library's blocks exist here). The
// block composer is an AAIF-only pilot, so this is the one library layouts render
// from. It also backs the empty-key fallback (see loadFallbackTemplates) so a
// layout saved without an explicit template_key still resolves every block and
// renders the same chrome. A layout WITH a template_key renders from that library
// (here, the same one); an unknown key surfaces ErrTemplateNotFound → 422.
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
	// template_key is client-controlled and must name a DIRECT child template set
	// (a single path segment). Without this guard a key with a separator or a dot
	// segment — e.g. "aaif-user-community/blocks" — Stat-matches a nested embedded
	// directory, then fails loadFromFS on the missing wrappers/ dir and gets
	// misclassified as a 500 deployment defect. Treat any non-single-segment key
	// as an unknown library (→ 422), the same as a key that names nothing.
	if key == "" || key == "." || key == ".." || strings.ContainsAny(key, `/\`) {
		return Templates{}, fmt.Errorf("%w: %q", ErrTemplateNotFound, key)
	}
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

// fallbackTemplates memoizes the composed empty-key fallback set (built once;
// the embedded FS never changes at runtime).
var (
	fallbackTemplatesOnce sync.Once
	fallbackTemplatesVal  Templates
	fallbackTemplatesErr  error
)

// loadFallbackTemplates returns the template set used when a layout omits
// template_key: the sole RenderTemplateKey library (wrapper + superset blocks).
// The block composer is an AAIF-only pilot that always sends this template_key,
// so a keyless layout is only reached defensively (e.g. a layout saved before
// per-newsletter selection); it renders the same library rather than failing.
func loadFallbackTemplates() (Templates, error) {
	fallbackTemplatesOnce.Do(func() {
		fallbackTemplatesVal, fallbackTemplatesErr = LoadEmbeddedTemplate(RenderTemplateKey)
	})
	return fallbackTemplatesVal, fallbackTemplatesErr
}

// LoadEmbeddedTemplateCached returns the parsed template set for a library key,
// memoizing successful parses process-wide. An empty (or whitespace) key falls
// back to the sole RenderTemplateKey set (loadFallbackTemplates) so a layout
// without an explicit library still resolves every block. A not-found key
// surfaces ErrTemplateNotFound.
func LoadEmbeddedTemplateCached(key string) (Templates, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return loadFallbackTemplates()
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
