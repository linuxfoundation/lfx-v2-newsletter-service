// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package render

import (
	"strings"
	"testing"
	"unicode/utf8"
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

// TestStripHTMLForTextTokenizerEdgeCases guards the two failure modes that a
// regex-based strip could not handle and that the html-tokenizer pass fixes.
func TestStripHTMLForTextTokenizerEdgeCases(t *testing.T) {
	// (1) A '>' inside an attribute value must not corrupt extraction. A naive
	// `<[^>]+>` tag pass stops at the first '>' (here inside title="1 > 0") and
	// leaks the attribute tail into the visible text.
	got := StripHTMLForText(`<p><a href="https://example.com/a" title="1 > 0">Read</a> now</p>`)
	if strings.Contains(got, "title=") || strings.Contains(got, `0">`) {
		t.Errorf("attribute tail leaked into text: %q", got)
	}
	for _, want := range []string{"Read", "https://example.com/a", "now"} {
		if !strings.Contains(got, want) {
			t.Errorf("visible content/link lost %q: %q", want, got)
		}
	}

	// (2) A self-closing <style/> is a raw-text element (HTML ignores the slash),
	// so its CSS must never leak into the text. The old regex skip-scanner keyed
	// on a </style> that a self-closing tag never opens, spilling the CSS.
	// Content BEFORE the style survives; the style's own content does not.
	got = StripHTMLForText(`<p>Before</p><style/>.leaked{color:red}`)
	if !strings.Contains(got, "Before") {
		t.Errorf("content before self-closing <style/> was dropped: %q", got)
	}
	for _, leaked := range []string{".leaked", "color:red", "leaked"} {
		if strings.Contains(got, leaked) {
			t.Errorf("self-closing <style/> css leaked %q: %q", leaked, got)
		}
	}
}

const testMyNewslettersURL = "https://app.lfx.dev/newsletters/my"

func TestEmailHTMLMyNewslettersLine(t *testing.T) {
	tests := []struct {
		name     string
		input    Chrome
		wantLine bool
	}{
		{
			name: "footer on with URL renders the line",
			input: Chrome{
				Subject:                 "Hello",
				BodyHTML:                "<p>Body</p>",
				DisplayName:             "Kubernetes",
				IncludeComplianceFooter: true,
				MyNewslettersURL:        testMyNewslettersURL,
			},
			wantLine: true,
		},
		{
			name: "footer on without URL omits the line",
			input: Chrome{
				Subject:                 "Hello",
				BodyHTML:                "<p>Body</p>",
				DisplayName:             "Kubernetes",
				IncludeComplianceFooter: true,
			},
			wantLine: false,
		},
		{
			name: "footer off suppresses the line even with URL set",
			input: Chrome{
				Subject:          "Hello",
				BodyHTML:         "<p>Body</p>",
				DisplayName:      "Kubernetes",
				MyNewslettersURL: testMyNewslettersURL,
			},
			wantLine: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			html := EmailHTML(tc.input)
			hasHref := strings.Contains(html, `href="`+testMyNewslettersURL+`"`)
			hasVerbiage := strings.Contains(html, "Missed an issue?") && strings.Contains(html, "My Newsletters</a>")
			if tc.wantLine && (!hasHref || !hasVerbiage) {
				t.Fatalf("HTML missing My Newsletters line:\n%s", html)
			}
			if !tc.wantLine && (hasHref || strings.Contains(html, "Missed an issue?")) {
				t.Fatalf("HTML unexpectedly contains My Newsletters line:\n%s", html)
			}
		})
	}
}

func TestEmailHTMLMyNewslettersLineAboveUnsubscribe(t *testing.T) {
	html := EmailHTML(Chrome{
		Subject:                 "Hello",
		BodyHTML:                "<p>Body</p>",
		DisplayName:             "Kubernetes",
		IncludeComplianceFooter: true,
		UnsubscribeURL:          "https://api.example/newsletters/unsubscribe?t=tok",
		MyNewslettersURL:        testMyNewslettersURL,
	})
	myIdx := strings.Index(html, "My Newsletters")
	unsubIdx := strings.Index(html, "Unsubscribe")
	if myIdx < 0 || unsubIdx < 0 {
		t.Fatalf("expected both My Newsletters and Unsubscribe in footer:\n%s", html)
	}
	if myIdx > unsubIdx {
		t.Errorf("My Newsletters line (%d) should render above the unsubscribe small print (%d)", myIdx, unsubIdx)
	}
}

// TestEmailHTMLChromeLinksDisableClickTracking verifies the Unsubscribe and My
// Newsletters anchors both carry clicktracking="off" (so the HMAC-signed
// unsubscribe link is not proxied through the provider's click-tracking
// redirect, and neither counts toward click_rate), and that the rendered
// unsubscribe URL itself is unchanged.
func TestEmailHTMLChromeLinksDisableClickTracking(t *testing.T) {
	const unsubURL = "https://api.example/newsletters/unsubscribe?t=tok"
	html := EmailHTML(Chrome{
		Subject:                 "Hello",
		BodyHTML:                "<p>Body</p>",
		DisplayName:             "Kubernetes",
		IncludeComplianceFooter: true,
		UnsubscribeURL:          unsubURL,
		MyNewslettersURL:        testMyNewslettersURL,
	})
	if !strings.Contains(html, `clicktracking="off" href="`+unsubURL+`"`) {
		t.Errorf("Unsubscribe anchor missing clicktracking=\"off\" attribute appearing before href:\n%s", html)
	}
	if !strings.Contains(html, `clicktracking="off" href="`+testMyNewslettersURL+`"`) {
		t.Errorf("My Newsletters anchor missing clicktracking=\"off\" attribute appearing before href:\n%s", html)
	}
	// The unsubscribe URL itself must still render exactly as given — the
	// exclusion is an anchor attribute, not a URL transformation.
	if !strings.Contains(html, `href="`+unsubURL+`"`) {
		t.Errorf("Unsubscribe URL rendered incorrectly:\n%s", html)
	}
}

func TestEmailHTMLMyNewslettersURLEscaped(t *testing.T) {
	html := EmailHTML(Chrome{
		Subject:                 "Hello",
		BodyHTML:                "<p>Body</p>",
		DisplayName:             "Kubernetes",
		IncludeComplianceFooter: true,
		MyNewslettersURL:        `https://app.lfx.dev/newsletters/my?a=1&b="x"`,
	})
	if !strings.Contains(html, `href="https://app.lfx.dev/newsletters/my?a=1&amp;b=&quot;x&quot;"`) {
		t.Fatalf("My Newsletters URL not HTML-escaped:\n%s", html)
	}
}

func TestEmailTextMyNewslettersLine(t *testing.T) {
	wantLine := "Missed an issue? View past newsletters any time in My Newsletters: " + testMyNewslettersURL

	withURL := Chrome{
		Subject:                 "Hello",
		BodyHTML:                "<p>Body</p>",
		DisplayName:             "Kubernetes",
		IncludeComplianceFooter: true,
		UnsubscribeURL:          "https://api.example/newsletters/unsubscribe?t=tok",
		MyNewslettersURL:        testMyNewslettersURL,
	}
	text := EmailText(withURL)
	if !strings.Contains(text, wantLine) {
		t.Fatalf("text body missing My Newsletters line:\n%s", text)
	}
	if strings.Index(text, wantLine) > strings.Index(text, "Unsubscribe from") {
		t.Errorf("My Newsletters line should precede the unsubscribe line:\n%s", text)
	}

	withURL.MyNewslettersURL = ""
	if text := EmailText(withURL); strings.Contains(text, "Missed an issue?") {
		t.Fatalf("text body unexpectedly contains My Newsletters line without URL:\n%s", text)
	}

	withURL.MyNewslettersURL = testMyNewslettersURL
	withURL.IncludeComplianceFooter = false
	if text := EmailText(withURL); strings.Contains(text, "Missed an issue?") {
		t.Fatalf("text body unexpectedly contains My Newsletters line with footer off:\n%s", text)
	}
}

// TestEmailHTMLForcesUTF8Charset guards the charset fix for Gmail's "[Message
// clipped]" trailer (the charset cause, independent of the ~102KB size cause).
// In SendGrid testing an all-ASCII/Latin-1 newsletter was transmitted as
// charset=iso-8859-1 and Gmail clipped it. EmailHTML injects a hidden zero-width
// non-joiner (U+200C), which is unrepresentable in single-byte charsets, so the
// rendered document must be encoded as UTF-8. This test fails if the guard is
// removed. See linuxfoundation/lfx-self-serve#1744.
func TestEmailHTMLForcesUTF8Charset(t *testing.T) {
	// A newsletter whose subject, display name and body are entirely ASCII.
	// Without the guard the rendered HTML would be pure ASCII on the wire.
	html := EmailHTML(Chrome{
		Subject:     "Weekly update",
		BodyHTML:    "<p>All ASCII content here.</p>",
		DisplayName: "Kubernetes",
	})
	if !strings.ContainsRune(html, '\u200c') {
		t.Fatalf("rendered HTML missing the U+200C charset guard:\n%s", html)
	}
	// The guard's purpose is a non-ASCII byte stream so the provider encodes
	// UTF-8. RuneCount < byte length holds iff at least one multibyte rune exists.
	if utf8.RuneCountInString(html) == len(html) {
		t.Errorf("rendered HTML is single-byte throughout (would be sent as iso-8859-1); expected a multibyte rune")
	}
}
