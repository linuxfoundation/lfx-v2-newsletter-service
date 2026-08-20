// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
)

func TestEmbeddedTemplateKeys(t *testing.T) {
	keys, err := EmbeddedTemplateKeys()
	if err != nil {
		t.Fatalf("EmbeddedTemplateKeys: %v", err)
	}
	want := []string{"aaif-user-community"}
	if len(keys) != len(want) {
		t.Fatalf("expected %d template keys, got %d (%v)", len(want), len(keys), keys)
	}
	if !sort.StringsAreSorted(keys) {
		t.Errorf("expected sorted keys, got %v", keys)
	}
	for i, k := range want {
		if keys[i] != k {
			t.Errorf("keys[%d] = %q, want %q", i, keys[i], k)
		}
	}
}

func TestLoadEmbeddedTemplate_UnknownKey(t *testing.T) {
	_, err := LoadEmbeddedTemplate("nope")
	if err == nil {
		t.Fatalf("expected error for unknown template key")
	}
	// The not-found sentinel is what lets render callers map a truly-unknown key
	// to 422 while a present-but-broken library stays a 500 deployment defect.
	if !errors.Is(err, ErrTemplateNotFound) {
		t.Errorf("expected ErrTemplateNotFound for a missing key, got %v", err)
	}
	if _, err := BuildEmbeddedManifest("nope"); err == nil {
		t.Fatalf("expected error for unknown manifest key")
	}
}

func TestLoadEmbeddedTemplateCached(t *testing.T) {
	// A real key parses, and repeated calls return the cached set.
	first, err := LoadEmbeddedTemplateCached(RenderTemplateKey)
	if err != nil {
		t.Fatalf("LoadEmbeddedTemplateCached(%s): %v", RenderTemplateKey, err)
	}
	if _, err := LoadEmbeddedTemplateCached(RenderTemplateKey); err != nil {
		t.Fatalf("second cached load: %v", err)
	}

	// An empty (or whitespace) key falls back to the sole RenderTemplateKey set:
	// same block coverage and same wrapper as loading that key explicitly.
	fallback, err := LoadEmbeddedTemplateCached("   ")
	if err != nil {
		t.Fatalf("empty-key fallback: %v", err)
	}
	if len(fallback.Blocks) != len(first.Blocks) {
		t.Errorf("empty-key fallback lost block coverage: %d vs %d blocks", len(fallback.Blocks), len(first.Blocks))
	}
	if fallback.Wrappers["default"] != first.Wrappers["default"] {
		t.Errorf("empty-key fallback did not use the %q wrapper", RenderTemplateKey)
	}

	// An unknown key surfaces the not-found sentinel (→ 422 at the callers).
	if _, err := LoadEmbeddedTemplateCached("no-such-library"); !errors.Is(err, ErrTemplateNotFound) {
		t.Errorf("expected ErrTemplateNotFound for unknown key, got %v", err)
	}
}

// TestLoadEmbeddedTemplate_NonSingleSegmentKeyIs422 pins that a client-controlled
// template_key with a path separator or dot segment is an unknown library
// (ErrTemplateNotFound → 422), not a 500 defect. Without the guard, a key like
// "aaif-user-community/blocks" Stat-matches a nested embedded directory and then
// fails deeper on a missing wrappers/ dir, misclassifying as a deployment defect.
func TestLoadEmbeddedTemplate_NonSingleSegmentKeyIs422(t *testing.T) {
	for _, key := range []string{"aaif-user-community/blocks", "../secret", "a/b", ".", ".."} {
		if _, err := LoadEmbeddedTemplate(key); !errors.Is(err, ErrTemplateNotFound) {
			t.Errorf("LoadEmbeddedTemplate(%q) err = %v, want ErrTemplateNotFound", key, err)
		}
		if _, err := LoadEmbeddedTemplateCached(key); !errors.Is(err, ErrTemplateNotFound) {
			t.Errorf("LoadEmbeddedTemplateCached(%q) err = %v, want ErrTemplateNotFound", key, err)
		}
	}
}

// TestEmptyKeyFallbackRendersSupersetSet proves a layout that omits template_key
// falls back to the sole RenderTemplateKey set: it resolves every block (here a
// superset block) and renders the wrapper's compliant unsubscribe row. The block
// composer always sends the key, so this is the defensive keyless path.
func TestEmptyKeyFallbackRendersSupersetSet(t *testing.T) {
	tmpl, err := LoadEmbeddedTemplateCached("") // empty template_key → fallback
	if err != nil {
		t.Fatalf("empty-key fallback load: %v", err)
	}

	if _, ok := tmpl.Blocks["sponsored_ad"]; !ok {
		t.Fatalf("fallback set missing block sponsored_ad")
	}

	layout := Layout{
		Blocks: []Block{
			{BlockType: "sponsored_ad", Content: map[string]any{"sponsor_name": "Acme", "headline": "Hello"}},
		},
	}
	wrapperContent := map[string]any{
		"edition": map[string]any{"unsubscribe_url": "https://example.com/unsub"},
	}
	out, err := Render(context.Background(), layout, tmpl, wrapperContent)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if !strings.Contains(out, "Acme") {
		t.Errorf("block sponsored_ad did not render\n%s", out)
	}
	if !strings.Contains(out, "https://example.com/unsub") {
		t.Errorf("fallback wrapper dropped the unsubscribe row")
	}
}

func TestBuildEmbeddedManifest_AAIF(t *testing.T) {
	m, err := BuildEmbeddedManifest("aaif-user-community")
	if err != nil {
		t.Fatalf("BuildEmbeddedManifest: %v", err)
	}
	if m.WrapperKey != "default" {
		t.Errorf("wrapper_key = %q, want default", m.WrapperKey)
	}
	if m.Wrapper == "" {
		t.Errorf("expected a non-empty wrapper body")
	}

	byType := map[string]ManifestEntry{}
	for _, b := range m.Blocks {
		byType[b.BlockType] = b
	}

	// The full palette: 23 blocks + 4 bricks in one namespace (the Style C
	// AAIF User Community set plus the shared default blocks kept here to
	// satisfy the render superset invariant).
	if len(m.Blocks) != 27 {
		t.Errorf("expected 27 palette entries, got %d", len(m.Blocks))
	}

	// Deterministic ordering by block_type.
	if !sort.SliceIsSorted(m.Blocks, func(i, j int) bool { return m.Blocks[i].BlockType < m.Blocks[j].BlockType }) {
		t.Errorf("expected blocks sorted by block_type")
	}

	// Container detection: mlops_community declares a slot field.
	if !byType["mlops_community"].IsContainer {
		t.Errorf("expected mlops_community to be a container")
	}
	if byType["intro_paragraph"].IsContainer {
		t.Errorf("did not expect intro_paragraph to be a container")
	}

	// The platform logo_header block: navigation category, empty defaults
	// (no tenant-specific assets baked into a shared palette).
	lh, ok := byType["logo_header"]
	if !ok {
		t.Fatalf("expected logo_header in the palette")
	}
	if lh.Category != "navigation" {
		t.Errorf("logo_header category = %q, want navigation", lh.Category)
	}
	var lhSchema map[string]map[string]any
	if err := json.Unmarshal(lh.Schema, &lhSchema); err != nil {
		t.Fatalf("logo_header schema unmarshal: %v", err)
	}
	for _, field := range []string{"image_url", "brand_label", "link"} {
		def, present := lhSchema[field]["default"]
		if !present || def != "" {
			t.Errorf("logo_header %s default = %v, want empty string", field, def)
		}
	}

	// Bricks land in the same palette with the default category.
	if byType["blog_post"].Category != "block" {
		t.Errorf("blog_post category = %q, want block", byType["blog_post"].Category)
	}

	for _, b := range m.Blocks {
		if b.Label == "" {
			t.Errorf("%s: empty label", b.BlockType)
		}
		if strings.Contains(b.Template, "<!--") {
			t.Errorf("%s: template body still contains comments", b.BlockType)
		}
		var anySchema map[string]any
		if err := json.Unmarshal(b.Schema, &anySchema); err != nil {
			t.Errorf("%s: schema is not valid JSON: %v", b.BlockType, err)
		}
	}
	if strings.Contains(m.Wrapper, "<!--") {
		t.Errorf("wrapper body still contains comments")
	}
}

// TestRenderSupersetInvariant enforces the render guarantee for EVERY embedded
// template set: all render paths load RenderTemplateKey's set, so every block
// any manifest can offer must exist there — otherwise a newsletter composed
// from that manifest fails at render time with "block template not found".
func TestRenderSupersetInvariant(t *testing.T) {
	super, err := LoadEmbeddedTemplate(RenderTemplateKey)
	if err != nil {
		t.Fatalf("LoadEmbeddedTemplate(%s): %v", RenderTemplateKey, err)
	}
	keys, err := EmbeddedTemplateKeys()
	if err != nil {
		t.Fatalf("EmbeddedTemplateKeys: %v", err)
	}
	for _, key := range keys {
		m, err := BuildEmbeddedManifest(key)
		if err != nil {
			t.Fatalf("BuildEmbeddedManifest(%s): %v", key, err)
		}
		for _, b := range m.Blocks {
			if _, ok := super.Blocks[b.BlockType]; !ok {
				t.Errorf("template %q block %q missing from the render superset %q", key, b.BlockType, RenderTemplateKey)
			}
		}
	}
}

func TestHumanizeKey(t *testing.T) {
	cases := map[string]string{
		"job_of_week":         "Job Of Week",
		"aaif-user-community": "AAIF User Community",
		"aaif":                "AAIF",
		"default":             "Default",
	}
	for in, want := range cases {
		if got := HumanizeKey(in); got != want {
			t.Errorf("HumanizeKey(%q) = %q, want %q", in, got, want)
		}
	}
}
