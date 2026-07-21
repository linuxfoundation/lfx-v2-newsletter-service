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

// RenderTemplateKey is the render BLOCK SUPERSET: the library whose block set
// contains every block type the editor can offer across all manifests
// (TestRenderSupersetInvariant pins that every library's blocks exist here). It
// backs the empty-key fallback's BLOCKS so a layout composed without an explicit
// library (or saved before per-newsletter selection) resolves every block
// regardless of which manifest the editor loaded. A layout WITH a template_key
// renders from that library instead (see LoadEmbeddedTemplateCached).
//
// The superset library is a BRANDED one (aaif-user-community's wrapper hard-codes
// AAIF's subscription URLs), so the empty-key fallback does NOT use its wrapper:
// it pairs the superset blocks with the neutral NeutralWrapperTemplateKey wrapper
// (see loadFallbackTemplates) so a keyless layout renders project-neutral chrome,
// never AAIF branding. A layout that wants AAIF chrome must set its template_key.
const RenderTemplateKey = "aaif-user-community"

// NeutralWrapperTemplateKey is the project-neutral library whose "default"
// wrapper backs the empty-key render fallback. That wrapper carries no
// brand-specific URLs or copy (any project can render through it) while keeping
// the same compliance footer structure — the `if=`-guarded unsubscribe row and
// the send-time edition.* sentinels — as every other wrapper, so a keyless
// fallback render stays CAN-SPAM-correct.
const NeutralWrapperTemplateKey = "default"

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

// fallbackTemplates memoizes the composed empty-key fallback set (built once;
// the embedded FS never changes at runtime).
var (
	fallbackTemplatesOnce sync.Once
	fallbackTemplatesVal  Templates
	fallbackTemplatesErr  error
)

// loadFallbackTemplates returns the template set used when a layout omits
// template_key: the neutral NeutralWrapperTemplateKey wrapper paired with the
// RenderTemplateKey SUPERSET blocks. Decoupling the wrapper from the blocks is
// what lets a keyless layout render project-neutral chrome (no AAIF branding)
// while still resolving every block any manifest can offer — switching the
// fallback wholesale to the neutral library would drop the superset-only blocks
// and 422 a layout that uses them.
func loadFallbackTemplates() (Templates, error) {
	fallbackTemplatesOnce.Do(func() {
		super, err := LoadEmbeddedTemplate(RenderTemplateKey)
		if err != nil {
			fallbackTemplatesErr = fmt.Errorf("declarative: load fallback superset blocks: %w", err)
			return
		}
		neutral, err := LoadEmbeddedTemplate(NeutralWrapperTemplateKey)
		if err != nil {
			fallbackTemplatesErr = fmt.Errorf("declarative: load fallback neutral wrapper: %w", err)
			return
		}
		fallbackTemplatesVal = Templates{
			Wrappers: neutral.Wrappers,
			Blocks:   super.Blocks,
		}
	})
	return fallbackTemplatesVal, fallbackTemplatesErr
}

// LoadEmbeddedTemplateCached returns the parsed template set for a library key,
// memoizing successful parses process-wide. An empty (or whitespace) key falls
// back to the neutral-wrapper + superset-blocks set (loadFallbackTemplates) so a
// layout without an explicit library renders with project-neutral chrome yet
// still resolves every block. A not-found key surfaces ErrTemplateNotFound.
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
