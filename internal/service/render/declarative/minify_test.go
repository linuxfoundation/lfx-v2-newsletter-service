// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestMinifyHTML(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"table whitespace is dropped", "<table>\n  <tr>\n    <td>\n      Hi\n    </td>\n  </tr>\n</table>", "<table><tr><td>Hi</td></tr></table>"},
		{"a space between inline elements is kept", "<p>\n  <a href=\"x\">one</a>\n  <strong>two</strong>\n</p>", "<p><a href=\"x\">one</a> <strong>two</strong></p>"},
		{"a space inside text is collapsed, not removed", "<td>one   two\n three</td>", "<td>one two three</td>"},
		{"pre keeps its whitespace", "<div>\n<pre>  a\n    b  </pre>\n</div>", "<div><pre>  a\n    b  </pre></div>"},
		{"style keeps its whitespace", "<head>\n<style type=\"text/css\">\n  .x { color: red; }\n</style>\n</head>", "<head><style type=\"text/css\">\n  .x { color: red; }\n</style></head>"},
		{"outlook conditional comments are unchanged", "<div>\n<!--[if mso | IE]><table\n   role=\"presentation\"><tr><td><![endif]-->\n<div>x</div>\n</div>", "<div><!--[if mso | IE]><table\n   role=\"presentation\"><tr><td><![endif]--><div>x</div></div>"},
		{"attributes are unchanged, including a quoted >", "<td style=\"a:1;\n  b:2\" title=\"x > y\">\n z\n</td>", "<td style=\"a:1;\n  b:2\" title=\"x > y\">z</td>"},
		{"a literal < in text is not a tag", "<td>1 < 2 and  3</td>", "<td>1 < 2 and 3</td>"},
		{"img keeps the space around it", "<span>a</span>\n<img src=\"i\">\n<span>b</span>", "<span>a</span> <img src=\"i\"> <span>b</span>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := minifyHTML(tc.in); got != tc.want {
				t.Errorf("minifyHTML()\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestMinifyHTMLKeepsAMemberEditionUnchanged renders a long aaif-member edition
// and checks that minifying changes nothing a client reads: every tag, attribute
// and comment is identical and in the same order, and the text reads the same
// words in the same order. It also checks the output gets smaller.
func TestMinifyHTMLKeepsAMemberEditionUnchanged(t *testing.T) {
	templates, err := LoadEmbeddedTemplateCached("aaif-member")
	if err != nil {
		t.Fatal(err)
	}
	items := make([]any, 0, 8)
	for i := 0; i < 8; i++ {
		items = append(items, map[string]any{
			"headline": fmt.Sprintf("Headline %d", i),
			"url":      "https://example.com/story",
			"body":     `<p>Some <strong>bold</strong> text, <a href="https://example.com/a">a link</a> <em>and</em> more.</p>`,
		})
	}
	blocks := []Block{{BlockType: "intro_letter", Content: map[string]any{
		"greeting": "Dear AAIF Members,",
		"body":     "<p>First paragraph with <a href=\"https://example.com\">a link</a>.</p><p>Second   paragraph.</p>",
	}}}
	for _, bt := range []string{"foundation_news", "member_innovation", "protocols_standards", "security", "funding"} {
		blocks = append(blocks, Block{BlockType: bt, Content: map[string]any{"items": items}})
	}
	layout := Layout{TemplateKey: "aaif-member", Blocks: blocks}
	wrapper := map[string]any{"edition": map[string]any{}}

	raw, err := renderUnminified(context.Background(), layout, templates, wrapper)
	if err != nil {
		t.Fatal(err)
	}
	min, err := Render(context.Background(), layout, templates, wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if len(min) >= len(raw) {
		t.Fatalf("minified %d bytes, unminified %d", len(min), len(raw))
	}
	t.Logf("unminified %d bytes, minified %d bytes (%.1f%% smaller)", len(raw), len(min), 100*float64(len(raw)-len(min))/float64(len(raw)))

	rawTags, rawWords := readTokens(t, raw)
	minTags, minWords := readTokens(t, min)
	if strings.Join(rawTags, "\n") != strings.Join(minTags, "\n") {
		t.Error("minifying changed a tag, attribute or comment")
	}
	if strings.Join(rawWords, " ") != strings.Join(minWords, " ") {
		t.Error("minifying changed the text")
	}
}

// readTokens returns every tag (with its attributes) and comment, and every word
// of text outside <style> and <script>.
func readTokens(t *testing.T, doc string) (tags, words []string) {
	t.Helper()
	z := html.NewTokenizer(strings.NewReader(doc))
	inRaw := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			return tags, words
		case html.TextToken:
			if inRaw == 0 {
				words = append(words, strings.Fields(string(z.Text()))...)
			}
		case html.CommentToken:
			tags = append(tags, "<!--"+string(z.Text())+"-->")
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken, html.DoctypeToken:
			tok := z.Token()
			tags = append(tags, tok.String())
			if tok.Data == "style" || tok.Data == "script" {
				if tok.Type == html.StartTagToken {
					inRaw++
				} else if tok.Type == html.EndTagToken {
					inRaw--
				}
			}
		}
	}
}
