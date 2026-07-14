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

// TestRender_ClassStylesReachCompiledHTML pins the class mapping: the
// semantic authoring classes (card, eyebrow, body) now carry canonical
// styles into the compiled email. hidden_gems authors card on its Section,
// an eyebrow Text, and body richtext.
func TestRender_ClassStylesReachCompiledHTML(t *testing.T) {
	templates, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	layout := Layout{WrapperKey: "default", Blocks: []Block{{
		BlockType: "hidden_gems",
		Content: map[string]any{"title": "Gems", "gems": []any{map[string]any{
			"link_text": "Gem one", "link_url": "https://example.com/g", "description": "<p>desc</p>",
		}}},
	}}}
	html, err := Render(context.Background(), layout, templates, map[string]any{"edition": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"background-color:#ffffff", // card on the section
		"text-transform:uppercase", // eyebrow inner div
		"letter-spacing:1px",       // eyebrow inner div
	} {
		if !strings.Contains(html, want) {
			t.Errorf("compiled HTML missing class-derived declaration %q", want)
		}
	}
}

// TestRender_DissolvedWrapperPaddingHoisted pins the wrapper hoist: the
// <Section><div style="padding:…">…</div> authoring pattern promotes the
// dissolved div's padding onto the mj-column instead of dropping it.
// hidden_gems wraps its content in <div style="padding:15px 15px 5px">.
func TestRender_DissolvedWrapperPaddingHoisted(t *testing.T) {
	templates, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	layout := Layout{WrapperKey: "default", Blocks: []Block{{
		BlockType: "hidden_gems",
		Content: map[string]any{"title": "Gems", "gems": []any{map[string]any{
			"link_text": "Gem one", "link_url": "https://example.com/g", "description": "<p>desc</p>",
		}}},
	}}}
	html, err := Render(context.Background(), layout, templates, map[string]any{"edition": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "padding:15px 15px 5px") {
		t.Error("dissolved wrapper div's padding did not reach the compiled email")
	}
}
