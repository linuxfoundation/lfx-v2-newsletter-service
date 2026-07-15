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

// previewDropTags are elements whose entire content is non-visible and must
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

// previewInvisibleReplacer removes decoded characters that render as nothing
// at all (the zero-width set) so they neither survive into the preview nor
// defeat whitespace collapsing. Blank-rendering whitespace (nbsp, em space,
// line separator, …) needs no entry here: unicode.IsSpace covers it during
// whitespace collapsing below.
var previewInvisibleReplacer = strings.NewReplacer(
	"\u200b", "", // zero-width space
	"\u200c", "", // zero-width non-joiner
	"\u200d", "", // zero-width joiner
	"\ufeff", "", // zero-width no-break space / BOM
)

// previewText reduces the authored body HTML to a single plain-text line
// suitable for the inbox preview: non-visible elements dropped, block
// boundaries turned into spaces, remaining tags stripped, entities decoded,
// whitespace collapsed, and the result truncated to roughly previewMaxRunes
// with a trailing ellipsis when cut. Returns "" for effectively empty bodies.
//
// Extraction walks x/net/html tokens rather than regexes so quoted
// attributes containing ">" (e.g. <p title="2 > 1">), comments, and raw-text
// elements are parsed the way a mail client would parse them, and entities
// are decoded only in text nodes — literal "&lt;b&gt;" stays literal text.
func previewText(bodyHTML string) string {
	var b strings.Builder
	z := html.NewTokenizer(strings.NewReader(bodyHTML))
	skip := 0 // depth of enclosing previewDropTags elements
tokens:
	for {
		switch tt := z.Next(); tt {
		case html.ErrorToken:
			// io.EOF or malformed input: keep whatever was extracted.
			break tokens
		case html.TextToken:
			if skip == 0 {
				b.Write(z.Text())
			}
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			tag := string(name) // the tokenizer lower-cases tag names
			switch {
			case previewDropTags[tag]:
				if tt == html.StartTagToken {
					skip++
				} else if tt == html.EndTagToken && skip > 0 {
					skip--
				}
			case previewBlockTags[tag]:
				b.WriteByte(' ')
			}
		}
	}
	out := previewInvisibleReplacer.Replace(b.String())
	// strings.Fields splits on unicode.IsSpace — the full Unicode whitespace
	// set, not regexp's ASCII-only \s — so decoded entities such as &emsp;,
	// &thinsp;, and &#x2028; collapse to single spaces too.
	out = strings.Join(strings.Fields(out), " ")
	return truncatePreview(out, previewMaxRunes)
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
