// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"fmt"
	"html"
	"sort"
	"strings"
)

// MJML structure note: mj-section may only contain mj-column (or mj-group),
// and mj-text / mj-image / mj-button must live inside an mj-column. The bound
// fixtures freely nest layout tags and inline content, so the translator
// normalizes that into valid MJML:
//   - Section / Row  -> mj-section (with an implicit single mj-column when the
//     section's content is inline rather than column-shaped)
//   - Column         -> mj-column
//   - Text / Heading -> mj-text
//   - Img            -> mj-image
//   - Button         -> mj-button
//   - Link           -> inline <a> inside the surrounding mj-text
//   - Hr             -> mj-divider
//   - richtext       -> mj-text with raw inner HTML
//   - inert HTML (div/span/p/strong/...) -> passed through inside an mj-text
//
// "inline" vs "block": inline tags (text, headings, links, inert HTML, images,
// buttons, dividers) are content that belongs in an mj-column. Block tags
// (section, row, column) are layout. When inline content appears directly
// under a section, the translator wraps it in mj-column > mj-text as needed.

// layout block-level tags (lowercased as net/html produces them).
var blockTags = map[string]bool{
	"section": true, "row": true, "column": true,
}

// inertTags are passthrough HTML tags allowed inside text content.
var inertTags = map[string]bool{
	"div": true, "span": true, "p": true, "strong": true, "em": true,
	"b": true, "i": true, "u": true, "br": true, "ul": true, "ol": true,
	"li": true, "small": true,
}

// translate converts the bound node tree (the assembled wrapper body) into an
// MJML document string.
func translate(roots []*node) string {
	var b strings.Builder
	b.WriteString("<mjml><mj-body>")
	for _, n := range roots {
		writeBlock(&b, n)
	}
	b.WriteString("</mj-body></mjml>")
	return b.String()
}

// writeBlock emits a top-level/layout node. Anything that is not a layout
// block is treated as loose inline content and wrapped in a section/column.
func writeBlock(b *strings.Builder, n *node) {
	if n == nil {
		return
	}
	switch n.Tag {
	case spacingWrapperTag:
		// Per-block outer spacing: emit one mj-wrapper carrying the combined
		// padding (see spacing.go) and render the block's own layout nodes
		// inside it. mj-wrapper is MJML's group-with-outer-spacing primitive and
		// may hold sibling mj-sections.
		b.WriteString("<mj-wrapper")
		writeAttr(b, "padding", n.Attrs["padding"])
		b.WriteString(">")
		for _, c := range n.Children {
			writeBlock(b, c)
		}
		b.WriteString("</mj-wrapper>")
	case "section", "row":
		writeSection(b, n)
	case "column":
		// A stray column at block level: wrap in a section.
		b.WriteString("<mj-section" + styleAttr(n, styleSection) + ">")
		writeColumn(b, n)
		b.WriteString("</mj-section>")
	default:
		// Loose inline content at the top level: wrap in section > column.
		b.WriteString("<mj-section><mj-column>")
		writeInlineAsText(b, []*node{n})
		b.WriteString("</mj-column></mj-section>")
	}
}

// writeSection emits one or more sibling mj-sections for a layout node.
//
// If the section's direct children are columns, they are emitted as
// side-by-side mj-columns within a single mj-section. Otherwise the section's
// content is a column-less run: it normally collapses into one implicit
// mj-column, BUT any nested Row/Section that directly holds multiple Column
// children is a "breakout" — it cannot nest inside the surrounding mj-section
// (MJML forbids mj-section in mj-section / mj-column), so it is emitted as its
// OWN sibling mj-section whose columns render side-by-side. Content before and
// after a breakout is split into separate sibling mj-sections so document
// order is preserved.
func writeSection(b *strings.Builder, n *node) {
	cols, others := partitionColumns(n.Children)
	if len(cols) > 0 {
		// Section is itself a row of columns: emit side-by-side directly.
		b.WriteString("<mj-section" + styleAttr(n, styleSection) + ">")
		for _, c := range cols {
			writeColumn(b, c)
		}
		// Any non-column siblings get their own trailing column so content is
		// not dropped.
		if len(others) > 0 {
			b.WriteString("<mj-column>")
			writeChildren(b, others)
			b.WriteString("</mj-column>")
		}
		b.WriteString("</mj-section>")
		return
	}

	// Column-less run: split it around any multi-column breakouts. The section's
	// own style is threaded down as the initial ancestor style so a breakout
	// lifted out of this section keeps its card chrome (see splitBreakouts).
	style := styleAttr(n, styleSection)
	segments := splitBreakouts(n.Children, styleOf(n))
	for _, seg := range segments {
		if seg.breakout != nil {
			writeBreakoutSection(b, seg.breakout)
			continue
		}
		if len(seg.inline) == 0 {
			continue
		}
		// The common authoring pattern <Section><div style="padding:…">…</div>
		// wraps ALL of a section's content in one padded layout div. That div
		// dissolves during translation (it cannot become an MJML element), so
		// without hoisting, its padding/background would be silently dropped —
		// promote its container-mappable declarations onto the mj-column that
		// replaces it.
		if len(seg.inline) == 1 && seg.inline[0] != nil && inertTags[seg.inline[0].Tag] && hasBlockDescendant(seg.inline[0]) {
			wrapper := seg.inline[0]
			b.WriteString("<mj-section" + style + "><mj-column" + styleAttr(wrapper, styleColumn) + ">")
			writeChildren(b, wrapper.Children)
			b.WriteString("</mj-column></mj-section>")
			continue
		}
		b.WriteString("<mj-section" + style + "><mj-column>")
		writeChildren(b, seg.inline)
		b.WriteString("</mj-column></mj-section>")
	}
}

// segment is one slice of a column-less section's content: either an ordered
// run of inline/flowing nodes (rendered in a single mj-column) or a single
// breakout node (a nested Row/Section of multiple Columns rendered as its own
// sibling mj-section).
type segment struct {
	inline   []*node
	breakout *node
}

// isBreakout reports whether n is a nested Row/Section that directly contains
// two or more Column children. Such a node must become its own sibling
// mj-section so its columns render side-by-side instead of stacked.
func isBreakout(n *node) bool {
	if n == nil || !blockTags[n.Tag] {
		return false
	}
	count := 0
	for _, c := range n.Children {
		if c != nil && c.Tag == "column" {
			count++
		}
	}
	return count >= 2
}

// splitBreakouts walks a column-less section's children in document order and
// produces a list of segments. A breakout node (see isBreakout) terminates the
// current inline run and becomes its own segment; everything else accumulates
// into inline runs. Single-column or column-less nested layout is left in the
// inline run so the existing dissolve/flatten behavior (writeChildren) applies.
//
// Breakouts can be wrapped in inert containers (e.g. the poll's
// <Section if><div><Row>…</Row></div></Section>), so a non-breakout block /
// inert node is recursed into: if it contains a breakout descendant, it is
// dissolved at this level so the breakout can surface as a sibling section,
// with its surrounding inline content preserved in order.
//
// ancestorStyle carries the container styling (card background / radius /
// padding) of every wrapper dissolved on the way down. It is layered UNDER a
// surfacing breakout's own style so the broken-out section keeps the chrome of
// the card it was lifted out of instead of rendering bare — without it, e.g.
// hot_take's poll Row loses the outer card background/radius and the wrapping
// div's padding.
func splitBreakouts(children []*node, ancestorStyle string) []segment {
	var segs []segment
	var run []*node
	flush := func() {
		if len(run) > 0 {
			segs = append(segs, segment{inline: run})
			run = nil
		}
	}
	for _, c := range children {
		switch {
		case c == nil:
			continue
		case isBreakout(c):
			flush()
			carryBreakoutStyle(c, ancestorStyle)
			segs = append(segs, segment{breakout: c})
		case containsBreakout(c):
			// A wrapper (inert div or single-column layout) that hides a
			// breakout deeper down. Dissolve it here so the breakout can reach
			// the section boundary, recursing to preserve order. Fold this
			// wrapper's own style into the ancestor chain so its background /
			// padding survives onto the breakout it hides.
			flush()
			inner := splitBreakouts(c.Children, mergeStyleDecls(ancestorStyle, styleOf(c)))
			segs = append(segs, inner...)
		default:
			run = append(run, c)
		}
	}
	flush()
	return segs
}

// styleOf returns a node's raw inline style, tolerating a nil node / Attrs.
func styleOf(n *node) string {
	if n == nil || n.Attrs == nil {
		return ""
	}
	return n.Attrs["style"]
}

// carryBreakoutStyle layers the dissolved-ancestor container styling UNDER a
// breakout node's own style (the node's own declarations still win per
// property), so the breakout's compiled mj-section carries the card chrome of
// the wrappers it was lifted out of. Non-container declarations are dropped
// later by styleAttr's per-element allowlist.
func carryBreakoutStyle(n *node, ancestorStyle string) {
	if n == nil || strings.TrimSpace(ancestorStyle) == "" {
		return
	}
	merged := mergeStyleDecls(ancestorStyle, styleOf(n))
	if n.Attrs == nil {
		n.Attrs = map[string]string{}
	}
	n.Attrs["style"] = merged
}

// containsBreakout reports whether n has a breakout descendant (a nested
// multi-column Row/Section) anywhere in its subtree.
func containsBreakout(n *node) bool {
	for _, c := range n.Children {
		if isBreakout(c) || containsBreakout(c) {
			return true
		}
	}
	return false
}

// writeBreakoutSection emits a nested multi-column Row/Section as its own
// sibling mj-section, one mj-column per Column child, so the columns render
// side-by-side. Non-column siblings (if any) get a trailing column so content
// is not dropped.
func writeBreakoutSection(b *strings.Builder, n *node) {
	cols, others := partitionColumns(n.Children)
	b.WriteString("<mj-section" + styleAttr(n, styleSection) + ">")
	for _, c := range cols {
		writeColumn(b, c)
	}
	if len(others) > 0 {
		b.WriteString("<mj-column>")
		writeChildren(b, others)
		b.WriteString("</mj-column>")
	}
	b.WriteString("</mj-section>")
}

// partitionColumns splits children into Column nodes and everything else.
func partitionColumns(children []*node) (cols, others []*node) {
	for _, c := range children {
		if c != nil && c.Tag == "column" {
			cols = append(cols, c)
		} else if c != nil {
			others = append(others, c)
		}
	}
	return cols, others
}

// writeColumn emits an mj-column and its content.
func writeColumn(b *strings.Builder, n *node) {
	b.WriteString("<mj-column" + styleAttr(n, styleColumn) + ">")
	writeChildren(b, n.Children)
	b.WriteString("</mj-column>")
}

// writeChildren emits the children of a column, grouping consecutive inline
// content into mj-text runs and breaking out for block-level MJML elements
// (images, buttons, dividers, standalone Text/Heading).
//
// Nested layout tags (section/row/column) cannot become nested mj-sections —
// MJML forbids mj-section inside mj-column. They are flattened into inert div
// passthrough inside the surrounding mj-text run, which keeps the authored
// markup's grouping (e.g. hot_take's poll Row of Columns) without violating
// MJML's structural rules. mj-button inside that run is hoisted out as its own
// block to stay valid; see writeChildren's button case.
func writeChildren(b *strings.Builder, children []*node) {
	var inlineRun []*node
	flush := func() {
		if len(inlineRun) > 0 {
			writeInlineAsText(b, inlineRun)
			inlineRun = nil
		}
	}
	for _, c := range children {
		if c == nil {
			continue
		}
		switch {
		case blockTags[c.Tag]:
			// Nested layout: hoist any block-level descendants (buttons,
			// images) out as their own MJML elements, rendering surrounding
			// inline content as inert divs.
			flush()
			writeNestedLayout(b, c)
		case c.Tag == "img":
			flush()
			writeImage(b, c)
		case c.Tag == "button":
			flush()
			writeButton(b, c)
		case c.Tag == "hr":
			flush()
			b.WriteString("<mj-divider" + styleAttr(c, styleDivider) + " />")
		case c.Tag == "text" || c.Tag == "heading":
			// A Text/Heading is its own mj-text block.
			flush()
			writeTextBlock(b, c)
		case hasBlockDescendant(c):
			// An inert container (e.g. a layout div) that wraps block-level
			// MJML content (a poll's buttons). It cannot be inlined into an
			// mj-text run, so dissolve it and recurse. Before dissolving, push
			// the container's own mappable styling down onto its block-level
			// children (which become sections/columns that CAN carry it), so a
			// padded wrapper div's padding is not silently dropped. The common
			// one-wrapper-per-section case is additionally hoisted onto the
			// mj-column in writeSection.
			flush()
			carryLinkHrefToImageChildren(c)
			pushContainerStyleToBlockChildren(c)
			writeChildren(b, c.Children)
		default:
			// richtext, links, inert HTML, raw text: accumulate.
			inlineRun = append(inlineRun, c)
		}
	}
	flush()
}

// carryLinkHrefToImageChildren propagates a dissolving link's href onto image
// descendants that lack their own href. A block-level <Img> inside a <Link>
// (e.g. logo_header's clickable logo) reaches the dissolve branch above, which
// emits only the image child and discards the link wrapper; without this the
// link destination would be lost. writeImage already renders href onto the
// mj-image (MJML wraps the image in an <a>), so carrying the link's href here
// preserves the clickable image. An image that already carries its own href
// keeps it — that is the more specific author intent.
func carryLinkHrefToImageChildren(container *node) {
	if container.Tag != "link" {
		return
	}
	href := container.Attrs["href"]
	if strings.TrimSpace(href) == "" {
		return
	}
	var walk func(n *node)
	walk = func(n *node) {
		for _, child := range n.Children {
			if child == nil {
				continue
			}
			if child.Tag == "img" {
				if child.Attrs == nil {
					child.Attrs = map[string]string{}
				}
				if strings.TrimSpace(child.Attrs["href"]) == "" {
					child.Attrs["href"] = href
				}
				continue
			}
			walk(child)
		}
	}
	walk(container)
}

// pushContainerStyleToBlockChildren merges a dissolving inert container's
// container-mappable style declarations (padding, background, radius) DOWN onto
// each of its block-level children before the container is discarded. The
// children become mj-section / mj-column / nested Section elements that carry
// these attributes, so a wrapper div's padding survives dissolution. The
// child's own declarations win per property (they are the more specific
// author intent). Inline children are untouched — they ride the text pipeline.
func pushContainerStyleToBlockChildren(container *node) {
	wrapperStyle := container.Attrs["style"]
	if strings.TrimSpace(wrapperStyle) == "" {
		return
	}
	for _, child := range container.Children {
		if child == nil {
			continue
		}
		// Only section/row children carry container padding onto their own
		// mj-section; other block children (buttons, images) have no
		// padding-bearing wrapper here, so pushing would be dropped anyway.
		if child.Tag == "section" || child.Tag == "row" {
			child.Attrs["style"] = mergeStyleDecls(wrapperStyle, child.Attrs["style"])
		}
	}
}

// blockLevelTags are nodes that must become their own MJML element (never
// inlined into an mj-text run): layout tags plus button/image/divider and
// standalone Text/Heading.
func isBlockLevel(n *node) bool {
	if n == nil {
		return false
	}
	if blockTags[n.Tag] {
		return true
	}
	switch n.Tag {
	case "img", "button", "hr", "text", "heading":
		return true
	default:
		return false
	}
}

// hasBlockDescendant reports whether n (typically an inert HTML container)
// contains any block-level descendant, meaning it cannot be safely inlined
// into an mj-text run.
func hasBlockDescendant(n *node) bool {
	for _, c := range n.Children {
		if isBlockLevel(c) || hasBlockDescendant(c) {
			return true
		}
	}
	return false
}

// writeNestedLayout flattens a layout node that appears inside a column.
// Because mj-section cannot nest in mj-column, the layout itself is dissolved:
// inline descendants accumulate into mj-text runs and block-level descendants
// (button/image/divider/standalone text) are emitted as their own MJML
// elements, preserving document order.
func writeNestedLayout(b *strings.Builder, n *node) {
	writeChildren(b, n.Children)
}

// writeTextBlock emits a single Text/Heading node as an mj-text element. The
// authored style is preserved: padding and color are promoted onto mj-text
// (styleAttr), and the remaining declarations ride an inner div that MJML
// passes through verbatim — without it, font-size, line-height, and the rest
// of the authored text presentation silently fell back to MJML defaults.
func writeTextBlock(b *strings.Builder, n *node) {
	b.WriteString("<mj-text" + styleAttr(n, styleText) + ">")
	inner := textInnerStyle(n)
	if inner != "" {
		b.WriteString(`<div style="` + html.EscapeString(inner) + `">`)
	}
	if n.Tag == "heading" {
		b.WriteString("<h2 style=\"margin:0\">")
		writeInline(b, n.Children)
		b.WriteString("</h2>")
	} else {
		writeInline(b, n.Children)
	}
	if inner != "" {
		b.WriteString("</div>")
	}
	b.WriteString("</mj-text>")
}

// writeInlineAsText wraps a run of inline nodes in one mj-text element. A
// richtext node carries its own raw HTML.
//
// A run that is exactly one styled richtext element keeps its authored
// presentation: padding and color are promoted onto mj-text, and the
// remaining declarations (font-size, line-height, font-family, …) ride an
// inner div wrapping the raw HTML — richtext previously emitted verbatim and
// its style attribute was silently dropped, so blocks like intro_paragraph
// rendered at MJML defaults.
func writeInlineAsText(b *strings.Builder, nodes []*node) {
	if len(nodes) == 1 && nodes[0] != nil && nodes[0].Tag == "richtext" && strings.TrimSpace(nodes[0].Attrs["style"]) != "" {
		n := nodes[0]
		b.WriteString("<mj-text" + styleAttr(n, styleText) + ">")
		if inner := textInnerStyle(n); inner != "" {
			b.WriteString(`<div style="` + html.EscapeString(inner) + `">`)
			b.WriteString(n.Raw)
			b.WriteString("</div>")
		} else {
			b.WriteString(n.Raw)
		}
		b.WriteString("</mj-text>")
		return
	}
	b.WriteString("<mj-text>")
	writeInline(b, nodes)
	b.WriteString("</mj-text>")
}

// writeInline emits inline content (text, richtext, links, inert HTML) without
// opening a new MJML block.
func writeInline(b *strings.Builder, nodes []*node) {
	for _, n := range nodes {
		if n == nil {
			continue
		}
		switch {
		case n.Tag == "":
			// Raw text or richtext-less raw node.
			b.WriteString(n.Raw)
		case n.Tag == "richtext":
			// SECURITY: richtext is emitted verbatim (writer-trusted WYSIWYG HTML,
			// not a sanitizer — see model.go trust boundary). Safe for email, but
			// the derived body_html MUST be sanitized before any web "view online"
			// surface serves it (that follow-up owns the sanitization).
			b.WriteString(n.Raw)
		case n.Tag == "link":
			writeLink(b, n)
		case n.Tag == "text" || n.Tag == "heading":
			// Nested Text/Heading inside inline context: render as a span/div.
			tag := "div"
			b.WriteString("<" + tag + htmlAttrs(n) + ">")
			writeInline(b, n.Children)
			b.WriteString("</" + tag + ">")
		case inertTags[n.Tag]:
			writeInert(b, n)
		default:
			// Unknown inline tag: render children only, so content survives.
			writeInline(b, n.Children)
		}
	}
}

// writeInert passes an allowlisted inert HTML tag through with its forwarded
// attributes.
func writeInert(b *strings.Builder, n *node) {
	if n.Tag == "br" {
		b.WriteString("<br />")
		return
	}
	b.WriteString("<" + n.Tag + htmlAttrs(n) + ">")
	writeInline(b, n.Children)
	b.WriteString("</" + n.Tag + ">")
}

// writeLink emits an inline anchor.
func writeLink(b *strings.Builder, n *node) {
	b.WriteString("<a" + htmlAttrs(n) + ">")
	writeInline(b, n.Children)
	b.WriteString("</a>")
}

// writeAttr appends a single ` name="value"` attribute with the value
// HTML-escaped. It is a no-op when value is empty.
func writeAttr(b *strings.Builder, name, value string) {
	if value == "" {
		return
	}
	b.WriteString(" ")
	b.WriteString(name)
	b.WriteString(`="`)
	b.WriteString(html.EscapeString(value))
	b.WriteString(`"`)
}

// writeImage emits an mj-image. src/alt/width carry across; remaining styling
// is dropped (MJML controls image layout via its own attributes).
func writeImage(b *strings.Builder, n *node) {
	b.WriteString("<mj-image")
	writeAttr(b, "src", n.Attrs["src"])
	writeAttr(b, "alt", n.Attrs["alt"])
	writeAttr(b, "width", n.Attrs["width"])
	writeAttr(b, "href", n.Attrs["href"])
	b.WriteString(" />")
}

// writeButton emits an mj-button carrying href. Presentational background /
// color are mapped from the authored inline style onto mj-button's own
// attributes so they survive MJML strict validation.
func writeButton(b *strings.Builder, n *node) {
	b.WriteString("<mj-button")
	writeAttr(b, "href", n.Attrs["href"])
	for _, m := range buttonStyleMappings(n.Attrs["style"]) {
		writeAttr(b, m.attr, m.val)
	}
	b.WriteString(">")
	writeInline(b, n.Children)
	b.WriteString("</mj-button>")
}

// styleMapping is one MJML attribute derived from an inline CSS declaration.
type styleMapping struct {
	attr string
	val  string
}

// buttonStyleMappings maps a subset of inline CSS onto mj-button attributes.
// MJML strict mode rejects an arbitrary inline `style` on mj-button, so only
// recognized declarations are forwarded; the rest are dropped.
func buttonStyleMappings(style string) []styleMapping {
	if style == "" {
		return nil
	}
	cssToAttr := map[string]string{
		"background-color": "background-color",
		"color":            "color",
		"border-radius":    "border-radius",
		"font-weight":      "font-weight",
		"padding":          "inner-padding",
	}
	var out []styleMapping
	for _, decl := range strings.Split(style, ";") {
		parts := strings.SplitN(decl, ":", 2)
		if len(parts) != 2 {
			continue
		}
		prop := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if attr, ok := cssToAttr[prop]; ok && val != "" {
			out = append(out, styleMapping{attr: attr, val: val})
		}
	}
	return out
}

// classStyleDefs are the canonical inline-style definitions for the template
// authoring classes (card, eyebrow, title, body, ...). The declarative
// templates ship no stylesheet — these classes were authored as semantic
// markers — so without this mapping they had no effect on the compiled email.
// applyClassStyles merges each definition UNDER the element's inline style
// (inline declarations win per property), after which the existing
// styleAttr / textInnerStyle pipeline carries them into the output.
var classStyleDefs = map[string]string{
	"card":        "background-color:#ffffff;border-radius:8px;padding:5px 0",
	"eyebrow":     "font-family:Arial,'Helvetica Neue',Helvetica,sans-serif;font-size:12px;letter-spacing:1px;text-transform:uppercase;color:#6b7c93;padding:14px 0 2px",
	"title":       "font-family:Arial,'Helvetica Neue',Helvetica,sans-serif;font-size:22px;line-height:1.3;font-weight:bold;color:#222222;padding:4px 0 8px",
	"brick-title": "font-family:Arial,'Helvetica Neue',Helvetica,sans-serif;font-size:18px;line-height:1.3;font-weight:bold;color:#222222;padding:4px 0 6px",
	"body":        "font-family:Arial,'Helvetica Neue',Helvetica,sans-serif;font-size:15px;line-height:1.5;color:#555555",
	"divider":     "border-color:#e2e8f0;border-width:1px;padding:8px 0",
	"link":        "color:#4086c6;text-decoration:underline",
}

// mergeStyleDecls layers override's declarations on top of base's, keeping
// first-seen property order and letting the override win per property.
func mergeStyleDecls(base, override string) string {
	var order []string
	vals := map[string]string{}
	for _, css := range []string{base, override} {
		for _, decl := range strings.Split(css, ";") {
			parts := strings.SplitN(decl, ":", 2)
			if len(parts) != 2 {
				continue
			}
			prop := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			if prop == "" || val == "" {
				continue
			}
			if _, seen := vals[prop]; !seen {
				order = append(order, prop)
			}
			vals[prop] = val
		}
	}
	var b strings.Builder
	for i, prop := range order {
		if i > 0 {
			b.WriteString(";")
		}
		b.WriteString(prop + ":" + vals[prop])
	}
	return b.String()
}

// applyClassStyles walks a bound tree merging each node's class-derived
// declarations under its inline style, so the semantic authoring classes
// take visual effect. Unknown classes are ignored. Runs once per render,
// before translation.
func applyClassStyles(nodes []*node) {
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if cls := n.Attrs["class"]; cls != "" {
			var classCSS string
			for _, token := range strings.Fields(cls) {
				if def, ok := classStyleDefs[token]; ok {
					if classCSS != "" {
						classCSS += ";"
					}
					classCSS += def
				}
			}
			if classCSS != "" {
				n.Attrs["style"] = mergeStyleDecls(classCSS, n.Attrs["style"])
			}
		}
		applyClassStyles(n.Children)
	}
}

// styleKind selects which MJML element's legal-attribute set styleAttr
// promotes onto. MJML strict mode rejects an arbitrary `style` attribute on
// mj-section / mj-column / mj-text, and each element accepts a DIFFERENT set
// of styling attributes (e.g. text-align is legal on mj-section and mj-text
// but illegal on mj-column), so the allowlist is per element.
type styleKind int

const (
	styleText styleKind = iota
	styleSection
	styleColumn
	styleDivider
)

// styleAllow maps each style kind to the CSS declarations that MJML accepts
// as native attributes on that element. Everything else is dropped for
// containers; for text the remainder rides the inner styled div (see
// writeTextBlock / writeInlineAsText). Classes cannot be mapped at all: the
// declarative templates ship no stylesheet, so inline styles are the only
// presentation carrier.
var styleAllow = map[styleKind]map[string]bool{
	styleText:    {"color": true, "padding": true},
	styleSection: {"background-color": true, "padding": true, "border-radius": true, "text-align": true},
	styleColumn:  {"background-color": true, "padding": true, "border-radius": true, "vertical-align": true},
	styleDivider: {"padding": true, "border-color": true, "border-width": true, "width": true},
}

// styleAttr promotes a node's allowlisted inline CSS declarations onto the
// MJML element as native attributes, so authored presentation (padding,
// background, radius, text color) reaches the compiled email instead of
// silently falling back to MJML defaults.
func styleAttr(n *node, kind styleKind) string {
	if n == nil {
		return ""
	}
	allow := styleAllow[kind]
	var b strings.Builder
	for _, decl := range strings.Split(n.Attrs["style"], ";") {
		parts := strings.SplitN(decl, ":", 2)
		if len(parts) != 2 {
			continue
		}
		prop := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if val == "" || !allow[prop] {
			continue
		}
		fmt.Fprintf(&b, " %s=%q", prop, html.EscapeString(val))
	}
	return b.String()
}

// textInnerStyle returns the declarations writeTextBlock and styled richtext
// runs carry on the inner div: everything except padding, which styleAttr
// already promoted onto mj-text itself (keeping it on both would apply it
// twice).
func textInnerStyle(n *node) string {
	var kept []string
	for _, decl := range strings.Split(n.Attrs["style"], ";") {
		parts := strings.SplitN(decl, ":", 2)
		if len(parts) != 2 {
			continue
		}
		prop := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if val == "" || prop == "padding" {
			continue
		}
		kept = append(kept, prop+":"+val)
	}
	return strings.Join(kept, ";")
}

// htmlAttrs renders forwarded attributes for an inline HTML element in a
// deterministic order.
func htmlAttrs(n *node) string {
	if len(n.Attrs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(n.Attrs))
	for k := range n.Attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		writeAttr(&b, k, n.Attrs[k])
	}
	return b.String()
}
