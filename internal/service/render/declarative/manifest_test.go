// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

func TestEmbeddedTemplateKeys(t *testing.T) {
	keys, err := EmbeddedTemplateKeys()
	if err != nil {
		t.Fatalf("EmbeddedTemplateKeys: %v", err)
	}
	want := []string{"aaif-user-community", "default"}
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
	if _, err := LoadEmbeddedTemplate("nope"); err == nil {
		t.Fatalf("expected error for unknown template key")
	}
	if _, err := BuildEmbeddedManifest("nope"); err == nil {
		t.Fatalf("expected error for unknown manifest key")
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

	// The full palette: 14 blocks + 4 bricks in one namespace.
	if len(m.Blocks) != 18 {
		t.Errorf("expected 18 palette entries, got %d", len(m.Blocks))
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

func TestBuildEmbeddedManifest_Default(t *testing.T) {
	m, err := BuildEmbeddedManifest("default")
	if err != nil {
		t.Fatalf("BuildEmbeddedManifest: %v", err)
	}
	if len(m.Blocks) != 4 {
		t.Errorf("expected 4 palette entries in the default template, got %d", len(m.Blocks))
	}
	// Every default block must also exist in the render superset, so anything
	// composed from the default manifest is guaranteed to render.
	super, err := LoadEmbeddedTemplate(RenderTemplateKey)
	if err != nil {
		t.Fatalf("LoadEmbeddedTemplate(%s): %v", RenderTemplateKey, err)
	}
	for _, b := range m.Blocks {
		if _, ok := super.Blocks[b.BlockType]; !ok {
			t.Errorf("default block %q missing from the render superset %q", b.BlockType, RenderTemplateKey)
		}
	}
}

func TestHumanizeKey(t *testing.T) {
	cases := map[string]string{
		"job_of_week":         "Job Of Week",
		"aaif-user-community": "Aaif User Community",
		"default":             "Default",
	}
	for in, want := range cases {
		if got := HumanizeKey(in); got != want {
			t.Errorf("HumanizeKey(%q) = %q, want %q", in, got, want)
		}
	}
}
