// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package render

import (
	"strings"
	"testing"
)

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

const testViewOnlineURL = "https://app.lfx.dev/newsletters/proj-1/nl-1/view"

func TestEmailHTMLViewOnlineLine(t *testing.T) {
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
				ViewOnlineURL:           testViewOnlineURL,
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
				Subject:       "Hello",
				BodyHTML:      "<p>Body</p>",
				DisplayName:   "Kubernetes",
				ViewOnlineURL: testViewOnlineURL,
			},
			wantLine: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			html := EmailHTML(tc.input)
			hasHref := strings.Contains(html, `href="`+testViewOnlineURL+`"`)
			hasVerbiage := strings.Contains(html, "Trouble viewing this email?") && strings.Contains(html, "View it online</a>")
			if tc.wantLine && (!hasHref || !hasVerbiage) {
				t.Fatalf("HTML missing View Online line:\n%s", html)
			}
			if !tc.wantLine && (hasHref || strings.Contains(html, "Trouble viewing this email?")) {
				t.Fatalf("HTML unexpectedly contains View Online line:\n%s", html)
			}
		})
	}
}

func TestEmailHTMLViewOnlineURLEscaped(t *testing.T) {
	html := EmailHTML(Chrome{
		Subject:                 "Hello",
		BodyHTML:                "<p>Body</p>",
		DisplayName:             "Kubernetes",
		IncludeComplianceFooter: true,
		ViewOnlineURL:           `https://app.lfx.dev/newsletters/proj-1/nl-1/view?a=1&b="x"`,
	})
	if !strings.Contains(html, `href="https://app.lfx.dev/newsletters/proj-1/nl-1/view?a=1&amp;b=&quot;x&quot;"`) {
		t.Fatalf("View Online URL not HTML-escaped:\n%s", html)
	}
}

func TestEmailTextViewOnlineLine(t *testing.T) {
	wantLine := "Trouble viewing this email? View it online: " + testViewOnlineURL

	withURL := Chrome{
		Subject:                 "Hello",
		BodyHTML:                "<p>Body</p>",
		DisplayName:             "Kubernetes",
		IncludeComplianceFooter: true,
		UnsubscribeURL:          "https://api.example/newsletters/unsubscribe?t=tok",
		ViewOnlineURL:           testViewOnlineURL,
	}
	text := EmailText(withURL)
	if !strings.Contains(text, wantLine) {
		t.Fatalf("text body missing View Online line:\n%s", text)
	}
	if strings.Index(text, wantLine) > strings.Index(text, "Unsubscribe from") {
		t.Errorf("View Online line should precede the unsubscribe line:\n%s", text)
	}

	withURL.ViewOnlineURL = ""
	if text := EmailText(withURL); strings.Contains(text, "Trouble viewing this email?") {
		t.Fatalf("text body unexpectedly contains View Online line without URL:\n%s", text)
	}

	withURL.ViewOnlineURL = testViewOnlineURL
	withURL.IncludeComplianceFooter = false
	if text := EmailText(withURL); strings.Contains(text, "Trouble viewing this email?") {
		t.Fatalf("text body unexpectedly contains View Online line with footer off:\n%s", text)
	}
}
