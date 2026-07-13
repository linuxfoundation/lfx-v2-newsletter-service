// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package render

import (
	"strings"
	"testing"
)

func TestPreviewText(t *testing.T) {
	longWordy := "<p>" + strings.Repeat("lorem ipsum ", 20) + "</p>"

	tests := []struct {
		name     string
		bodyHTML string
		want     string
	}{
		{
			name:     "strips tags and collapses whitespace",
			bodyHTML: "<h2>Release   notes</h2>\n<p>The\nMay update is out.</p>",
			want:     "Release notes The May update is out.",
		},
		{
			name:     "empty body yields empty preview",
			bodyHTML: "",
			want:     "",
		},
		{
			name:     "markup-only body yields empty preview",
			bodyHTML: "<p></p><hr/>",
			want:     "",
		},
		{
			name:     "short body is untouched",
			bodyHTML: "<p>Hello members</p>",
			want:     "Hello members",
		},
		{
			name:     "adjacent blocks get separators",
			bodyHTML: "<p>Hello</p><p>members</p><h2>News</h2><ul><li>one</li><li>two</li></ul>",
			want:     "Hello members News one two",
		},
		{
			name:     "line breaks get separators, inline tags do not",
			bodyHTML: "<p>first<br/>second <strong>bo</strong>ld</p>",
			want:     "first second bold",
		},
		{
			name:     "named and numeric entities decode to characters",
			bodyHTML: "<p>May update &mdash; what&#8217;s new &amp; next</p>",
			want:     "May update — what’s new & next",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := previewText(tt.bodyHTML); got != tt.want {
				t.Errorf("previewText() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("long body truncates at a word boundary with ellipsis", func(t *testing.T) {
		got := previewText(longWordy)
		if !strings.HasSuffix(got, "…") {
			t.Fatalf("previewText() = %q, want ellipsis suffix", got)
		}
		trimmed := strings.TrimSuffix(got, "…")
		if len([]rune(trimmed)) > preheaderMaxLen {
			t.Errorf("previewText() length = %d runes, want <= %d", len([]rune(trimmed)), preheaderMaxLen)
		}
		if strings.HasSuffix(trimmed, " ") || !strings.HasSuffix(trimmed, "lorem") && !strings.HasSuffix(trimmed, "ipsum") {
			t.Errorf("previewText() = %q, want cut on a whole word", got)
		}
	})
}

func TestEmailHTMLPreheader(t *testing.T) {
	t.Run("preview text appears hidden before the header chrome", func(t *testing.T) {
		html := EmailHTML(Chrome{
			Subject:     "May Update",
			BodyHTML:    "<p>Board election results &amp; upcoming events</p>",
			DisplayName: "CNCF",
		})
		preheaderIdx := strings.Index(html, "Board election results &amp; upcoming events")
		if preheaderIdx == -1 {
			t.Fatal("EmailHTML() missing preheader text")
		}
		headerIdx := strings.Index(html, "&middot; Newsletter")
		if headerIdx == -1 || preheaderIdx > headerIdx {
			t.Errorf("preheader at %d must precede header chrome at %d", preheaderIdx, headerIdx)
		}
		if !strings.Contains(html[:headerIdx], "display:none") {
			t.Error("preheader container must be hidden")
		}
		if !strings.Contains(html, "&nbsp;&zwnj;") {
			t.Error("preheader must pad with zero-width joiners so chrome does not bleed into the preview")
		}
	})

	t.Run("preheader escapes authored text", func(t *testing.T) {
		html := EmailHTML(Chrome{
			Subject:  "Update",
			BodyHTML: `<p>a <b>b</b> "quoted" body</p>`,
		})
		if !strings.Contains(html, `a b &quot;quoted&quot; body`) {
			t.Errorf("EmailHTML() preheader not escaped:\n%s", html[:400])
		}
	})

	t.Run("empty body omits the preheader div", func(t *testing.T) {
		html := EmailHTML(Chrome{Subject: "Update", BodyHTML: ""})
		if strings.Contains(html, "mso-hide:all") {
			t.Error("EmailHTML() must omit the preheader when the body has no text")
		}
	})
}
