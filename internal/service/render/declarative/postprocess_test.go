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
	out := reconcileCardBorders(in)

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
	out := reconcileCardBorders(in)

	if strings.Contains(out, "border-top:1px solid #e5e7eb;padding:24px;border-radius") {
		t.Errorf("single-side border-top should not inherit a card radius:\n%s", out)
	}
}

// TestReconcileCardBorders_KeepsOwnRadius pins that an element already carrying
// its own border-radius is left alone (no double application).
func TestReconcileCardBorders_KeepsOwnRadius(t *testing.T) {
	in := `<div style="border-radius:20px"><span style="border:1px solid #000;border-radius:4px">x</span></div>`
	out := reconcileCardBorders(in)
	if strings.Count(out, "border-radius") != 2 {
		t.Errorf("expected the element to keep only its own radius:\n%s", out)
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
