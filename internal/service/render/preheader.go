// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package render

import (
	"html"
	"regexp"
	"strings"
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
var previewDropTags = []string{"style", "script", "title", "head", "noscript", "template"}

var previewDropRes = func() []*regexp.Regexp {
	res := make([]*regexp.Regexp, 0, len(previewDropTags))
	for _, tag := range previewDropTags {
		res = append(res, regexp.MustCompile(`(?is)<`+tag+`\b[^>]*>.*?</`+tag+`\s*>`))
	}
	return res
}()

var previewCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

// previewBlockBoundaryRe matches tags that introduce a visual break; they are
// replaced with a space so "…end of list item</li><li>Next…" does not run the
// words together. Inline tags (strong, em, a, span…) are stripped without a
// separator so words split across them stay intact.
var previewBlockBoundaryRe = regexp.MustCompile(`(?i)</?(?:p|div|br|li|ul|ol|h[1-6]|table|thead|tbody|tfoot|tr|td|th|blockquote|hr|pre|section|article|aside|header|footer|figure|figcaption|dl|dt|dd|form|fieldset|address|nav|main)\b[^>]*>`)

var previewWhitespaceRe = regexp.MustCompile(`\s+`)

// previewInvisibleReplacer normalizes decoded characters that render as
// blank (nbsp) or nothing at all (zero-width set) so they neither survive
// into the preview nor defeat whitespace collapsing.
var previewInvisibleReplacer = strings.NewReplacer(
	"\u00a0", " ", // no-break space
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
func previewText(bodyHTML string) string {
	out := previewCommentRe.ReplaceAllString(bodyHTML, " ")
	for _, re := range previewDropRes {
		out = re.ReplaceAllString(out, " ")
	}
	out = previewBlockBoundaryRe.ReplaceAllString(out, " ")
	out = tagRe.ReplaceAllString(out, "")
	// Decode after stripping so literal "&lt;b&gt;" text cannot become a tag.
	out = html.UnescapeString(out)
	out = previewInvisibleReplacer.Replace(out)
	out = previewWhitespaceRe.ReplaceAllString(out, " ")
	out = strings.TrimSpace(out)
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
