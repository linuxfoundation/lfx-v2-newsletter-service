// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"bytes"
	"strings"
)

// minifyHTML removes formatting whitespace from a rendered email without
// changing what any client displays. MJML's output is indented across many
// lines, which is roughly a seventh of a long edition's bytes, and long
// editions run close to Gmail's ~102KB clipping point.
//
// It is deliberately conservative, rather than a general HTML minifier:
//   - <pre>, <textarea>, <script> and <style> contents, every comment (including
//     Outlook's <!--[if mso]> conditional blocks) and every tag's attributes are
//     copied unchanged. Inline styles stay on each element, as email clients
//     need them.
//   - Elsewhere a run of whitespace becomes one space, so the words and spaces a
//     reader sees are unchanged.
//   - That space is dropped only next to a block or table tag (see minifyBlockTags),
//     where HTML does not render it. Whitespace between inline elements, such as
//     </a> <strong>, is kept.
//
// It runs on MJML's output after reconcileCardBorders, which matches MJML's own
// formatting, so MJML's built-in minify stays off.
func minifyHTML(in string) string {
	out := make([]byte, 0, len(in))
	i := 0
	for i < len(in) {
		c := in[i]
		switch {
		case c == '<' && strings.HasPrefix(in[i:], "<!--"):
			end := strings.Index(in[i+4:], "-->")
			if end < 0 {
				return string(append(out, in[i:]...))
			}
			stop := i + 4 + end + 3
			out = append(out, in[i:stop]...)
			i = stop
		case c == '<' && isTagStart(in[i:]):
			name, closing := tagName(in[i:])
			tagEnd := findTagEnd(in, i)
			if tagEnd < 0 {
				return string(append(out, in[i:]...))
			}
			if minifyBlockTags[name] {
				out = bytes.TrimSuffix(out, []byte{' '})
			}
			out = append(out, in[i:tagEnd]...)
			i = tagEnd
			if !closing && minifyRawTextTags[name] {
				end := indexFold(in[i:], "</"+name)
				if end < 0 {
					return string(append(out, in[i:]...))
				}
				out = append(out, in[i:i+end]...)
				i += end
				continue
			}
			if minifyBlockTags[name] {
				i = skipSpace(in, i)
			}
		case isSpace(c):
			j := skipSpace(in, i)
			if j < len(in) && in[j] == '<' && isTagStart(in[j:]) {
				if name, _ := tagName(in[j:]); minifyBlockTags[name] {
					i = j
					continue
				}
			}
			if len(out) > 0 && out[len(out)-1] != ' ' {
				out = append(out, ' ')
			}
			i = j
		default:
			out = append(out, c)
			i++
		}
	}
	return string(out)
}

// minifyBlockTags are elements whose surrounding whitespace HTML does not render: the
// document structure, tables and block containers MJML emits. Inline elements
// (a, span, strong, em, img, br, ...) are absent on purpose.
var minifyBlockTags = map[string]bool{
	"html": true, "head": true, "body": true, "meta": true, "title": true, "link": true,
	"table": true, "thead": true, "tbody": true, "tfoot": true, "tr": true, "td": true, "th": true,
	"div": true, "p": true, "center": true, "ul": true, "ol": true, "li": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"blockquote": true, "hr": true, "style": true, "!doctype": true,
}

// minifyRawTextTags hold content that must be copied byte for byte.
var minifyRawTextTags = map[string]bool{"pre": true, "textarea": true, "script": true, "style": true}

// isTagStart reports whether s starts a tag rather than a literal "<" in text.
func isTagStart(s string) bool {
	if len(s) < 2 {
		return false
	}
	c := s[1]
	if c == '/' || c == '!' {
		return len(s) > 2
	}
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// tagName returns the lowercased element name of the tag at the start of s and
// whether it is a closing tag.
func tagName(s string) (string, bool) {
	i := 1
	closing := false
	if i < len(s) && s[i] == '/' {
		closing = true
		i++
	}
	start := i
	for i < len(s) && s[i] != '>' && s[i] != '/' && !isSpace(s[i]) {
		i++
	}
	return strings.ToLower(s[start:i]), closing
}

// findTagEnd returns the index just past the '>' that closes the tag starting
// at in[start], skipping any '>' inside a quoted attribute value, or -1.
func findTagEnd(in string, start int) int {
	var quote byte
	for i := start + 1; i < len(in); i++ {
		c := in[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return i + 1
		}
	}
	return -1
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\t' || c == '\r' || c == '\f'
}

func skipSpace(s string, i int) int {
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	return i
}

// indexFold is strings.Index, ignoring ASCII case.
func indexFold(s, substr string) int {
	return strings.Index(strings.ToLower(s), strings.ToLower(substr))
}
