// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package render

import (
	"strings"
	"testing"
)

// TestStripHTMLForTextDropsHeadStyleScript asserts the layout text/plain
// derivation drops <head>/<style>/<script> inner text. The layout send path
// feeds a full compiled MJML document, whose <head><style>…</style></head>
// carries CSS/media-query text that must never leak into the plain-text part.
func TestStripHTMLForTextDropsHeadStyleScript(t *testing.T) {
	doc := `<!DOCTYPE html><html><head>` +
		`<title>Weekly</title>` +
		`<style>@media only screen and (max-width:600px){.col{width:100%!important;}}` +
		`.btn{background-color:#4086c6;}</style>` +
		`</head><body>` +
		`<p>Hello subscribers</p>` +
		`<script>trackOpen();</script>` +
		`<a href="https://example.com/read">Read more</a>` +
		`</body></html>`

	got := StripHTMLForText(doc)

	// The CSS / media-query / script text must be gone.
	for _, leaked := range []string{"@media", "max-width", "background-color", "#4086c6", "trackOpen", "<title>", "Weekly"} {
		if strings.Contains(got, leaked) {
			t.Errorf("text body leaked non-rendered content %q: %q", leaked, got)
		}
	}
	// The real body content survives, with link destinations preserved.
	if !strings.Contains(got, "Hello subscribers") {
		t.Errorf("text body dropped visible content: %q", got)
	}
	if !strings.Contains(got, "https://example.com/read") {
		t.Errorf("text body dropped link destination: %q", got)
	}
}
