// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"strings"

	nethtml "golang.org/x/net/html"
)

// voidElements are HTML void elements. They never nest, so they must not shift
// the element-depth bookkeeping reconcileCardBorders uses to find the enclosing
// border-radius.
var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"source": true, "track": true, "wbr": true,
}

// cardGap is the vertical gap between stacked cards. MJML strips the card's
// authored margin, so without this the cards butt together (and the shadow has
// nowhere to show).
const cardGap = "24px"

// reconcileCardBorders post-processes mjml-compiled HTML so a bordered, rounded
// "card" renders like the reference source: rounded corners, a drop shadow, and a
// gap between stacked cards.
//
// MJML renders a bordered, rounded mj-section with the border-radius on the
// outer <div>/<table> but the full border on the inner content <td>. A radius on
// the table does not clip a border on the cell, so the visible border has square
// corners (mj-column drops border/border-radius entirely, so the styling cannot
// simply move there). It also strips box-shadow and margin from the section
// entirely. This post-process restores all three, matching what MJML dropped:
//   - copies the nearest enclosing border-radius onto each element that carries a
//     FULL border but no radius of its own (the card cell), and sets
//     border-collapse:separate inline on radiused tables so the cell's radius
//     takes effect (inline wins over MJML's head-level border-collapse reset);
//   - re-adds the card's AUTHORED drop shadow on its mj-section wrapper. The
//     shadow value (colour and offset) is captured pre-translation by
//     collectCardShadows and threaded in via cardShadows in document order: the
//     Nth rounded card wrapper gets the Nth entry. This preserves a card's real
//     shadow — a violet shadow on a black-bordered card, or a blue shadow on a
//     borderless card — instead of synthesising one from the border colour (which
//     dropped the authored colour and skipped borderless cards entirely);
//   - re-adds the inter-card bottom gap on that same card wrapper (a radiused,
//     horizontally-centred element with no border of its own).
//
// The shadow and gap use inline styles email clients honour where supported, and
// harmlessly ignore where not.
func reconcileCardBorders(input string, cardShadows []string) string {
	z := nethtml.NewTokenizer(strings.NewReader(input))
	var b strings.Builder
	// radii is a stack of the border-radius values of the currently-open
	// ancestors, tagged with the element depth at which each was opened.
	type frame struct {
		depth  int
		radius string
	}
	var radii []frame
	depth := 0
	// shadowIdx advances once per rounded card wrapper encountered, indexing
	// cardShadows in the same document order collectCardShadows produced it.
	shadowIdx := 0

	for {
		tt := z.Next()
		if tt == nethtml.ErrorToken {
			return b.String()
		}
		if tt == nethtml.EndTagToken {
			for len(radii) > 0 && radii[len(radii)-1].depth >= depth {
				radii = radii[:len(radii)-1]
			}
			if depth > 0 {
				depth--
			}
			b.Write(z.Raw())
			continue
		}
		if tt != nethtml.StartTagToken {
			b.Write(z.Raw())
			continue
		}

		name, hasAttr := z.TagName()
		tag := string(name)
		type attr struct{ key, val string }
		var attrs []attr
		style := ""
		for hasAttr {
			var k, v []byte
			k, v, hasAttr = z.TagAttr()
			key := string(k)
			val := string(v)
			if key == "style" {
				style = val
			}
			attrs = append(attrs, attr{key: key, val: val})
		}

		newStyle := style
		radius := cssProp(style, "border-radius")
		border := cssProp(style, "border")
		// A full-bordered element with no radius of its own IS the card cell: it
		// inherits the nearest enclosing card radius so its border rounds.
		if border != "" && radius == "" && len(radii) > 0 {
			newStyle = appendStyleDecl(newStyle, "border-radius", radii[len(radii)-1].radius)
		}
		// A radiused table needs border-collapse:separate for a child cell's
		// border-radius to render; MJML tables carry cellspacing="0" so this adds
		// no gaps.
		if radius != "" && tag == "table" && cssProp(style, "border-collapse") == "" {
			newStyle = appendStyleDecl(newStyle, "border-collapse", "separate")
		}
		// The card's mj-section wrapper — a radiused, horizontally-centred element
		// (margin: … auto) with no border of its own. Consume this card's authored
		// box-shadow (in document order) and re-add it here, along with the
		// inter-card bottom gap MJML stripped from the card's margin (so stacked
		// cards don't butt together and the shadow has room to show).
		if radius != "" && border == "" && marginIsAutoCentred(cssProp(style, "margin")) {
			shadow := ""
			if shadowIdx < len(cardShadows) {
				shadow = cardShadows[shadowIdx]
			}
			shadowIdx++
			if shadow != "" && cssProp(newStyle, "box-shadow") == "" {
				newStyle = appendStyleDecl(newStyle, "box-shadow", shadow)
			}
			if cssProp(newStyle, "margin-bottom") == "" {
				newStyle = appendStyleDecl(newStyle, "margin-bottom", cardGap)
			}
		}

		if newStyle != style {
			b.WriteString("<" + tag)
			for _, a := range attrs {
				val := a.val
				if a.key == "style" {
					val = newStyle
				}
				b.WriteString(" " + a.key + `="` + nethtml.EscapeString(val) + `"`)
			}
			b.WriteString(">")
		} else {
			b.Write(z.Raw())
		}

		if !voidElements[tag] {
			depth++
			if radius != "" {
				radii = append(radii, frame{depth: depth, radius: radius})
			}
		}
	}
}

// cssProp returns the value of a CSS property in an inline style string, matching
// the property name exactly (so "border" does not match "border-top"). Empty if
// absent.
func cssProp(style, prop string) string {
	for _, decl := range strings.Split(style, ";") {
		i := strings.IndexByte(decl, ':')
		if i < 0 {
			continue
		}
		if strings.TrimSpace(decl[:i]) == prop {
			return strings.TrimSpace(decl[i+1:])
		}
	}
	return ""
}

// borderColor extracts the colour from a CSS `border` shorthand
// ("<width> <style> <color>") — the last whitespace-separated token. Returns ""
// for a function-notation colour (rgb()/hsl(), which contain spaces) so the
// caller skips the shadow rather than emit a malformed value; the AAIF cards use
// hex, so this is not hit in practice.
func borderColor(border string) string {
	fields := strings.Fields(border)
	if len(fields) == 0 {
		return ""
	}
	color := fields[len(fields)-1]
	if strings.ContainsAny(color, "()") {
		return ""
	}
	return color
}

// marginIsAutoCentred reports whether a CSS `margin` value uses `auto` for its
// horizontal centring (e.g. "0px auto"), which marks an mj-section wrapper.
func marginIsAutoCentred(margin string) bool {
	return strings.Contains(margin, "auto")
}

// appendStyleDecl appends a "prop:val" declaration to an inline style string.
func appendStyleDecl(style, prop, val string) string {
	style = strings.TrimSpace(style)
	if style != "" && !strings.HasSuffix(style, ";") {
		style += ";"
	}
	return style + prop + ":" + val
}
