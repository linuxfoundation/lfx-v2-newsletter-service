// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

func TestMinifyHTML(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"table whitespace is dropped", "<table>\n  <tr>\n    <td>\n      Hi\n    </td>\n  </tr>\n</table>", "<table><tr><td>Hi</td></tr></table>"},
		{"a space between inline elements is kept", "<td>\n  <a href=\"x\">one</a>\n  <strong>two</strong>\n</td>", "<td><a href=\"x\">one</a> <strong>two</strong></td>"},
		{"a space inside text is collapsed, not removed", "<td>one   two\n three</td>", "<td>one two three</td>"},
		{"block tags keep one space, as richtext can make them inline", "<td><p style=\"display:inline\">a</p>\n  <p style=\"display:inline\">b</p></td>", "<td><p style=\"display:inline\">a</p> <p style=\"display:inline\">b</p></td>"},
		{"pre keeps its whitespace", "<td>\n<pre>  a\n    b  </pre>\n</td>", "<td><pre>  a\n    b  </pre></td>"},
		{"uppercase PRE keeps its whitespace", "<td><PRE>  a\n  b</PRE></td>", "<td><PRE>  a\n  b</PRE></td>"},
		{"pre after non-ASCII text keeps its whitespace", "<td>İİİİİİ<pre>İİİİİİ a      b</pre> c   d</td>", "<td>İİİİİİ<pre>İİİİİİ a      b</pre> c d</td>"},
		{"an element styled white-space:pre keeps its whitespace", "<td><div style=\"color:red; white-space: pre-wrap\">a\n   <div>b    c</div>\n d</div>   e</td>", "<td><div style=\"color:red; white-space: pre-wrap\">a\n   <div>b    c</div>\n d</div> e</td>"},
		{"style keeps its whitespace", "<head>\n<style type=\"text/css\">\n  .x { color: red; }\n</style>\n</head>", "<head><style type=\"text/css\">\n  .x { color: red; }\n</style></head>"},
		{"outlook conditional comments are unchanged", "<td>\n<!--[if mso | IE]><table\n   role=\"presentation\"><tr><td><![endif]-->\n<div>x</div>\n</td>", "<td><!--[if mso | IE]><table\n   role=\"presentation\"><tr><td><![endif]--> <div>x</div></td>"},
		{"attributes are unchanged, including a quoted >", "<td style=\"a:1;\n  b:2\" title=\"x > y\">\n z\n</td>", "<td style=\"a:1;\n  b:2\" title=\"x > y\">z</td>"},
		{"a literal < in text is not a tag", "<td>1 < 2 and  3</td>", "<td>1 < 2 and 3</td>"},
		{"img keeps the space around it", "<span>a</span>\n<img src=\"i\">\n<span>b</span>", "<span>a</span> <img src=\"i\"> <span>b</span>"},
		{"a trailing space in a protected element is kept", "<tr><td style=\"white-space:break-spaces\">a   </td></tr>", "<tr><td style=\"white-space:break-spaces\">a   </td></tr>"},
		{"a trailing space in style is kept", "<head><style>.x{}  </style></head>", "<head><style>.x{}  </style></head>"},
		{"an inline table keeps the spaces beside it", "<span>one</span> <table style=\"display:inline-table\"><tr><td>two</td></tr></table> <span>three</span>", "<span>one</span> <table style=\"display:inline-table\"><tr><td>two</td></tr></table> <span>three</span>"},
		{"a later white-space declaration is honoured", "<td><div style=\"white-space:normal;white-space:pre-wrap\">a   b</div></td>", "<td><div style=\"white-space:normal;white-space:pre-wrap\">a   b</div></td>"},
		{"a close tag inside a comment does not end a protected element", "<td><div style=\"white-space:pre-wrap\"><!-- </div> -->a   b</div>  c</td>", "<td><div style=\"white-space:pre-wrap\"><!-- </div> -->a   b</div> c</td>"},
		{"a close tag inside a quoted attribute does not end a protected element", "<td><div style=\"white-space:pre\"><span title=\"</div>\">a   b</span></div>  c</td>", "<td><div style=\"white-space:pre\"><span title=\"</div>\">a   b</span></div> c</td>"},
		{"an unterminated pre is copied unchanged", "<td><pre>  a\n  b", "<td><pre>  a\n  b"},
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
				switch tok.Type {
				case html.StartTagToken:
					inRaw++
				case html.EndTagToken:
					inRaw--
				}
			}
		}
	}
}

// TestMinifyHTMLIsLinearInRawTextElements guards against rescanning the rest of
// the document at every <pre>, which made a 400 KB document of them take seconds.
func TestMinifyHTMLIsLinearInRawTextElements(t *testing.T) {
	doc := strings.Repeat("<pre>  a  b  </pre>\n  ", 20_000)
	start := time.Now()
	out := minifyHTML(doc)
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("minifying %d bytes of <pre> took %v", len(doc), took)
	}
	if !strings.HasPrefix(out, "<pre>  a  b  </pre> <pre>") {
		t.Fatalf("unexpected output start: %q", out[:40])
	}
}
