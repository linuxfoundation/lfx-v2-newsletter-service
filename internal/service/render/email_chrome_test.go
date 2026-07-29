// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package render

import (
	"strings"
	"testing"
)

func TestInlineBodyStylesEmbeddedMedia(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		want     []string
		wantSame bool
	}{
		{
			name: "img gets responsive block style",
			body: `<img src="https://example.org/banner.png" alt="Banner">`,
			want: []string{
				`<img style="` + bodyTagStyles["img"] + `" src="https://example.org/banner.png" alt="Banner">`,
			},
		},
		{
			name: "self-closing img is styled and keeps attributes",
			body: `<img src="https://example.org/banner.png"/>`,
			want: []string{
				`<img style="` + bodyTagStyles["img"] + `" src="https://example.org/banner.png">`,
			},
		},
		{
			name: "inline code gets monospace chip style",
			body: `<p>Run <code>make test</code> before pushing.</p>`,
			want: []string{
				`<code style="` + bodyTagStyles["code"] + `">make test</code>`,
			},
		},
		{
			name: "pre gets block code panel style",
			body: `<pre class="ql-syntax" spellcheck="false">line one
    indented line</pre>`,
			want: []string{
				`<pre style="` + bodyTagStyles["pre"] + `" class="ql-syntax" spellcheck="false">line one
    indented line</pre>`,
			},
		},
		{
			name:     "author-supplied img style wins",
			body:     `<img style="width:120px;" src="https://example.org/logo.png">`,
			wantSame: true,
		},
		{
			name:     "author img style wins when a quoted attribute contains '>'",
			body:     `<img alt="a > b" style="width:120px;">`,
			wantSame: true,
		},
		{
			name: "img with '>' in a quoted attribute still gets the full style",
			body: `<img alt="a > b" src="https://example.org/x.png">`,
			want: []string{
				`<img style="` + bodyTagStyles["img"] + `" alt="a > b" src="https://example.org/x.png">`,
			},
		},
		{
			name: "'style=' inside a quoted value does not count as a style attribute",
			body: `<img alt="choose style=wide" src="https://example.org/x.png">`,
			want: []string{
				`<img style="` + bodyTagStyles["img"] + `" alt="choose style=wide" src="https://example.org/x.png">`,
			},
		},
		{
			name:     "author style attribute with spaces around '=' still wins",
			body:     `<img style = "width:120px;" src="https://example.org/logo.png">`,
			wantSame: true,
		},
		{
			name:     "uppercase STYLE attribute still wins",
			body:     `<img STYLE="width:120px;" src="https://example.org/logo.png">`,
			wantSame: true,
		},
		{
			name: "unquoted src value ending in '/' keeps the slash",
			body: `<img src=https://example.org/assets/>`,
			want: []string{
				`<img style="` + bodyTagStyles["img"] + `" src=https://example.org/assets/>`,
			},
		},
		{
			name:     "author style wins with an unquoted value ending in '/'",
			body:     `<img style="width:120px;" src=https://example.org/assets/>`,
			wantSame: true,
		},
		{
			name:     "author-supplied code style wins",
			body:     `<p><code style="color:red;">x</code></p>`,
			want:     []string{`<code style="color:red;">x</code>`},
			wantSame: false,
		},
		{
			name:     "author-supplied pre style wins",
			body:     `<pre style="background:black;">x</pre>`,
			want:     []string{`<pre style="background:black;">x</pre>`},
			wantSame: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := inlineBodyStyles(tc.body)
			if tc.wantSame && got != tc.body {
				t.Fatalf("inlineBodyStyles(%q) = %q, want input unchanged", tc.body, got)
			}
			for _, fragment := range tc.want {
				if !strings.Contains(got, fragment) {
					t.Fatalf("inlineBodyStyles(%q) = %q, missing fragment %q", tc.body, got, fragment)
				}
			}
		})
	}
}

func TestBodyTagStylesEmbeddedMediaRules(t *testing.T) {
	imgStyle := bodyTagStyles["img"]
	for _, rule := range []string{"max-width:100%", "height:auto", "display:block", "border-radius:8px"} {
		if !strings.Contains(imgStyle, rule) {
			t.Errorf("img style %q missing %q", imgStyle, rule)
		}
	}

	codeStyle := bodyTagStyles["code"]
	for _, rule := range []string{monoFontStack, "font-size:14px", "background-color:" + colorGray50, "border-radius:4px"} {
		if !strings.Contains(codeStyle, rule) {
			t.Errorf("code style %q missing %q", codeStyle, rule)
		}
	}

	preStyle := bodyTagStyles["pre"]
	for _, rule := range []string{monoFontStack, "background-color:" + colorGray50, "white-space:pre-wrap", "word-break:break-word"} {
		if !strings.Contains(preStyle, rule) {
			t.Errorf("pre style %q missing %q", preStyle, rule)
		}
	}
}

func TestInlineBodyStylesTagOrderCoversMap(t *testing.T) {
	seen := make(map[string]bool, len(inlineBodyStylesTagOrder))
	for _, tag := range inlineBodyStylesTagOrder {
		if seen[tag] {
			t.Errorf("tag order entry %q is duplicated", tag)
		}
		seen[tag] = true
		if _, ok := bodyTagStyles[tag]; !ok {
			t.Errorf("tag order entry %q has no style map entry", tag)
		}
	}
	for tag := range bodyTagStyles {
		if !seen[tag] {
			t.Errorf("style map entry %q missing from tag order", tag)
		}
	}
}

func TestEmailHTMLStylesEmbeddedMedia(t *testing.T) {
	html := EmailHTML(Chrome{
		Subject:  "Release notes",
		BodyHTML: `<p>Ship it.</p><img src="https://example.org/x.png"><pre>go build ./...</pre>`,
	})
	for _, fragment := range []string{
		`<img style="` + bodyTagStyles["img"] + `" src="https://example.org/x.png">`,
		`<pre style="` + bodyTagStyles["pre"] + `">go build ./...</pre>`,
	} {
		if !strings.Contains(html, fragment) {
			t.Fatalf("EmailHTML output missing fragment %q", fragment)
		}
	}
}
