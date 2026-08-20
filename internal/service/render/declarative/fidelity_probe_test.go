// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// sampleContentFromSchema builds a representative content map for a block's
// SCHEMA so a render test exercises every field branch (text/richtext/number/
// image scalars and one array item with its nested fields filled). Slot fields
// are skipped — child blocks are supplied separately.
func sampleContentFromSchema(t *testing.T, schema json.RawMessage) map[string]any {
	t.Helper()
	var fields map[string]map[string]any
	if err := json.Unmarshal(schema, &fields); err != nil {
		t.Fatalf("schema unmarshal: %v", err)
	}
	return sampleFromFields(fields)
}

func sampleFromFields(fields map[string]map[string]any) map[string]any {
	out := map[string]any{}
	for name, def := range fields {
		switch def["type"] {
		case "text", "textarea":
			out[name] = "Sample " + name
		case "richtext":
			out[name] = "<p>Sample " + name + "</p>"
		case "number":
			out[name] = float64(1)
		case "image":
			out[name] = "https://example.com/image.png"
		case "array":
			nested, _ := def["fields"].(map[string]any)
			nestedTyped := map[string]map[string]any{}
			for k, v := range nested {
				if m, ok := v.(map[string]any); ok {
					nestedTyped[k] = m
				}
			}
			out[name] = []any{sampleFromFields(nestedTyped)}
		case "slot":
			// Child blocks are supplied by the caller, not via content.
		}
	}
	return out
}

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

// TestRender_ClassStylesReachCompiledHTML pins the class mapping: the semantic
// authoring classes (card, eyebrow, body) carry canonical styles into the
// compiled email. The sole embedded set (aaif-user-community) is fully
// inline-styled (Style C) and authors no mapped classes, so this exercises the
// class mapping (applyClassStyles, still applied to every render) through an
// inline probe template: card on the Section, eyebrow on a heading Text.
func TestRender_ClassStylesReachCompiledHTML(t *testing.T) {
	templates := Templates{
		Wrappers: map[string]string{"default": `<slot name="body" />`},
		Blocks: map[string]string{
			"probe": `<Section class="card"><Text class="eyebrow">SECTION</Text><Text class="body"><p>body</p></Text></Section>`,
		},
	}
	layout := Layout{WrapperKey: "default", Blocks: []Block{{BlockType: "probe"}}}
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

// TestRender_AllAAIFBlocksCompile renders every block the AAIF manifest offers
// through the full MJML pipeline, both with representative content and empty,
// asserting each compiles. It is the regression guard for the ported Style C
// set: a future edit (or an MJML-mapping change) that makes any block emit
// invalid MJML would otherwise ship undetected and surface as a runtime 422 the
// first time a writer used that block.
func TestRender_AllAAIFBlocksCompile(t *testing.T) {
	templates, err := LoadEmbeddedTemplate(RenderTemplateKey)
	if err != nil {
		t.Fatal(err)
	}
	m, err := BuildEmbeddedManifest(RenderTemplateKey)
	if err != nil {
		t.Fatal(err)
	}
	wc := map[string]any{"edition": map[string]any{}}
	for _, entry := range m.Blocks {
		entry := entry
		t.Run(entry.BlockType, func(t *testing.T) {
			// Populated: a slot block also gets one child so its <slot> renders.
			block := Block{BlockType: entry.BlockType, Content: sampleContentFromSchema(t, entry.Schema)}
			if entry.IsContainer {
				block.Blocks = []Block{{BlockType: "blog_post", Content: map[string]any{"title": "Child"}}}
			}
			if _, err := Render(context.Background(), Layout{WrapperKey: "default", Blocks: []Block{block}}, templates, wc); err != nil {
				t.Errorf("populated render failed: %v", err)
			}
			// Empty: no content at all must still compile.
			if _, err := Render(context.Background(), Layout{WrapperKey: "default", Blocks: []Block{{BlockType: entry.BlockType}}}, templates, wc); err != nil {
				t.Errorf("empty render failed: %v", err)
			}
		})
	}
}

// TestRender_ComplianceLinksSuppressClickTracking pins that the layout path's
// compliance-footer opt-out links carry clicktracking="off". Layout newsletters
// bypass email_chrome on send, so this wrapper is the only place the attribute
// can be set; without it SendGrid would rewrite and count opt-out/My Newsletters
// clicks toward click_rate. htmlAttrs emits attributes in sorted order, so
// clicktracking lands before href — the order SendGrid requires to honor it.
func TestRender_ComplianceLinksSuppressClickTracking(t *testing.T) {
	templates, err := LoadEmbeddedTemplate(RenderTemplateKey)
	if err != nil {
		t.Fatal(err)
	}
	layout := Layout{Blocks: []Block{{BlockType: "intro_paragraph", Content: map[string]any{"text": "<p>Body.</p>"}}}}
	wc := map[string]any{"edition": map[string]any{
		"unsubscribe_url":          "https://example.com/unsub",
		"manage_subscriptions_url": "https://example.com/my",
	}}
	doc, err := RenderMJML(layout, templates, wc)
	if err != nil {
		t.Fatalf("RenderMJML: %v", err)
	}
	for _, want := range []string{
		`clicktracking="off" href="https://example.com/unsub"`,
		`clicktracking="off" href="https://example.com/my"`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("compliance link missing suppressed click-tracking %q:\n%s", want, doc)
		}
	}
}
