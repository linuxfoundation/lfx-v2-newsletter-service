// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
)

// loadTestTemplates loads the EMBEDDED production templates (the same set the
// binary ships) so render tests can never drift from what is actually emitted.
func loadTestTemplates(t *testing.T) Templates {
	t.Helper()
	tmpl, err := LoadEmbeddedTemplate(RenderTemplateKey)
	if err != nil {
		t.Fatalf("LoadEmbeddedTemplate: %v", err)
	}
	return tmpl
}

func TestLoadEmbeddedTemplate(t *testing.T) {
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

// TestRenderMJML_LinkWrappingImageKeepsHref asserts that a block-level <Img>
// inside a <Link> (logo_header's clickable logo) survives the link-dissolve
// path with its destination intact: the link's href must be carried onto the
// emitted mj-image so the rendered logo stays clickable.
func TestRenderMJML_LinkWrappingImageKeepsHref(t *testing.T) {
	tmpl := loadTestTemplates(t)

	layout := Layout{
		Blocks: []Block{
			{
				BlockType: "logo_header",
				Content: map[string]any{
					"image_url": "https://cdn.example/logo.png",
					"link":      "https://example.com/home",
				},
			},
		},
	}

	doc, err := RenderMJML(layout, tmpl, nil)
	if err != nil {
		t.Fatalf("RenderMJML: %v", err)
	}
	// The link wrapper is dissolved to the image child, but the href must land
	// on the mj-image so the logo remains a clickable link.
	if !strings.Contains(doc, `href="https://example.com/home"`) {
		t.Errorf("expected the link href carried onto the mj-image:\n%s", doc)
	}
	if !strings.Contains(doc, "<mj-image") {
		t.Errorf("expected an mj-image for the logo:\n%s", doc)
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

// TestRenderMJML_PollBreakoutKeepsCardChrome asserts that when hot_take's poll
// Row breaks out into its own sibling mj-section, it still carries the card
// chrome of the wrappers it was lifted out of: the outer `card` class'
// background-color / border-radius and the poll wrapper div's padding. Without
// the ancestor-style carry, the poll section renders bare (no white card
// background, no radius, no padding) and visually detaches from the rest of the
// block.
func TestRenderMJML_PollBreakoutKeepsCardChrome(t *testing.T) {
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

	sec := sectionContaining(doc, "https://example.com/yes")
	if sec == "" {
		t.Fatalf("could not isolate poll mj-section\n---\n%s", doc)
	}
	// Style C hot_take card → background-color:#ffffff;border-radius:14px carried
	// onto the broken-out poll section; the poll wrapper div's padding wins over
	// the card's own padding.
	for _, want := range []string{
		`background-color="#ffffff"`,
		`border-radius="14px"`,
		`padding="0 22px 14px"`,
	} {
		if !strings.Contains(sec, want) {
			t.Errorf("poll breakout section missing carried card chrome %q:\n%s", want, sec)
		}
	}
}

// TestRenderMJML_TextStatsCardStaysOneSection asserts that a card block whose
// only breakout is a row of text columns (by_the_numbers' stats) renders as ONE
// mj-section: the eyebrow plus an mj-table of side-by-side stat cells, all
// inside the single dark card. Before the mj-table path, writeSection lifted the
// stats Row into its own sibling section, and the postprocess card gap then drew
// a visible break between the eyebrow half and the stats half — two cards where
// the design is one. Unlike hot_take's poll (buttons, which keep the split
// path), text-only stat columns are safe to collapse into a table cell.
func TestRenderMJML_TextStatsCardStaysOneSection(t *testing.T) {
	tmpl := loadTestTemplates(t)

	layout := Layout{
		Blocks: []Block{
			{
				BlockType: "by_the_numbers",
				Content: map[string]any{
					"stats": []any{
						map[string]any{"value": "110M+", "label": "downloads"},
						map[string]any{"value": "42", "label": "contributors"},
					},
				},
			},
		},
	}

	doc, err := RenderMJML(layout, tmpl, nil)
	if err != nil {
		t.Fatalf("RenderMJML: %v", err)
	}

	// Exactly one dark card section — the stats must not spill into a second one.
	if n := strings.Count(doc, `background-color="#131313"`); n != 1 {
		t.Errorf("expected exactly one dark card section, got %d:\n%s", n, doc)
	}
	// Both stats ride the same section as the eyebrow, inside an mj-table so the
	// cells sit side-by-side (the split path would have used sibling mj-columns
	// in a second section instead).
	sec := sectionContaining(doc, "110M+")
	if sec == "" {
		t.Fatalf("could not isolate stats mj-section\n---\n%s", doc)
	}
	for _, want := range []string{"BY THE NUMBERS", "42", "<mj-table"} {
		if !strings.Contains(sec, want) {
			t.Errorf("expected stats card section to contain %q:\n%s", want, sec)
		}
	}
	if strings.Contains(sec, "<mj-column") && strings.Count(sec, "<mj-column") != 1 {
		t.Errorf("expected the stats to render as table cells, not sibling mj-columns:\n%s", sec)
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
// TestRender_RespectsContextCancellation proves the mjml compile is bound by the
// caller's context: a context already cancelled before Render is called surfaces
// an error instead of compiling. This is the plumbing behind the service-owned
// mjmlCompileTimeout that bounds every render path against a pathological layout.
// TestRender_ClientLayoutFailuresAreUnrenderable pins the error classification:
// a client-driven layout failure (unknown wrapper key, unknown block_type) is
// flagged ErrUnrenderableLayout so callers map it to 422, rather than surfacing
// as an untyped error the callers would treat as a 500 packaging defect.
func TestRender_ClientLayoutFailuresAreUnrenderable(t *testing.T) {
	tmpl := loadTestTemplates(t)

	unknownBlock := Layout{Blocks: []Block{{BlockType: "no_such_block_type"}}}
	if _, err := Render(context.Background(), unknownBlock, tmpl, nil); !errors.Is(err, ErrUnrenderableLayout) {
		t.Errorf("unknown block_type err = %v, want ErrUnrenderableLayout", err)
	}

	unknownWrapper := Layout{
		WrapperKey: "no_such_wrapper",
		Blocks:     []Block{{BlockType: "intro_paragraph", Content: map[string]any{"text": "<p>Body.</p>"}}},
	}
	if _, err := Render(context.Background(), unknownWrapper, tmpl, nil); !errors.Is(err, ErrUnrenderableLayout) {
		t.Errorf("unknown wrapper key err = %v, want ErrUnrenderableLayout", err)
	}
}

// TestCheckLayoutBounds pins the EXACT block-count and nesting-depth boundaries:
// a layout at each ceiling passes, one over is rejected as ErrUnrenderableLayout.
// (A layout at maxLayoutDepth must pass — a leaf sitting at the deepest allowed
// level is legitimate; only children one level deeper breach the limit.)
func TestCheckLayoutBounds(t *testing.T) {
	leaf := Block{BlockType: "x"}

	atCount := make([]Block, maxLayoutBlocks)
	for i := range atCount {
		atCount[i] = leaf
	}
	if err := checkLayoutBounds(atCount); err != nil {
		t.Errorf("a layout at maxLayoutBlocks (%d) should pass, got %v", maxLayoutBlocks, err)
	}
	overCount := make([]Block, maxLayoutBlocks+1)
	for i := range overCount {
		overCount[i] = leaf
	}
	if err := checkLayoutBounds(overCount); !errors.Is(err, ErrUnrenderableLayout) {
		t.Errorf("a layout over maxLayoutBlocks should be rejected, got %v", err)
	}

	// chain builds a single nested chain n blocks deep, a leaf at the deepest level.
	chain := func(n int) []Block {
		b := Block{BlockType: "x"}
		for i := 1; i < n; i++ {
			b = Block{BlockType: "x", Blocks: []Block{b}}
		}
		return []Block{b}
	}
	if err := checkLayoutBounds(chain(maxLayoutDepth)); err != nil {
		t.Errorf("a chain at maxLayoutDepth (%d) should pass, got %v", maxLayoutDepth, err)
	}
	if err := checkLayoutBounds(chain(maxLayoutDepth + 1)); !errors.Is(err, ErrUnrenderableLayout) {
		t.Errorf("a chain over maxLayoutDepth should be rejected, got %v", err)
	}
}

// TestRenderMJML_RejectsOversizedLayout confirms the bound is wired into
// renderMJML (the choke point every render path funnels through), so an
// oversized layout surfaces as ErrUnrenderableLayout (→ 422) and a small
// legitimate layout is unaffected.
func TestRenderMJML_RejectsOversizedLayout(t *testing.T) {
	tmpl := loadTestTemplates(t)

	tooMany := Layout{Blocks: make([]Block, maxLayoutBlocks+1)}
	for i := range tooMany.Blocks {
		tooMany.Blocks[i] = Block{BlockType: "intro_paragraph", Content: map[string]any{"text": "<p>x</p>"}}
	}
	if _, err := RenderMJML(tooMany, tmpl, nil); !errors.Is(err, ErrUnrenderableLayout) {
		t.Errorf("oversized layout via RenderMJML err = %v, want ErrUnrenderableLayout", err)
	}

	ok := Layout{Blocks: []Block{{BlockType: "intro_paragraph", Content: map[string]any{"text": "<p>Body.</p>"}}}}
	if _, err := RenderMJML(ok, tmpl, nil); err != nil {
		t.Errorf("small valid layout should render, got %v", err)
	}
}

// TestRender_NumericBindingNoScientificNotation binds a large numeric field — as
// JSON decodes it into content, a float64 — and asserts it renders as a plain
// integer, not the "1e+06" exponent form fmt's %v produces for float64 >= 1e6.
func TestRender_NumericBindingNoScientificNotation(t *testing.T) {
	tmpl := loadTestTemplates(t)
	layout := Layout{
		Blocks: []Block{
			{BlockType: "logo_header", Content: map[string]any{"brand_label": float64(1000000)}},
		},
	}
	out, err := Render(context.Background(), layout, tmpl, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out, "1000000") {
		t.Errorf("numeric binding did not render as a plain integer:\n%s", out)
	}
	if strings.Contains(out, "1e+06") {
		t.Errorf("numeric binding rendered in scientific notation:\n%s", out)
	}
}

func TestRender_RespectsContextCancellation(t *testing.T) {
	tmpl := loadTestTemplates(t)
	layout := Layout{
		Blocks: []Block{
			{BlockType: "intro_paragraph", Content: map[string]any{"text": "<p>Body.</p>"}},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the compile runs

	if _, err := Render(ctx, layout, tmpl, nil); err == nil {
		t.Fatalf("expected Render to fail on a cancelled context")
	}
}

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

// TestRenderMJML_BlockSpacingPadding asserts that a top-level block carrying
// `_spacing_padding` is wrapped in an mj-wrapper whose padding attribute carries
// that value, so the outer spacing reaches the sent email.
func TestRenderMJML_BlockSpacingPadding(t *testing.T) {
	tmpl := loadTestTemplates(t)

	layout := Layout{
		Blocks: []Block{
			{
				BlockType: "intro_paragraph",
				Content: map[string]any{
					"text":            "<p>Body.</p>",
					spacingPaddingKey: "16px",
				},
			},
		},
	}

	doc, err := RenderMJML(layout, tmpl, nil)
	if err != nil {
		t.Fatalf("RenderMJML: %v", err)
	}
	if !strings.Contains(doc, `<mj-wrapper padding="16px">`) {
		t.Errorf("expected mj-wrapper with padding=16px\n---\n%s", doc)
	}
	// The wrapper must enclose the block's own section(s).
	if idx := strings.Index(doc, `<mj-wrapper padding="16px">`); idx >= 0 {
		inner := doc[idx:]
		if endIdx := strings.Index(inner, "</mj-wrapper>"); endIdx >= 0 {
			if !strings.Contains(inner[:endIdx], "<mj-section") {
				t.Errorf("expected the block's mj-section inside the wrapper\n---\n%s", doc)
			}
		}
	}
	// The reserved key must not leak into the output.
	if strings.Contains(doc, spacingPaddingKey) {
		t.Errorf("reserved spacing key leaked into output\n---\n%s", doc)
	}

	// It must also survive the full mjml compile.
	out, err := Render(context.Background(), layout, tmpl, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out == "" || !strings.Contains(out, "<table") {
		t.Errorf("expected compiled table HTML for a spaced block")
	}
}

// TestRenderMJML_BlockSpacingMargin asserts that a block carrying only
// `_spacing_margin` still produces a visible outer-spacing wrapper (margin is
// mapped onto the wrapper padding — see combineSpacing).
func TestRenderMJML_BlockSpacingMargin(t *testing.T) {
	tmpl := loadTestTemplates(t)

	layout := Layout{
		Blocks: []Block{
			{
				BlockType: "intro_paragraph",
				Content: map[string]any{
					"text":           "<p>Body.</p>",
					spacingMarginKey: "24px",
				},
			},
		},
	}

	doc, err := RenderMJML(layout, tmpl, nil)
	if err != nil {
		t.Fatalf("RenderMJML: %v", err)
	}
	if !strings.Contains(doc, `<mj-wrapper padding="24px">`) {
		t.Errorf("expected margin-only block to emit mj-wrapper padding=24px\n---\n%s", doc)
	}
}

// TestRenderMJML_BlockSpacingPaddingAndMargin asserts that when both keys are
// set, they are combined component-wise into a single wrapper padding (the
// composer's div stacks margin+padding into one visible gap).
func TestRenderMJML_BlockSpacingPaddingAndMargin(t *testing.T) {
	tmpl := loadTestTemplates(t)

	layout := Layout{
		Blocks: []Block{
			{
				BlockType: "intro_paragraph",
				Content: map[string]any{
					"text":            "<p>Body.</p>",
					spacingPaddingKey: "8px 16px",
					spacingMarginKey:  "4px",
				},
			},
		},
	}

	doc, err := RenderMJML(layout, tmpl, nil)
	if err != nil {
		t.Fatalf("RenderMJML: %v", err)
	}
	// 8px/16px (t/b, r/l) + 4px all sides = 12px vertical, 20px horizontal.
	if !strings.Contains(doc, `<mj-wrapper padding="12px 20px">`) {
		t.Errorf("expected combined padding=12px 20px\n---\n%s", doc)
	}
}

// TestRenderMJML_BlockSpacingZeroNoWrapper pins the "0px = no wrapper" contract:
// a block with `_spacing_padding: "0px"` and no margin must render byte-identically
// to the same block with no spacing keys at all — i.e. no mj-wrapper.
func TestRenderMJML_BlockSpacingZeroNoWrapper(t *testing.T) {
	tmpl := loadTestTemplates(t)

	spaced := Layout{
		Blocks: []Block{
			{
				BlockType: "intro_paragraph",
				Content: map[string]any{
					"text":            "<p>Body.</p>",
					spacingPaddingKey: "0px",
					spacingMarginKey:  "0px",
				},
			},
		},
	}
	bare := Layout{
		Blocks: []Block{
			{
				BlockType: "intro_paragraph",
				Content:   map[string]any{"text": "<p>Body.</p>"},
			},
		},
	}

	spacedDoc, err := RenderMJML(spaced, tmpl, nil)
	if err != nil {
		t.Fatalf("RenderMJML(spaced): %v", err)
	}
	bareDoc, err := RenderMJML(bare, tmpl, nil)
	if err != nil {
		t.Fatalf("RenderMJML(bare): %v", err)
	}
	if strings.Contains(spacedDoc, "<mj-wrapper") {
		t.Errorf("0px spacing must not emit an mj-wrapper\n---\n%s", spacedDoc)
	}
	if spacedDoc != bareDoc {
		t.Errorf("0px spacing must be byte-identical to no spacing\n--- spaced ---\n%s\n--- bare ---\n%s", spacedDoc, bareDoc)
	}
}

// TestParse_SelfClosedCustomTagKeepsSiblings pins the parser fix for
// self-closed custom tags: HTML5 parsing ignores the self-closing slash on
// unknown elements, so <richtext … /> previously stayed open and swallowed
// every following sibling. Both the richtext content and the Text sibling
// after it must render.
func TestParse_SelfClosedCustomTagKeepsSiblings(t *testing.T) {
	tpl := `<Section><richtext field="body" /><Text>after-sibling</Text></Section>`
	nodes, err := parseTemplate(tpl)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || len(nodes[0].children) != 2 {
		t.Fatalf("expected Section with 2 children (richtext + Text), got %+v", nodes)
	}
	if nodes[0].children[0].richtextField != "body" {
		t.Errorf("first child should be the richtext, got %+v", nodes[0].children[0])
	}
	if nodes[0].children[1].Tag != "text" {
		t.Errorf("the Text sibling was swallowed: %+v", nodes[0].children[1])
	}
}

// TestRender_AuthoredCardShadowSurvives pins finding #5: a card's AUTHORED
// box-shadow survives translation with its real colour, for both a bordered card
// whose shadow differs from its border and a BORDERLESS card. hit_reply authors a
// violet shadow (6px 6px 0 #7b4dff) on a black (#131313) border; by_the_numbers
// authors a blue shadow (6px 6px 0 #3d8bff) on a borderless card. Before the fix
// the shadow was synthesised from the border colour (so hit_reply rendered black)
// and borderless cards got no shadow at all.
func TestRender_AuthoredCardShadowSurvives(t *testing.T) {
	tmpl := loadTestTemplates(t)
	layout := Layout{Blocks: []Block{
		{BlockType: "hit_reply"},
		{BlockType: "by_the_numbers"},
	}}
	out, err := Render(context.Background(), layout, tmpl, map[string]any{"edition": map[string]any{}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out, "box-shadow:6px 6px 0 #7b4dff") {
		t.Errorf("hit_reply authored violet shadow did not survive:\n%s", out)
	}
	if strings.Contains(out, "box-shadow:6px 6px 0 #131313") {
		t.Errorf("a card shadow was synthesised from the black border colour instead of the authored value:\n%s", out)
	}
	if !strings.Contains(out, "box-shadow:6px 6px 0 #3d8bff") {
		t.Errorf("by_the_numbers borderless card lost its authored blue shadow:\n%s", out)
	}
}

// TestRenderMJML_SlotSeparatorBetweenChildren asserts the <slot><separator>
// renders the divider BETWEEN consecutive child blocks only — never before the
// first or after the last — so mlops_community's bricks get an inline rule that
// matches the composer canvas. One child yields no separator; three yield two.
func TestRenderMJML_SlotSeparatorBetweenChildren(t *testing.T) {
	tmpl := loadTestTemplates(t)
	child := func(title string) Block {
		return Block{BlockType: "blog_post", Content: map[string]any{"title": title, "description": "<p>d</p>", "blog_link": "https://example.com", "link_text": "read"}}
	}
	for _, tc := range []struct {
		n, wantSeparators int
	}{{1, 0}, {2, 1}, {3, 2}} {
		children := make([]Block, 0, tc.n)
		for i := 0; i < tc.n; i++ {
			children = append(children, child("brick"))
		}
		layout := Layout{Blocks: []Block{{BlockType: "mlops_community", Content: map[string]any{}, Blocks: children}}}
		out, err := RenderMJML(layout, tmpl, nil)
		if err != nil {
			t.Fatalf("n=%d: %v", tc.n, err)
		}
		if got := strings.Count(out, "border-top:1px solid #d8d8db"); got != tc.wantSeparators {
			t.Errorf("%d children: got %d separators, want %d (between-only)", tc.n, got, tc.wantSeparators)
		}
	}
}

// TestRender_SplitCardShadowIndexStaysAligned covers the box-shadow index
// desync: hot_take's poll uses buttons, so writeSection cannot keep it as one
// table-safe card and splits it into multiple radiused sibling wrappers (the
// breakout row carries the card's border-radius/margin). A shadow slice that
// counted one entry per AUTHORED card would advance the re-shadow index twice
// for hot_take, so the next card would read past the slice and lose its authored
// shadow. Assert the trailing by_the_numbers keeps its blue shadow behind a
// splitting hot_take.
func TestRender_SplitCardShadowIndexStaysAligned(t *testing.T) {
	tmpl := loadTestTemplates(t)
	layout := Layout{Blocks: []Block{
		{BlockType: "hot_take"},       // button poll → not table-safe → splits into sibling sections
		{BlockType: "by_the_numbers"}, // borderless card with a distinct blue shadow
	}}
	out, err := Render(context.Background(), layout, tmpl, map[string]any{"edition": map[string]any{}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Assert the blue shadow lands on by_the_numbers' OWN card, not just anywhere:
	// its dark (#131313) borderless wrapper must carry box-shadow #3d8bff in the
	// same style attribute. On a desync the blue shadow would instead attach to
	// hot_take's (white) breakout wrapper, so a bare Contains(#3d8bff) would still
	// pass while by_the_numbers rendered shadowless.
	darkCardBlueShadow := regexp.MustCompile(`background-color:#131313[^"]*box-shadow:6px 6px 0 #3d8bff|box-shadow:6px 6px 0 #3d8bff[^"]*background-color:#131313`)
	if !darkCardBlueShadow.MatchString(out) {
		t.Errorf("by_the_numbers' dark card lost its authored blue shadow behind a splitting hot_take (shadow index desync):\n%s", out)
	}
}

// TestRenderMJML_ImageBorderRadiusCarried pins the authored image styling that
// the new templates rely on: MJML strict mode drops an arbitrary inline `style`
// on mj-image, so writeImage forwards the supported presentational declarations
// (border, border-radius) onto mj-image attributes. Without this, meme_of_week /
// last_weeks_take / sponsored_ad lose their intended border-radius:8px rounding.
func TestRenderMJML_ImageBorderRadiusCarried(t *testing.T) {
	tmpl := loadTestTemplates(t)

	layout := Layout{
		Blocks: []Block{
			{
				BlockType: "meme_of_week",
				Content: map[string]any{
					"image_url": "https://cdn.example/meme.png",
				},
			},
		},
	}

	doc, err := RenderMJML(layout, tmpl, nil)
	if err != nil {
		t.Fatalf("RenderMJML: %v", err)
	}
	if !strings.Contains(doc, "<mj-image") {
		t.Fatalf("expected an mj-image for the meme image:\n%s", doc)
	}
	if !strings.Contains(doc, `border-radius="8px"`) {
		t.Errorf("expected authored border-radius:8px carried onto mj-image as an attribute:\n%s", doc)
	}
}

// TestRenderMJML_PodcastSeparatorsNoDangling covers the podcast brick's optional
// platform links. All three fields (video/spotify/apple) are independent optional
// fields, so the previous per-link " · " separators produced a leading separator
// for combinations like spotify-only ("· Spotify"). The brick now uses
// separator-free spacing, so no punctuation separator may appear for any
// combination while every present link still renders.
func TestRenderMJML_PodcastSeparatorsNoDangling(t *testing.T) {
	tmpl := loadTestTemplates(t)

	cases := []struct {
		name    string
		content map[string]any
		want    []string
	}{
		{
			name:    "spotify only",
			content: map[string]any{"title": "Ep 1", "spotify_link": "https://open.spotify.com/x"},
			want:    []string{"Spotify"},
		},
		{
			name:    "apple only",
			content: map[string]any{"title": "Ep 1", "apple_link": "https://podcasts.apple.com/x"},
			want:    []string{"Apple"},
		},
		{
			name: "video and apple, no spotify",
			content: map[string]any{
				"title":      "Ep 1",
				"video_link": "https://youtu.be/x",
				"apple_link": "https://podcasts.apple.com/x",
			},
			want: []string{"Video", "Apple"},
		},
		{
			name: "all three",
			content: map[string]any{
				"title":        "Ep 1",
				"video_link":   "https://youtu.be/x",
				"spotify_link": "https://open.spotify.com/x",
				"apple_link":   "https://podcasts.apple.com/x",
			},
			want: []string{"Video", "Spotify", "Apple"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			layout := Layout{Blocks: []Block{{BlockType: "podcast", Content: tc.content}}}
			doc, err := RenderMJML(layout, tmpl, nil)
			if err != nil {
				t.Fatalf("RenderMJML: %v", err)
			}
			for _, sep := range []string{"·", "&middot;", "&#183;", "&#xB7;"} {
				if strings.Contains(doc, sep) {
					t.Errorf("expected no %q separator for %s:\n%s", sep, tc.name, doc)
				}
			}
			for _, label := range tc.want {
				if !strings.Contains(doc, ">"+label) {
					t.Errorf("expected %q link label present for %s:\n%s", label, tc.name, doc)
				}
			}
		})
	}
}
