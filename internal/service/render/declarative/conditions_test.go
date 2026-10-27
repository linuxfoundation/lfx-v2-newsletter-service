// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// conditionTemplates returns a template set with one test block and a minimal
// wrapper, so a directive can be tested without the production templates.
func conditionTemplates(block string) Templates {
	return Templates{
		Wrappers: map[string]string{"default": `<Section><slot name="body" /></Section>`},
		Blocks:   map[string]string{"probe": block},
	}
}

func renderProbe(t *testing.T, block string, content map[string]any) string {
	t.Helper()
	layout := Layout{Blocks: []Block{{BlockType: "probe", Content: content}}}
	out, err := RenderMJML(layout, conditionTemplates(block), map[string]any{})
	if err != nil {
		t.Fatalf("RenderMJML: %v", err)
	}
	return out
}

func TestUnless_RendersOnlyWhenConditionIsFalse(t *testing.T) {
	const block = `<Section>
  <Text if="url">has-url</Text>
  <Text unless="url">no-url</Text>
</Section>`

	cases := []struct {
		name    string
		content map[string]any
		want    string
		notWant string
	}{
		{"value present", map[string]any{"url": "https://example.com"}, "has-url", "no-url"},
		{"value absent", map[string]any{}, "no-url", "has-url"},
		{"value blank", map[string]any{"url": "   "}, "no-url", "has-url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := renderProbe(t, block, tc.content)
			if !strings.Contains(out, tc.want) {
				t.Errorf("expected %q in output:\n%s", tc.want, out)
			}
			if strings.Contains(out, tc.notWant) {
				t.Errorf("did not expect %q in output:\n%s", tc.notWant, out)
			}
		})
	}
}

func TestIf_Comparison(t *testing.T) {
	const block = `<Section>
  <Text if="mode == compact">is-compact</Text>
  <Text if="mode != compact">not-compact</Text>
</Section>`

	cases := []struct {
		name    string
		content map[string]any
		want    string
		notWant string
	}{
		{"equal", map[string]any{"mode": "compact"}, "is-compact", "not-compact"},
		{"different", map[string]any{"mode": "wide"}, "not-compact", "is-compact"},
		{"missing field counts as empty text", map[string]any{}, "not-compact", "is-compact"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := renderProbe(t, block, tc.content)
			if !strings.Contains(out, tc.want) {
				t.Errorf("expected %q in output:\n%s", tc.want, out)
			}
			if strings.Contains(out, tc.notWant) {
				t.Errorf("did not expect %q in output:\n%s", tc.notWant, out)
			}
		})
	}
}

// TestUnless_ItalicToggle covers the user community intro_paragraph block, which
// picks the italic or upright variant with unless="italic == false" and
// if="italic == false". A boolean false and the text "false" both choose the
// upright variant. True, "true" and an absent field choose the italic one.
func TestUnless_ItalicToggle(t *testing.T) {
	const block = `<Section>
  <Text unless="italic == false">italic-variant</Text>
  <Text if="italic == false">upright-variant</Text>
</Section>`

	cases := []struct {
		name   string
		italic any
		set    bool
		want   string
	}{
		{"boolean false", false, true, "upright-variant"},
		{"text false", "false", true, "upright-variant"},
		{"boolean true", true, true, "italic-variant"},
		{"text true", "true", true, "italic-variant"},
		{"absent", nil, false, "italic-variant"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := map[string]any{}
			if tc.set {
				content["italic"] = tc.italic
			}
			out := renderProbe(t, block, content)
			other := "upright-variant"
			if tc.want == other {
				other = "italic-variant"
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("expected %q in output:\n%s", tc.want, out)
			}
			if strings.Contains(out, other) {
				t.Errorf("did not expect %q in output:\n%s", other, out)
			}
		})
	}
}

// TestIf_TruthinessOfNonText checks the plain condition on values that are not
// text: a boolean, a number, and arrays.
func TestIf_TruthinessOfNonText(t *testing.T) {
	cases := []struct {
		name  string
		value any
		shown bool
	}{
		{"true", true, true},
		{"false", false, false},
		{"zero", float64(0), false},
		{"non-zero number", float64(3), true},
		{"empty array", []any{}, false},
		{"array with an item", []any{map[string]any{"a": "b"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := renderProbe(t, `<Section><Text if="flag">flag-shown</Text></Section>`, map[string]any{"flag": tc.value})
			if got := strings.Contains(out, "flag-shown"); got != tc.shown {
				t.Errorf("shown = %v, want %v:\n%s", got, tc.shown, out)
			}
		})
	}
}

// TestUnless_OnRichtext checks that a condition on a richtext element is
// honoured. The intro_paragraph block relies on this to choose between two
// richtext variants.
func TestUnless_OnRichtext(t *testing.T) {
	const block = `<Section>
  <richtext unless="plain" field="text" />
  <richtext if="plain" field="text" style="font-style:normal" />
</Section>`
	out := renderProbe(t, block, map[string]any{"text": "<p>hello</p>", "plain": "yes"})
	if got := strings.Count(out, "<p>hello</p>"); got != 1 {
		t.Errorf("expected exactly one richtext variant, got %d:\n%s", got, out)
	}
}

// TestUnless_OnWrapper checks that the wrapper binder honours unless, since the
// wrapper does not go through bindNode for plain elements.
func TestUnless_OnWrapper(t *testing.T) {
	templates := Templates{
		Wrappers: map[string]string{"default": `<Section>
  <Text unless="edition.hide">wrapper-row</Text>
  <slot name="body" />
</Section>`},
		Blocks: map[string]string{"probe": `<Section><Text>body-row</Text></Section>`},
	}
	layout := Layout{Blocks: []Block{{BlockType: "probe"}}}

	shown, err := RenderMJML(layout, templates, map[string]any{"edition": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(shown, "wrapper-row") {
		t.Errorf("expected the wrapper row when edition.hide is empty:\n%s", shown)
	}
	hidden, err := RenderMJML(layout, templates, map[string]any{"edition": map[string]any{"hide": "yes"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hidden, "wrapper-row") {
		t.Errorf("did not expect the wrapper row when edition.hide is set:\n%s", hidden)
	}
}

// TestNestedEach_LFBlogsShape renders the weekly agentic lf_blogs block with two
// groups of two items each, so the array inside an array item has to bind against
// the item.
func TestNestedEach_LFBlogsShape(t *testing.T) {
	templates, err := LoadEmbeddedTemplate("aaif-weekly-agentic")
	if err != nil {
		t.Fatal(err)
	}
	group := func(name string, titles ...string) map[string]any {
		items := make([]any, 0, len(titles))
		for _, title := range titles {
			items = append(items, map[string]any{"headline": title, "url": "https://example.com/" + title})
		}
		return map[string]any{"name": name, "label": name, "title": name, "items": items}
	}
	layout := Layout{
		TemplateKey: "aaif-weekly-agentic",
		Blocks: []Block{{BlockType: "lf_blogs", Content: map[string]any{
			"groups": []any{
				group("GroupOne", "alpha", "beta"),
				group("GroupTwo", "gamma", "delta"),
			},
		}}},
	}
	out, err := Render(context.Background(), layout, templates, map[string]any{"edition": map[string]any{}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{"alpha", "beta", "gamma", "delta"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected nested item %q in output", want)
		}
	}
}

// TestLowercaseAnchorIsALink checks that a lowercase <a href> renders as a link,
// the same as <Link>.
func TestLowercaseAnchorIsALink(t *testing.T) {
	out := renderProbe(t, `<Section><Text>Read <a href="https://example.com/post" style="color:#111111">the post</a></Text></Section>`, map[string]any{})
	if !strings.Contains(out, `<a href="https://example.com/post"`) || !strings.Contains(out, ">the post</a>") {
		t.Errorf("expected the lowercase anchor to render as a link:\n%s", out)
	}
}

// TestLowercaseAnchorUnsafeSchemeIsDropped checks that the URL scheme gate
// applies to a lowercase <a> as it does to <Link>.
func TestLowercaseAnchorUnsafeSchemeIsDropped(t *testing.T) {
	out := renderProbe(t, `<Section><Text><a href="javascript:alert(1)">x</a></Text></Section>`, map[string]any{})
	if strings.Contains(out, "javascript:") {
		t.Errorf("an unsafe href must not reach the output:\n%s", out)
	}
}

// TestWrapperStyleElementIsDropped checks that a <style> element in a wrapper
// does not leak its CSS into the email as text.
func TestWrapperStyleElementIsDropped(t *testing.T) {
	templates := Templates{
		Wrappers: map[string]string{"default": `<style>
@media (prefers-color-scheme: dark) { body { background-color:#0f0f10 !important; } }
</style>
<Section><slot name="body" /></Section>`},
		Blocks: map[string]string{"probe": `<Section><Text>body-row</Text></Section>`},
	}
	out, err := Render(context.Background(), Layout{Blocks: []Block{{BlockType: "probe"}}}, templates, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(out, "prefers-color-scheme") {
		t.Errorf("the wrapper <style> text must not reach the output:\n%s", out)
	}
}

func TestImageWidthGetsAUnit(t *testing.T) {
	cases := map[string]string{"230": "230px", "230px": "230px", " 98 ": "98px", "": "", "50%": "50%"}
	for in, want := range cases {
		if got := imageWidth(in); got != want {
			t.Errorf("imageWidth(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDividerAttrs(t *testing.T) {
	cases := []struct {
		name  string
		style string
		want  string
	}{
		{"rule from border-top and margin", "border:0;border-top:1px solid #d8d8db;margin:0 0 16px", ` border-color="#d8d8db" border-width="1px" padding="0 0 16px"`},
		{"thick rule", "border:0;border-top:2px solid #111111;margin:0 0 10px", ` border-color="#111111" border-width="2px" padding="0 0 10px"`},
		{"explicit attributes win", "border-top:1px solid #d8d8db;margin:4px 0;padding:8px 0;width:100%", ` border-color="#d8d8db" border-width="1px" padding="8px 0" width="100%"`},
		{"no style", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dividerAttrs(&node{Tag: "hr", Attrs: map[string]string{"style": tc.style}})
			if got != tc.want {
				t.Errorf("dividerAttrs(%q) = %q, want %q", tc.style, got, tc.want)
			}
		})
	}
}

// libraryCases is the expected shape of each embedded library. The palette count
// includes bricks, which share one palette with blocks.
var libraryCases = []struct {
	key          string
	label        string
	palette      int
	starters     int
	firstStarter string
}{
	{"aaif-member", "AAIF Member", 19, 19, "intro_letter"},
	{"aaif-user-community", "AAIF User Community", 26, 9, "intro_paragraph"},
	{"aaif-weekly-agentic", "AAIF Weekly Agentic", 12, 10, "lead_commentary"},
}

func TestEmbeddedLibraries_Shape(t *testing.T) {
	for _, tc := range libraryCases {
		t.Run(tc.key, func(t *testing.T) {
			m, err := BuildEmbeddedManifest(tc.key)
			if err != nil {
				t.Fatalf("BuildEmbeddedManifest: %v", err)
			}
			if m.WrapperKey != "default" || m.Wrapper == "" {
				t.Errorf("wrapper_key = %q, wrapper empty = %v", m.WrapperKey, m.Wrapper == "")
			}
			if len(m.Blocks) != tc.palette {
				t.Errorf("palette = %d entries, want %d", len(m.Blocks), tc.palette)
			}
			if len(m.StarterBlocks) != tc.starters {
				t.Errorf("starter blocks = %d, want %d", len(m.StarterBlocks), tc.starters)
			}
			if len(m.StarterBlocks) > 0 && m.StarterBlocks[0].BlockType != tc.firstStarter {
				t.Errorf("first starter block = %q, want %q", m.StarterBlocks[0].BlockType, tc.firstStarter)
			}
			if got := HumanizeKey(tc.key); got != tc.label {
				t.Errorf("label = %q, want %q", got, tc.label)
			}
		})
	}
}

// TestLibraryResolution checks that a layout renders with the library named by
// its template_key only. A block from another library is unrenderable, and an
// empty key uses the fallback library.
func TestLibraryResolution(t *testing.T) {
	render := func(layout Layout) error {
		templates, err := LoadEmbeddedTemplateCached(layout.TemplateKey)
		if err != nil {
			return err
		}
		_, err = Render(context.Background(), layout, templates, map[string]any{"edition": map[string]any{}})
		return err
	}

	// hot_take belongs to the user community library only.
	err := render(Layout{TemplateKey: "aaif-member", Blocks: []Block{{BlockType: "hot_take"}}})
	if !errors.Is(err, ErrUnrenderableLayout) {
		t.Errorf("hot_take in aaif-member: err = %v, want ErrUnrenderableLayout", err)
	}
	if err := render(Layout{TemplateKey: "aaif-user-community", Blocks: []Block{{BlockType: "hot_take"}}}); err != nil {
		t.Errorf("hot_take in aaif-user-community: %v", err)
	}
	// intro_letter belongs to the member library only.
	err = render(Layout{TemplateKey: "aaif-user-community", Blocks: []Block{{BlockType: "intro_letter"}}})
	if !errors.Is(err, ErrUnrenderableLayout) {
		t.Errorf("intro_letter in aaif-user-community: err = %v, want ErrUnrenderableLayout", err)
	}
	if err := render(Layout{TemplateKey: "aaif-member", Blocks: []Block{{BlockType: "intro_letter"}}}); err != nil {
		t.Errorf("intro_letter in aaif-member: %v", err)
	}
	// An empty key falls back to the user community library.
	if err := render(Layout{Blocks: []Block{{BlockType: "hot_take"}}}); err != nil {
		t.Errorf("hot_take with an empty template_key: %v", err)
	}
	if got := FallbackTemplateKey; got != "aaif-user-community" {
		t.Errorf("FallbackTemplateKey = %q, want aaif-user-community", got)
	}
	// A key the binary does not embed.
	if err := render(Layout{TemplateKey: "nope"}); !errors.Is(err, ErrTemplateNotFound) {
		t.Errorf("unknown key: err = %v, want ErrTemplateNotFound", err)
	}
}

// TestStarterBlocks_NameKnownBlocks checks that readStarterBlocks refuses an
// edition.json that names a block the set does not have.
func TestStarterBlocks_NameKnownBlocks(t *testing.T) {
	palette := []ManifestEntry{{BlockType: "a"}, {BlockType: "b"}}
	dir := t.TempDir()
	writeFile := func(content string) {
		t.Helper()
		if err := os.WriteFile(dir+"/edition.json", []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeFile(`{"version":1,"blocks":[{"block":"b"},{"block":"a"}]}`)
	got, err := readStarterBlocks(os.DirFS(dir), ".", palette)
	if err != nil {
		t.Fatalf("readStarterBlocks: %v", err)
	}
	if len(got) != 2 || got[0].BlockType != "b" || got[1].BlockType != "a" {
		t.Errorf("starters = %+v, want b then a", got)
	}

	writeFile(`{"version":1,"blocks":[{"block":"missing"}]}`)
	if _, err := readStarterBlocks(os.DirFS(dir), ".", palette); err == nil {
		t.Error("expected an error for a starter block the set does not have")
	}

	writeFile(`not json`)
	if _, err := readStarterBlocks(os.DirFS(dir), ".", palette); err == nil {
		t.Error("expected an error for invalid JSON")
	}
}

func TestStarterBlocks_MissingFileMeansNone(t *testing.T) {
	got, err := readStarterBlocks(os.DirFS(t.TempDir()), ".", nil)
	if err != nil {
		t.Fatalf("readStarterBlocks: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("starters = %#v, want an empty, non-nil list", got)
	}
	// The wire form is an empty array, not null.
	raw, err := json.Marshal(Manifest{StarterBlocks: got})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"starter_blocks":[]`) {
		t.Errorf("manifest JSON = %s, want starter_blocks as an empty array", raw)
	}
}

func TestComplianceLinkSentinelsGetClickTrackingOff(t *testing.T) {
	attrs := bindAttrs(map[string]string{"href": "%%UNSUBSCRIBE_URL%%"}, nil)
	if attrs["clicktracking"] != "off" {
		t.Errorf("attrs = %v, want clicktracking off on the unsubscribe link", attrs)
	}
	attrs = bindAttrs(map[string]string{"href": "https://example.com"}, nil)
	if _, ok := attrs["clicktracking"]; ok {
		t.Errorf("attrs = %v, an ordinary link must not get clicktracking", attrs)
	}
}
