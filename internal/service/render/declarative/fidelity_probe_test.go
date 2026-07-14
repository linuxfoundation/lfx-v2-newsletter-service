// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"context"
	"strings"
	"testing"
)

// TestRender_TextStylingReachesCompiledHTML pins the fidelity fix: the
// authored text presentation (font-size, line-height, color, padding) must
// reach the compiled email instead of silently falling back to MJML defaults.
// intro_paragraph authors font-size:20px, line-height:1.5, color:#555, and
// padding:20px 15px on its Text element.
func TestRender_TextStylingReachesCompiledHTML(t *testing.T) {
	templates, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	layout := Layout{WrapperKey: "default", Blocks: []Block{{
		BlockType: "intro_paragraph",
		Content:   map[string]any{"text": "Probe content here"},
	}}}
	html, err := Render(context.Background(), layout, templates, map[string]any{"edition": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"font-size:20px", "line-height:1.5", "color:#555", "padding:20px 15px"} {
		if !strings.Contains(html, want) {
			t.Errorf("compiled HTML missing authored declaration %q", want)
		}
	}
}

// TestRender_SectionStylingReachesCompiledHTML pins the container mapping:
// allowlisted section styles (padding here, via blog_post's
// Section style="padding:0 15px") are promoted to MJML attributes rather
// than dropped.
func TestRender_SectionStylingReachesCompiledHTML(t *testing.T) {
	templates, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	layout := Layout{WrapperKey: "default", Blocks: []Block{{
		BlockType: "blog_post",
		Content:   map[string]any{"title": "T", "link_url": "https://example.com", "link_text": "Read", "description": "D"},
	}}}
	html, err := Render(context.Background(), layout, templates, map[string]any{"edition": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "padding:0 15px") {
		t.Error("compiled HTML missing the section's authored padding:0 15px")
	}
}
