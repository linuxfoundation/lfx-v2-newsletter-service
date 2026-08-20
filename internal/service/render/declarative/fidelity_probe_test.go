// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"context"
	"strings"
	"testing"
)

// TestRender_TextStylingReachesCompiledHTML pins the fidelity fix: the
// authored text presentation (font-size, color) on a Text/Heading must reach
// the compiled email instead of silently falling back to MJML defaults. The
// Style C hidden_gems block authors font-size:26px on its title Heading and
// color:#3c3c3f on its body Text; the translator carries both onto the inner
// styled div of the mj-text.
func TestRender_TextStylingReachesCompiledHTML(t *testing.T) {
	templates, err := LoadEmbeddedTemplate(RenderTemplateKey)
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
	for _, want := range []string{"font-size:26px", "color:#3c3c3f"} {
		if !strings.Contains(html, want) {
			t.Errorf("compiled HTML missing authored declaration %q", want)
		}
	}
}

// TestRender_SectionStylingReachesCompiledHTML pins the container mapping:
// allowlisted section styles are promoted to MJML attributes rather than
// dropped. The Style C hidden_gems card authors background-color:#ffffff and
// border-radius:14px on its Section; both reach the compiled email.
func TestRender_SectionStylingReachesCompiledHTML(t *testing.T) {
	templates, err := LoadEmbeddedTemplate(RenderTemplateKey)
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
	for _, want := range []string{"background-color:#ffffff", "border-radius:14px"} {
		if !strings.Contains(html, want) {
			t.Errorf("compiled HTML missing the section's authored declaration %q", want)
		}
	}
}

// TestDefaultKeyWrapperIsProjectNeutral pins the tenant-leak fix for the
// NEUTRAL template set: the default key's wrapper must not carry any
// brand-specific URL (the aaif-user-community key keeps its own brand chrome
// by design, scoped to that key).
func TestDefaultKeyWrapperIsProjectNeutral(t *testing.T) {
	templates, err := LoadEmbeddedTemplate("default")
	if err != nil {
		t.Fatal(err)
	}
	layout := Layout{WrapperKey: "default", Blocks: []Block{{
		BlockType: "intro_paragraph",
		Content:   map[string]any{"text": "hello"},
	}}}
	html, err := Render(context.Background(), layout, templates, map[string]any{"edition": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "aaif.live") {
		t.Error("default-key wrapper must not carry a brand URL")
	}
	if !strings.Contains(html, "Delivered by LFX") {
		t.Error("default-key wrapper missing the compliance footer")
	}
}

// TestRender_ExecutiveDirectorWeeklyBlocks renders a miniature Executive
// Director weekly edition through the full pipeline: letter intro, section
// heading, a news item with a source link, and the signature. Pins that the
// new template set's blocks compose and render end to end (they live in the
// render superset until per-newsletter template selection ships).
func TestRender_ExecutiveDirectorWeeklyBlocks(t *testing.T) {
	templates, err := LoadEmbeddedTemplate(RenderTemplateKey)
	if err != nil {
		t.Fatal(err)
	}
	layout := Layout{WrapperKey: "default", Blocks: []Block{
		{BlockType: "letter_intro", Content: map[string]any{
			"greeting": "To the LF Team:",
			"body":     "<p>It feels like every month is a year.</p>",
			"signoff":  "LF",
		}},
		{BlockType: "section_heading", Content: map[string]any{"title": "NEWS"}},
		{BlockType: "news_items", Content: map[string]any{"items": []any{map[string]any{
			"headline":     "FINOS Launches OSERA",
			"summary":      "<p>A financial services led alliance for supply chain resiliency.</p>",
			"source_label": "Linux Foundation",
			"source_url":   "https://www.linuxfoundation.org/press/finos-osera",
		}}}},
		{BlockType: "signature", Content: map[string]any{"name": "Jim Zemlin", "title": "CEO, The Linux Foundation"}},
	}}
	html, err := Render(context.Background(), layout, templates, map[string]any{"edition": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"To the LF Team:",
		"every month is a year",
		">NEWS<",
		"FINOS Launches OSERA",
		`href="https://www.linuxfoundation.org/press/finos-osera"`,
		"Linux Foundation",
		"Jim Zemlin",
		"CEO, The Linux Foundation",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered edition missing %q", want)
		}
	}
}

// TestRender_ClassStylesReachCompiledHTML pins the class mapping: the semantic
// authoring classes (card, eyebrow, body) carry canonical styles into the
// compiled email. The AAIF User Community set is fully inline-styled (Style C),
// so this exercises the class mapping through the "default" set, whose generic
// block authors card on its Section and eyebrow on its heading Text.
func TestRender_ClassStylesReachCompiledHTML(t *testing.T) {
	templates, err := LoadEmbeddedTemplate("default")
	if err != nil {
		t.Fatal(err)
	}
	layout := Layout{WrapperKey: "default", Blocks: []Block{{
		BlockType: "generic",
		Content:   map[string]any{"heading": "SECTION", "title": "Title", "body": "<p>body</p>"},
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
// hidden_gems wraps its content in <div style="padding:22px 22px 10px">.
func TestRender_DissolvedWrapperPaddingHoisted(t *testing.T) {
	templates, err := LoadEmbeddedTemplate(RenderTemplateKey)
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
	if !strings.Contains(html, "padding:22px 22px 10px") {
		t.Error("dissolved wrapper div's padding did not reach the compiled email")
	}
}
