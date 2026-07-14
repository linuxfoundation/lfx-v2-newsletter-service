// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"context"
	"strings"
	"testing"
)

// loadTestTemplates loads the EMBEDDED production templates (the same set the
// binary ships) so render tests can never drift from what is actually emitted.
func loadTestTemplates(t *testing.T) Templates {
	t.Helper()
	tmpl, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	return tmpl
}

func TestLoadEmbedded(t *testing.T) {
	tmpl := loadTestTemplates(t)
	if _, ok := tmpl.Wrappers["default"]; !ok {
		t.Fatalf("expected default wrapper to load")
	}
	// blocks/ and bricks/ unify into one map keyed by filename.
	for _, key := range []string{"intro_paragraph", "hot_take", "hidden_gems", "mlops_community", "blog_post"} {
		if _, ok := tmpl.Blocks[key]; !ok {
			t.Errorf("expected block template %q to load", key)
		}
	}
}

func TestRenderMJML_Directives(t *testing.T) {
	tmpl := loadTestTemplates(t)

	layout := Layout{
		Blocks: []Block{
			// richtext field.
			{
				BlockType: "intro_paragraph",
				Content: map[string]any{
					"text": `<p>Welcome to <strong>this week's</strong> edition.</p>`,
				},
			},
			// each= loop with a richtext field per item, plus an empty if=
			// (no title) so the title heading is dropped.
			{
				BlockType: "hidden_gems",
				Content: map[string]any{
					"gems": []any{
						map[string]any{
							"link_text":   "Vector DB deep dive",
							"link_url":    "https://example.com/vectordb",
							"description": "<p>A great read.</p>",
						},
						map[string]any{
							"link_text":   "Eval harnesses",
							"link_url":    "https://example.com/evals",
							"description": "<p>Another one.</p>",
						},
					},
				},
			},
			// slot block with nested child blocks (blog_post bricks).
			{
				BlockType: "mlops_community",
				Content:   map[string]any{},
				Blocks: []Block{
					{
						BlockType: "blog_post",
						Content: map[string]any{
							"title":       "Scaling inference",
							"description": "<p>How we did it.</p>",
							"blog_link":   "https://example.com/blog",
							"link_text":   "Read the blog",
						},
					},
				},
			},
			// hot_take with a present title (if= true) but no poll options
			// (if= false → poll section dropped).
			{
				BlockType: "hot_take",
				Content: map[string]any{
					"title": "Is RAG dead?",
					"body":  "<p>Probably not.</p>",
				},
			},
		},
	}

	doc, err := RenderMJML(layout, tmpl, nil)
	if err != nil {
		t.Fatalf("RenderMJML: %v", err)
	}

	mustContain := []string{
		"<mjml>",
		"<mj-body>",
		"Welcome to",                   // richtext (intro)
		"<strong>this week's</strong>", // richtext HTML emitted verbatim (unescaped)
		"Vector DB deep dive",          // each item 1
		"Eval harnesses",               // each item 2
		`href="https://example.com/vectordb"`,
		"Scaling inference", // nested child block via slot
		"How we did it.",    // nested child richtext
		"Is RAG dead?",      // hot_take present title
	}
	for _, s := range mustContain {
		if !strings.Contains(doc, s) {
			t.Errorf("expected MJML to contain %q\n---\n%s", s, doc)
		}
	}

	// Empty if= must drop content: hidden_gems has no title, so "HIDDEN GEMS"
	// eyebrow is present but the title heading binding {{title}} must not leak
	// an empty heading with the placeholder. hot_take has no poll options, so
	// neither poll label should appear.
	mustNotContain := []string{
		"{{title}}",                    // unresolved binding leaked
		"poll_option",                  // raw directive field name leaked
		"HOT TAKE</mj-text><mj-button", // poll button rendered despite empty if
	}
	for _, s := range mustNotContain {
		if strings.Contains(doc, s) {
			t.Errorf("expected MJML NOT to contain %q\n---\n%s", s, doc)
		}
	}
}

func TestRender_CompilesToTableHTML(t *testing.T) {
	tmpl := loadTestTemplates(t)

	layout := Layout{
		Blocks: []Block{
			{
				BlockType: "hot_take",
				Content: map[string]any{
					"title":               "Is RAG dead?",
					"body":                "<p>Probably not.</p>",
					"poll_option_1_label": "Yes",
					"poll_option_1_link":  "https://example.com/yes",
					"poll_option_2_label": "No",
					"poll_option_2_link":  "https://example.com/no",
				},
			},
		},
	}

	out, err := Render(context.Background(), layout, tmpl, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out == "" {
		t.Fatal("expected non-empty HTML output")
	}
	if !strings.Contains(out, "<table") {
		t.Errorf("expected table-based email HTML, got:\n%s", out)
	}
	// Bound text and a poll link href must survive compilation.
	if !strings.Contains(out, "Is RAG dead?") {
		t.Error("expected bound title in compiled HTML")
	}
	if !strings.Contains(out, "https://example.com/yes") {
		t.Errorf("expected poll link href in compiled HTML")
	}
}

// TestBindAttrs_URLSchemeGate asserts that bound href/src values are gated by
// scheme: unsafe schemes (javascript:) and empty/relative values are dropped,
// http/https/mailto are kept, and deferred %%…%% send-time sentinels pass through.
func TestBindAttrs_URLSchemeGate(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string // "" means the href is expected to be dropped
	}{
		{"https kept", "https://example.com/x", "https://example.com/x"},
		{"http kept", "http://example.com", "http://example.com"},
		{"mailto kept", "mailto:a@example.com", "mailto:a@example.com"},
		{"javascript dropped", "javascript:alert(1)", ""},
		{"data dropped", "data:text/html,<script>1</script>", ""},
		{"relative dropped", "/relative/path", ""},
		{"empty dropped", "", ""},
		{"deferred sentinel kept", "%%UNSUBSCRIBE_URL%%", "%%UNSUBSCRIBE_URL%%"},
		{"partial %% is not a sentinel", "javascript:alert(1)%%", ""},
		{"unsafe with trailing sentinel dropped", "javascript:alert(1)%%X%%", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bindAttrs(map[string]string{"href": "{{link}}"}, map[string]any{"link": tc.value})
			if got["href"] != tc.want {
				t.Errorf("href = %q, want %q", got["href"], tc.want)
			}
		})
	}
}

// TestRenderMJML_PollColumnsSideBySide asserts that hot_take's poll — a nested
// Row of two Columns inside a block — breaks out into its own mj-section with
// two side-by-side mj-columns (one button each) rather than stacking both
// buttons in a single column.
func TestRenderMJML_PollColumnsSideBySide(t *testing.T) {
	tmpl := loadTestTemplates(t)

	layout := Layout{
		Blocks: []Block{
			{
				BlockType: "hot_take",
				Content: map[string]any{
					"title":               "Is RAG dead?",
					"body":                "<p>Probably not.</p>",
					"poll_option_1_label": "Yes",
					"poll_option_1_link":  "https://example.com/yes",
					"poll_option_2_label": "No",
					"poll_option_2_link":  "https://example.com/no",
				},
			},
		},
	}

	doc, err := RenderMJML(layout, tmpl, nil)
	if err != nil {
		t.Fatalf("RenderMJML: %v", err)
	}

	// The mj-section that carries the first poll button must also carry the
	// second one, and must contain exactly two mj-columns (one button each) —
	// the side-by-side shape. If the buttons had stacked, both hrefs would land
	// in a single mj-column instead.
	sec := sectionContaining(doc, "https://example.com/yes")
	if sec == "" {
		t.Fatalf("could not isolate poll mj-section\n---\n%s", doc)
	}
	if !strings.Contains(sec, "https://example.com/no") {
		t.Errorf("expected both poll buttons in the same mj-section (side-by-side), got:\n%s", sec)
	}
	if n := strings.Count(sec, "<mj-column"); n != 2 {
		t.Errorf("expected poll mj-section to hold exactly 2 mj-columns (side-by-side), got %d:\n%s", n, sec)
	}
	if n := strings.Count(sec, "<mj-button"); n != 2 {
		t.Errorf("expected poll mj-section to hold 2 mj-buttons, got %d:\n%s", n, sec)
	}

	// And the compiled HTML must use MJML's two-column (50% each) layout, which
	// is what renders the buttons side-by-side; a stacked single column would
	// be mj-column-per-100.
	out, err := Render(context.Background(), layout, tmpl, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out, "mj-column-per-50") {
		t.Errorf("expected two-column (mj-column-per-50) side-by-side layout in compiled HTML")
	}
}

// sectionContaining returns the <mj-section>…</mj-section> substring that holds
// the given needle, or "" if none does. It is test-only and assumes the
// translator never nests mj-section (which MJML forbids).
func sectionContaining(doc, needle string) string {
	idx := strings.Index(doc, needle)
	if idx < 0 {
		return ""
	}
	start := strings.LastIndex(doc[:idx], "<mj-section")
	if start < 0 {
		return ""
	}
	endTag := "</mj-section>"
	end := strings.Index(doc[start:], endTag)
	if end < 0 {
		return ""
	}
	return doc[start : start+end+len(endTag)]
}

func TestRender_EmptyIfOmitsContent(t *testing.T) {
	tmpl := loadTestTemplates(t)

	layout := Layout{
		Blocks: []Block{
			{
				BlockType: "hot_take",
				Content: map[string]any{
					"title": "Present title",
					"body":  "<p>Body.</p>",
					// No poll options → poll Section if= is false.
				},
			},
		},
	}

	out, err := Render(context.Background(), layout, tmpl, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(out, "Vote option") {
		t.Errorf("empty-if poll content should be absent")
	}
	// The poll links must be absent because the poll Section did not render.
	if strings.Contains(out, "poll_option") {
		t.Errorf("unresolved poll field leaked: %s", out)
	}
}

// TestRender_WrapperEditionFields verifies that runtime wrapper content
// (edition.*) supplied to Render binds the wrapper's {{edition.x}} fields and
// satisfies their if= guards: a supplied edition.date renders its text and a
// supplied edition.unsubscribe_url renders the Unsubscribe link, while omitted
// fields leave their if=-guarded chrome rows absent.
func TestRender_WrapperEditionFields(t *testing.T) {
	tmpl := loadTestTemplates(t)

	layout := Layout{
		Blocks: []Block{
			{
				BlockType: "intro_paragraph",
				Content:   map[string]any{"text": "<p>Body.</p>"},
			},
		},
	}

	wrapperContent := map[string]any{
		"edition": map[string]any{
			"date":            "January 1, 2026",
			"unsubscribe_url": "https://example.com/unsub",
			// view_online_link and manage_subscriptions_url omitted on purpose.
		},
	}

	out, err := Render(context.Background(), layout, tmpl, wrapperContent)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// Supplied fields render.
	if !strings.Contains(out, "January 1, 2026") {
		t.Errorf("expected edition.date text in output\n%s", out)
	}
	if !strings.Contains(out, "https://example.com/unsub") {
		t.Errorf("expected unsubscribe link href in output")
	}

	// Omitted edition.view_online_link → the "View Online" link is absent.
	if strings.Contains(out, "View Online") {
		t.Errorf("expected View Online row to be absent when edition.view_online_link omitted")
	}

	// When no wrapperContent is supplied, the guarded rows drop entirely.
	bare, err := Render(context.Background(), layout, tmpl, nil)
	if err != nil {
		t.Fatalf("Render(nil): %v", err)
	}
	if strings.Contains(bare, "January 1, 2026") {
		t.Errorf("date should be absent without wrapperContent")
	}
	if strings.Contains(bare, "Unsubscribe") {
		t.Errorf("unsubscribe row should be absent without wrapperContent")
	}
}

// TestBindAttrs_SingleEscape pins the fix for double-escaped attribute values:
// bindAttrs keeps bound values RAW and serialization (writeAttr) escapes
// exactly once, so a URL with query parameters ships as &amp; in the HTML, not
// &amp;amp; (which delivered links with a broken amp;-prefixed parameter).
func TestBindAttrs_SingleEscape(t *testing.T) {
	url := "https://example.com/?a=1&b=2"
	got := bindAttrs(map[string]string{"href": "{{link}}"}, map[string]any{"link": url})
	if got["href"] != url {
		t.Fatalf("bindAttrs must keep the value raw; got %q", got["href"])
	}

	var b strings.Builder
	writeAttr(&b, "href", got["href"])
	out := b.String()
	if !strings.Contains(out, "a=1&amp;b=2") {
		t.Errorf("serialized attr must escape once: %q", out)
	}
	if strings.Contains(out, "&amp;amp;") {
		t.Errorf("serialized attr is double-escaped: %q", out)
	}
}
