// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"bytes"
	"strings"
)

// minifyHTML removes formatting whitespace from a rendered email without
// changing what a client displays. MJML's output is indented across many
// lines, which is roughly a seventh of a long edition's bytes. Smaller emails
// are clipped by Gmail (~102KB) less often and less deeply.
//
// It is deliberately conservative, rather than a general HTML minifier:
//   - Every tag's attributes (so every inline style), every comment (including
//     Outlook's <!--[if mso]> conditional blocks), the contents of <pre>,
//     <textarea>, <script> and <style>, and any element styled white-space:pre,
//     pre-wrap, pre-line or break-spaces are copied unchanged.
//   - Elsewhere a run of whitespace becomes one space, so the words and spaces a
//     reader sees are unchanged.
//   - That space is dropped only next to table-structure and document-head tags
//     (see minifyDropSpaceTags), where HTML does not render it. Next to div, p and
//     other block tags it stays as one space, because richtext can style those
//     display:inline, and then the space shows.
//
// It runs on MJML's output after reconcileCardBorders, which matches MJML's own
// formatting, so MJML's built-in minify stays off.
func minifyHTML(in string) string {
	out := make([]byte, 0, len(in))
	// wroteSpace is true while the last byte of out is a space this function
	// wrote in place of a whitespace run. Only that space may be dropped before a
	// table or head tag: a space copied from a comment or a protected element is
	// content and stays.
	wroteSpace := false
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
			wroteSpace = false
			i = stop
		case c == '<' && isTagStart(in[i:]):
			name, closing := tagName(in[i:])
			tagEnd := findTagEnd(in, i)
			if tagEnd < 0 {
				return string(append(out, in[i:]...))
			}
			if minifyDropSpaceTags[name] && wroteSpace {
				out = bytes.TrimSuffix(out, []byte{' '})
			}
			tag := in[i:tagEnd]
			out = append(out, tag...)
			wroteSpace = false
			i = tagEnd
			if !closing && !strings.HasSuffix(tag, "/>") && (minifyRawTextTags[name] || preservesWhitespace(tag)) {
				// Copy everything up to the matching close tag unchanged.
				end := matchingClose(in, i, name)
				if end < 0 {
					return string(append(out, in[i:]...))
				}
				out = append(out, in[i:end]...)
				i = end
				continue
			}
			if minifyDropSpaceTags[name] {
				i = skipSpace(in, i)
			}
		case isSpace(c):
			j := skipSpace(in, i)
			if j < len(in) && in[j] == '<' && isTagStart(in[j:]) {
				if name, _ := tagName(in[j:]); minifyDropSpaceTags[name] {
					i = j
					continue
				}
			}
			if len(out) > 0 && out[len(out)-1] != ' ' {
				out = append(out, ' ')
				wroteSpace = true
			}
			i = j
		default:
			out = append(out, c)
			wroteSpace = false
			i++
		}
	}
	return string(out)
}

// minifyDropSpaceTags are elements whose surrounding whitespace HTML never
// renders: the rows and cells inside a table, which MJML emits by the
// thousand, and the document head. table itself and block containers such as
// div and p are absent on purpose, as an inline style can make them inline
// (display:inline-table, display:inline), and then the space beside them shows.
// Whitespace inside a table is still dropped, next to its rows and cells.
// style is absent too: richtext can put a <style> between two words, and the
// head's own <style> already sits between head and meta tags, which drop it.
var minifyDropSpaceTags = map[string]bool{
	"html": true, "head": true, "body": true, "meta": true, "title": true, "link": true,
	"thead": true, "tbody": true, "tfoot": true, "tr": true, "td": true, "th": true,
	"!doctype": true,
}

// minifyRawTextTags hold content that must be copied byte for byte.
var minifyRawTextTags = map[string]bool{"pre": true, "textarea": true, "script": true, "style": true}

// preservesWhitespace reports whether a start tag's inline style keeps its
// content's whitespace (white-space: pre, pre-wrap, pre-line or break-spaces).
// Every white-space declaration is checked, and any that preserves whitespace
// counts, since a later declaration in the same style wins in the cascade.
func preservesWhitespace(tag string) bool {
	style := strings.ToLower(tag)
	for {
		k := strings.Index(style, "white-space")
		if k < 0 {
			return false
		}
		style = style[k+len("white-space"):]
		rest := strings.TrimLeft(style, " \t\n\r:")
		if strings.HasPrefix(rest, "pre") || strings.HasPrefix(rest, "break-spaces") {
			return true
		}
	}
}

// matchingClose returns the index of the close tag that ends the element named
// name whose content starts at in[from], counting nested elements of the same
// name, or -1. Names are compared without changing case, so byte offsets stay
// those of in.
//
// script, style and textarea content is raw text, so their close tag is the
// first "</name". Elsewhere (pre, and elements styled white-space:pre) the
// content is HTML: comments are skipped, and each tag is read whole, so text
// shaped like a tag inside a comment or a quoted attribute is not mistaken for
// the close tag.
func matchingClose(in string, from int, name string) int {
	raw := name == "script" || name == "style" || name == "textarea"
	depth := 1
	i := from
	for i < len(in) {
		if in[i] != '<' {
			i++
			continue
		}
		rest := in[i:]
		if !raw && strings.HasPrefix(rest, "<!--") {
			end := strings.Index(rest[4:], "-->")
			if end < 0 {
				return -1
			}
			i += 4 + end + 3
			continue
		}
		if strings.HasPrefix(rest, "</") && hasTagName(rest[2:], name) {
			depth--
			if depth == 0 {
				return i
			}
		} else if !raw && hasTagName(rest[1:], name) {
			depth++
		}
		if raw || !isTagStart(rest) {
			i++
			continue
		}
		end := findTagEnd(in, i)
		if end < 0 {
			return -1
		}
		i = end
	}
	return -1
}

// hasTagName reports whether s starts with the tag name, in any ASCII case,
// followed by the end of the name.
func hasTagName(s, name string) bool {
	if len(s) < len(name) || !strings.EqualFold(s[:len(name)], name) {
		return false
	}
	if len(s) == len(name) {
		return true
	}
	c := s[len(name)]
	return c == '>' || c == '/' || isSpace(c)
}

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
