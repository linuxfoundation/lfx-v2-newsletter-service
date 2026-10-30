// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"strings"
	"testing"
)

// TestReconcileCardBorders_RoundsBorderedCell pins the fix for MJML squaring a
// bordered rounded card: MJML renders border-radius on the outer table but the
// full border on the inner cell, so the border has square corners. reconcile
// copies the enclosing radius onto the bordered cell and marks the radiused
// table border-collapse:separate so the corners round.
func TestReconcileCardBorders_RoundsBorderedCell(t *testing.T) {
	in := `<div style="border-radius:14px;max-width:600px">` +
		`<table style="border-radius:14px;width:100%">` +
		`<tbody><tr><td style="border:1px solid #131313;padding:20px 0">Hi</td></tr></tbody>` +
		`</table></div>`
	out := reconcileCardBorders(in, nil)

	if !strings.Contains(out, `border:1px solid #131313;padding:20px 0;border-radius:14px`) {
		t.Errorf("bordered cell did not inherit the enclosing border-radius:\n%s", out)
	}
	if !strings.Contains(out, `border-radius:14px;width:100%;border-collapse:separate`) {
		t.Errorf("radiused table did not get border-collapse:separate:\n%s", out)
	}
}

// TestReconcileCardBorders_LeavesSingleSideBorders pins that a single-side border
// (e.g. the footer's border-top divider) is NOT treated as a card border and
// stays untouched — only a full `border` shorthand rounds.
func TestReconcileCardBorders_LeavesSingleSideBorders(t *testing.T) {
	in := `<div style="border-radius:14px"><table style="border-radius:14px">` +
		`<tbody><tr><td style="border-top:1px solid #e5e7eb;padding:24px">Footer</td></tr></tbody>` +
		`</table></div>`
	out := reconcileCardBorders(in, nil)

	if strings.Contains(out, "border-top:1px solid #e5e7eb;padding:24px;border-radius") {
		t.Errorf("single-side border-top should not inherit a card radius:\n%s", out)
	}
}

// TestReconcileCardBorders_KeepsOwnRadius pins that an element already carrying
// its own border-radius is left alone (no double application).
func TestReconcileCardBorders_KeepsOwnRadius(t *testing.T) {
	in := `<div style="border-radius:20px"><span style="border:1px solid #000;border-radius:4px">x</span></div>`
	out := reconcileCardBorders(in, nil)
	if strings.Count(out, "border-radius") != 2 {
		t.Errorf("expected the element to keep only its own radius:\n%s", out)
	}
}

// TestReconcileCardBorders_ReAppliesAuthoredShadow pins that each card's AUTHORED
// box-shadow (captured pre-translation, threaded in as cardShadows in document
// order) is re-applied to its rounded mj-section wrapper — preserving the real
// colour/offset instead of synthesising one from the border colour. It covers
// both a bordered card whose shadow colour differs from its border (a violet
// shadow on a black border) and a BORDERLESS card (which the old
// synthesise-from-border path skipped entirely, losing its shadow).
func TestReconcileCardBorders_ReAppliesAuthoredShadow(t *testing.T) {
	// Bordered card (violet shadow, black border) then a borderless card (blue
	// shadow), in document order — mirrors the compiled wrapper/table/cell shape.
	bordered := `<div style="border-radius:14px;margin:0px auto;max-width:600px">` +
		`<table style="border-radius:14px;width:100%">` +
		`<tbody><tr><td style="border:1px solid #131313;padding:20px 0">Hi</td></tr></tbody>` +
		`</table></div>`
	borderless := `<div style="background:#131313;border-radius:14px;margin:0px auto;max-width:600px">` +
		`<table style="background:#131313;border-radius:14px;width:100%">` +
		`<tbody><tr><td style="padding:20px 0">Nums</td></tr></tbody>` +
		`</table></div>`
	shadows := []string{"6px 6px 0 #7b4dff", "6px 6px 0 #3d8bff"}
	out := reconcileCardBorders(bordered+borderless, shadows)

	// The bordered card's wrapper keeps the AUTHORED violet shadow, not one
	// synthesised from its black border colour.
	if !strings.Contains(out, "box-shadow:6px 6px 0 #7b4dff") {
		t.Errorf("bordered card did not get its authored violet shadow:\n%s", out)
	}
	if strings.Contains(out, "box-shadow:6px 6px 0 #131313") {
		t.Errorf("bordered card shadow was synthesised from the border colour instead of the authored value:\n%s", out)
	}
	// The borderless card keeps its authored blue shadow (previously lost).
	if !strings.Contains(out, "box-shadow:6px 6px 0 #3d8bff") {
		t.Errorf("borderless card did not keep its authored blue shadow:\n%s", out)
	}
	// Exactly one shadow per card wrapper — not on the cell or inner table.
	if strings.Count(out, "box-shadow") != 2 {
		t.Errorf("expected exactly two box-shadows (one per card wrapper), got %d:\n%s", strings.Count(out, "box-shadow"), out)
	}
}

// TestReconcileCardBorders_AddsGapToCardWrapper pins that the card's mj-section
// wrapper (radiused, margin:auto-centred, no border) gets the inter-card bottom
// gap MJML strips, while a plain centred section (no card radius) and a
// single-side-bordered footer do NOT.
func TestReconcileCardBorders_AddsGapToCardWrapper(t *testing.T) {
	card := `<div style="border-radius:14px;margin:0px auto;max-width:600px">` +
		`<table style="border-radius:14px;width:100%"><tbody><tr>` +
		`<td style="border:1px solid #131313">Hi</td></tr></tbody></table></div>`
	plain := `<div style="margin:0px auto;max-width:600px"><table><tbody><tr><td>Header</td></tr></tbody></table></div>`
	out := reconcileCardBorders(card+plain, nil)
	if strings.Count(out, "margin-bottom:24px") != 1 {
		t.Errorf("expected the card wrapper (only) to get the 24px gap, got %d:\n%s", strings.Count(out, "margin-bottom:24px"), out)
	}
	if !strings.Contains(out, "border-radius:14px;margin:0px auto;max-width:600px;margin-bottom:24px") {
		t.Errorf("gap not appended to the card wrapper div:\n%s", out)
	}
}

// TestBorderColor pins the colour extraction from a border shorthand.
func TestBorderColor(t *testing.T) {
	cases := map[string]string{
		"1px solid #131313":  "#131313",
		"2px dashed black":   "black",
		"1px solid rgb(0,0)": "", // function-notation → skipped
		"":                   "",
	}
	for in, want := range cases {
		if got := borderColor(in); got != want {
			t.Errorf("borderColor(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPropagateTextAlign pins that a container's text-align propagates onto text
// descendants that don't set their own — the fix for centered header/footer
// chrome rendering left-aligned once MJML dissolves the wrapping div.
func TestPropagateTextAlign(t *testing.T) {
	container := &node{
		Tag:   "div",
		Attrs: map[string]string{"style": "text-align:center;padding:6px 0 0"},
		Children: []*node{
			{Tag: "text", Attrs: map[string]string{"style": "font-size:11px"}},
			{Tag: "img", Attrs: map[string]string{"src": "x"}},
		},
	}
	propagateTextAlign([]*node{container}, "")
	if got := cssProp(container.Children[0].Attrs["style"], "text-align"); got != "center" {
		t.Errorf("text child did not inherit center: %q", got)
	}
	// Non-text nodes (img) are not given a text-align.
	if got := cssProp(container.Children[1].Attrs["style"], "text-align"); got != "" {
		t.Errorf("img should not inherit text-align: %q", got)
	}

	// A text node that sets its own alignment is left alone.
	own := &node{
		Tag:      "div",
		Attrs:    map[string]string{"style": "text-align:center"},
		Children: []*node{{Tag: "text", Attrs: map[string]string{"style": "text-align:left"}}},
	}
	propagateTextAlign([]*node{own}, "")
	if got := cssProp(own.Children[0].Attrs["style"], "text-align"); got != "left" {
		t.Errorf("own alignment overwritten: %q", got)
	}
}

func TestCSSProp(t *testing.T) {
	style := "border:1px solid #131313;border-radius:14px;padding:20px 0"
	if got := cssProp(style, "border"); got != "1px solid #131313" {
		t.Errorf(`cssProp "border" = %q`, got)
	}
	if got := cssProp(style, "border-radius"); got != "14px" {
		t.Errorf(`cssProp "border-radius" = %q`, got)
	}
	// Exact match: "border" must not match "border-radius"/"border-top".
	if got := cssProp("border-top:1px solid #000", "border"); got != "" {
		t.Errorf(`cssProp "border" matched border-top: %q`, got)
	}
}
