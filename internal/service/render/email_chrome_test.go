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
		{
			name:     "table cells get separators",
			bodyHTML: "<table><tr><td>Name</td><td>Status</td></tr><tr><th>Total</th><td>4</td></tr></table>",
			want:     "Name Status Total 4",
		},
		{
			name:     "entities decode exactly once",
			bodyHTML: "<p>escape it as &amp;mdash; in HTML</p>",
			want:     "escape it as &mdash; in HTML",
		},
		{
			name:     "attribute values with angle brackets do not leak",
			bodyHTML: `<p>See <a title="1 > 0" href="https://example.org">Hello</a> world</p>`,
			want:     "See Hello world",
		},
		{
			name:     "style and script payloads never surface",
			bodyHTML: "<style>p{color:red}</style><script>alert(1)</script><p>Hi members</p>",
			want:     "Hi members",
		},
		{
			name:     "semantic containers get separators by default",
			bodyHTML: "<section>Hello</section><section>members</section><dl><dt>Term</dt><dd>Def</dd></dl><figure><figcaption>Cap</figcaption></figure>",
			want:     "Hello members Term Def Cap",
		},
		{
			name:     "phrasing and unknown elements stay inline mid-word",
			bodyHTML: "<p>mem<label>ber</label>s and cus<x-chip>to</x-chip>m</p>",
			want:     "members and custom",
		},
		{
			name:     "self-closed script syntax still hides the payload",
			bodyHTML: "<script/>alert(1)</script><p>Hi members</p>",
			want:     "Hi members",
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
		if len([]rune(got)) > preheaderMaxLen {
			t.Errorf("previewText() length = %d runes including ellipsis, want <= %d", len([]rune(got)), preheaderMaxLen)
		}
		trimmed := strings.TrimSuffix(got, "…")
		if strings.HasSuffix(trimmed, " ") || !strings.HasSuffix(trimmed, "lorem") && !strings.HasSuffix(trimmed, "ipsum") {
			t.Errorf("previewText() = %q, want cut on a whole word", got)
		}
	})

	t.Run("unbroken long word stays within the cap including ellipsis", func(t *testing.T) {
		got := previewText("<p>" + strings.Repeat("a", 3*preheaderMaxLen) + "</p>")
		if !strings.HasSuffix(got, "…") {
			t.Fatalf("previewText() = %q, want ellipsis suffix", got)
		}
		if len([]rune(got)) > preheaderMaxLen {
			t.Errorf("previewText() length = %d runes including ellipsis, want <= %d", len([]rune(got)), preheaderMaxLen)
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
		if got := strings.Count(html, "&nbsp;&zwnj;"); got < preheaderMaxLen {
			t.Errorf("preheader padding = %d &nbsp;&zwnj; pairs, want >= %d so a short preview fills the whole client window and chrome does not bleed in", got, preheaderMaxLen)
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
