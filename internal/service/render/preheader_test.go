// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package render

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPreviewText(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "plain text passes through",
			body: "Quarterly update from the project.",
			want: "Quarterly update from the project.",
		},
		{
			name: "empty body",
			body: "",
			want: "",
		},
		{
			name: "whitespace and empty tags only",
			body: "  <p>   </p>\n<div><br/></div>  ",
			want: "",
		},
		{
			name: "inline tags do not split words",
			body: "<p>Hel<strong>lo</strong> <em>world</em> from <a href=\"https://lfx.dev\">LFX</a></p>",
			want: "Hello world from LFX",
		},
		{
			name: "block boundaries separate words",
			body: "<h2>Release</h2><p>Shipped v2</p><ul><li>Faster builds</li><li>Fewer bugs</li></ul>",
			want: "Release Shipped v2 Faster builds Fewer bugs",
		},
		{
			name: "br separates lines",
			body: "<p>line one<br>line two</p>",
			want: "line one line two",
		},
		{
			name: "style content dropped",
			body: "<style>.x{color:red;}</style><p>Visible copy</p>",
			want: "Visible copy",
		},
		{
			name: "script content dropped",
			body: "<script>alert('hi')</script><p>Visible copy</p>",
			want: "Visible copy",
		},
		{
			name: "title and comments dropped",
			body: "<title>Hidden title</title><!-- hidden comment --><p>Visible copy</p>",
			want: "Visible copy",
		},
		{
			name: "uppercase and attributed drop tags",
			body: "<STYLE type=\"text/css\">.x{}</STYLE><P>Visible copy</P>",
			want: "Visible copy",
		},
		{
			name: "entities decoded",
			body: "<p>R&amp;D &lt;update&gt; &quot;now&quot;&nbsp;live &mdash; more</p>",
			want: "R&D <update> \"now\" live — more",
		},
		{
			name: "literal escaped markup stays text",
			body: "<p>type &lt;script&gt; to see</p>",
			want: "type <script> to see",
		},
		{
			name: "nbsp and zero-width padding collapses",
			body: "<p>one&nbsp;&zwnj;&nbsp;&zwnj;two</p>",
			want: "one two",
		},
		{
			name: "internal whitespace collapses",
			body: "<p>spread\n\n   over \t lines</p>",
			want: "spread over lines",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := previewText(tc.body); got != tc.want {
				t.Fatalf("previewText(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestPreviewTextTruncation(t *testing.T) {
	t.Run("long text truncates at a word boundary with ellipsis", func(t *testing.T) {
		body := "<p>" + strings.Repeat("wordy ", 60) + "</p>"
		got := previewText(body)
		if !strings.HasSuffix(got, "…") {
			t.Fatalf("truncated preview must end with ellipsis, got %q", got)
		}
		if n := utf8.RuneCountInString(got); n > previewMaxRunes+1 {
			t.Fatalf("preview is %d runes, want at most %d", n, previewMaxRunes+1)
		}
		// The cut backs up to a word boundary, so the last kept token is the
		// full word "wordy" — never a mid-word fragment or a dangling space.
		if !strings.HasSuffix(got, "wordy…") {
			t.Fatalf("preview should end on a whole word plus ellipsis: %q", got)
		}
	})

	t.Run("text at the limit is not truncated", func(t *testing.T) {
		body := strings.Repeat("a", previewMaxRunes)
		got := previewText(body)
		if got != body {
			t.Fatalf("previewText altered text at the limit: got %d runes with suffix %q", utf8.RuneCountInString(got), got[len(got)-3:])
		}
	})

	t.Run("one over the limit gains an ellipsis", func(t *testing.T) {
		body := strings.Repeat("a", previewMaxRunes+1)
		got := previewText(body)
		if !strings.HasSuffix(got, "…") {
			t.Fatalf("expected ellipsis, got %q", got)
		}
		if n := utf8.RuneCountInString(got); n != previewMaxRunes+1 {
			t.Fatalf("unbroken text should hard-cut at %d runes plus ellipsis, got %d", previewMaxRunes, n)
		}
	})

	t.Run("multibyte runes are not split", func(t *testing.T) {
		body := strings.Repeat("é", previewMaxRunes+10)
		got := previewText(body)
		if !utf8.ValidString(got) {
			t.Fatalf("preview is not valid UTF-8: %q", got)
		}
		if !strings.HasSuffix(got, "…") {
			t.Fatalf("expected ellipsis, got %q", got)
		}
	})
}

var preheaderDivRe = regexp.MustCompile(`(?s)<body[^>]*>\s*<div style="` + regexp.QuoteMeta(preheaderStyle) + `">(.*?)</div>`)

func TestEmailHTMLPreheaderInjection(t *testing.T) {
	htmlOut := EmailHTML(Chrome{
		Subject:     "October Update",
		BodyHTML:    "<p>Big news: R&amp;D shipped.</p><p>More below.</p>",
		DisplayName: "CNCF",
	})

	m := preheaderDivRe.FindStringSubmatch(htmlOut)
	if m == nil {
		t.Fatalf("preheader div not found immediately after <body> tag")
	}
	inner := m[1]
	if !strings.HasPrefix(inner, "Big news: R&amp;D shipped. More below.") {
		t.Fatalf("preheader text = %q, want it to start with the derived preview", inner)
	}
	if !strings.HasSuffix(inner, strings.Repeat("&nbsp;&zwnj;", preheaderPadPairs)) {
		t.Fatalf("preheader must end with %d &nbsp;&zwnj; pad pairs", preheaderPadPairs)
	}
	if strings.Count(htmlOut, preheaderStyle) != 1 {
		t.Fatalf("expected exactly one preheader div")
	}

	// The preheader must precede all visible chrome content.
	if strings.Index(htmlOut, preheaderStyle) > strings.Index(htmlOut, "<table role=") {
		t.Fatalf("preheader must come before the layout table")
	}
}

func TestEmailHTMLPreheaderTruncatesLongBody(t *testing.T) {
	htmlOut := EmailHTML(Chrome{
		Subject:     "Subject",
		BodyHTML:    "<p>" + strings.Repeat("lots of newsletter copy ", 30) + "</p>",
		DisplayName: "CNCF",
	})
	m := preheaderDivRe.FindStringSubmatch(htmlOut)
	if m == nil {
		t.Fatalf("preheader div not found")
	}
	text := strings.TrimSuffix(m[1], strings.Repeat("&nbsp;&zwnj;", preheaderPadPairs))
	if !strings.HasSuffix(text, "…") {
		t.Fatalf("long body preview must end with ellipsis, got %q", text)
	}
}

func TestEmailHTMLPreheaderOmittedForEmptyBody(t *testing.T) {
	for _, body := range []string{"", "<p><br></p>", "<style>.x{}</style>"} {
		htmlOut := EmailHTML(Chrome{Subject: "Subject", BodyHTML: body, DisplayName: "CNCF"})
		if strings.Contains(htmlOut, preheaderStyle) {
			t.Fatalf("body %q must not emit a preheader", body)
		}
	}
}

func TestEmailHTMLPreheaderEscapesDerivedText(t *testing.T) {
	htmlOut := EmailHTML(Chrome{
		Subject:     "Subject",
		BodyHTML:    "<p>watch for &lt;img&gt; &amp; \"quotes\"</p>",
		DisplayName: "CNCF",
	})
	m := preheaderDivRe.FindStringSubmatch(htmlOut)
	if m == nil {
		t.Fatalf("preheader div not found")
	}
	if !strings.HasPrefix(m[1], "watch for &lt;img&gt; &amp; &quot;quotes&quot;") {
		t.Fatalf("preheader text must be HTML-escaped, got %q", m[1])
	}
}
