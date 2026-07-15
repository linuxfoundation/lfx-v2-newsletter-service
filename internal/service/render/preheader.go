// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package render

import (
	"strings"

	"golang.org/x/net/html"
)

// previewMaxRunes is the rough preview length target. Mail clients show
// anywhere from ~35 to ~140 characters next to the subject; 140 fills the
// longest common preview line without wasting derivation effort.
const previewMaxRunes = 140

// preheaderPadPairs is how many "&nbsp;&zwnj;" pairs follow the preview text
// so clients that fill the preview line from the body stop at the padding
// instead of appending header/body copy after our text.
const preheaderPadPairs = 96

// preheaderStyle hides the preheader in the major clients: display:none for
// standards-compliant clients, max-height/max-width/overflow for clients that
// ignore display on divs, opacity/font-size/line-height so any leak is
// invisible, and mso-hide for Outlook desktop.
const preheaderStyle = "display:none;font-size:1px;line-height:1px;max-height:0;max-width:0;opacity:0;overflow:hidden;mso-hide:all;visibility:hidden;"

// previewDropTags are elements whose entire subtree is non-visible and must
// not leak into the preview text.
var previewDropTags = map[string]bool{
	"style":    true,
	"script":   true,
	"title":    true,
	"head":     true,
	"noscript": true,
	"template": true,
}

// previewBlockTags are tags that introduce a visual break; they contribute a
// space so "…end of list item</li><li>Next…" does not run the words
// together. Inline tags (strong, em, a, span…) contribute nothing so words
// split across them stay intact.
var previewBlockTags = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "ul": true, "ol": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"table": true, "thead": true, "tbody": true, "tfoot": true, "tr": true,
	"td": true, "th": true, "blockquote": true, "hr": true, "pre": true,
	"section": true, "article": true, "aside": true, "header": true,
	"footer": true, "figure": true, "figcaption": true, "dl": true,
	"dt": true, "dd": true, "form": true, "fieldset": true, "address": true,
	"nav": true, "main": true,
}

// previewZeroWidthCutset are zero-width characters trimmed from the EDGES of
// whitespace-delimited tokens only: zero-width space, zero-width non-joiner,
// zero-width joiner, and the zero-width no-break space / BOM. At a token edge
// they join nothing, so trimming them strips authored "&nbsp;&zwnj;…" padding
// runs (each zwnj sits between spaces) without touching joiners inside words,
// where ZWJ carries emoji sequences and ZWNJ is orthographically significant
// (e.g. in Persian).
const previewZeroWidthCutset = "\u200b\u200c\u200d\ufeff"

// previewText reduces the authored body HTML to a single plain-text line
// suitable for the inbox preview: non-visible subtrees dropped, block
// boundaries turned into spaces, remaining tags stripped, entities decoded,
// whitespace collapsed, and the result truncated to roughly previewMaxRunes
// with a trailing ellipsis when cut. Returns "" for effectively empty bodies.
//
// The body is parsed into a DOM with the HTML5 tree-construction rules — not
// just tokenized — so the preview walks the same tree a mail client renders:
// quoted attributes containing ">" (e.g. <p title="2 > 1">), omitted end tags
// (an unclosed <head> still yields the body text), a stray "/" on a non-void
// raw-text element (<style/>), comments, and raw-text content are all handled
// by the parser, and entities are decoded only in text nodes — literal
// "&lt;b&gt;" stays literal text.
func previewText(bodyHTML string) string {
	doc, err := html.Parse(strings.NewReader(bodyHTML))
	if err != nil {
		// html.Parse cannot fail on an in-memory reader; be defensive anyway.
		return ""
	}
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			b.WriteString(n.Data)
			return
		case html.ElementNode:
			if previewDropTags[n.Data] {
				return
			}
			if previewBlockTags[n.Data] {
				b.WriteByte(' ')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && previewBlockTags[n.Data] {
			b.WriteByte(' ')
		}
	}
	walk(doc)
	// strings.Fields splits on unicode.IsSpace — the full Unicode whitespace
	// set, not regexp's ASCII-only \s — so decoded entities such as &emsp;,
	// &thinsp;, and &#x2028; collapse to single spaces too. Zero-width
	// characters are trimmed at token edges only (see previewZeroWidthCutset)
	// so padding runs vanish while in-word joiners survive.
	fields := strings.Fields(b.String())
	kept := fields[:0]
	for _, f := range fields {
		if f = strings.Trim(f, previewZeroWidthCutset); f != "" {
			kept = append(kept, f)
		}
	}
	return truncatePreview(strings.Join(kept, " "), previewMaxRunes)
}

// truncatePreview cuts text to at most maxRunes runes, backing up to the last
// word boundary inside the cut and appending an ellipsis when truncation
// happened.
func truncatePreview(text string, maxRunes int) string {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	cut := string(runes[:maxRunes])
	if idx := strings.LastIndexByte(cut, ' '); idx > 0 {
		cut = cut[:idx]
	}
	return strings.TrimRight(cut, " ") + "…"
}

// renderPreheaderHTML emits the hidden preheader div injected immediately
// after <body>. Empty when the body yields no preview text — with nothing to
// control, an empty hidden div would only blank the preview line.
func renderPreheaderHTML(bodyHTML string) string {
	preview := previewText(bodyHTML)
	if preview == "" {
		return ""
	}
	return `<div style="` + preheaderStyle + `">` + escapeHTML(preview) +
		strings.Repeat("&nbsp;&zwnj;", preheaderPadPairs) + `</div>` + "\n"
}
