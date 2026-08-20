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

// reconcileCardBorders post-processes mjml-compiled HTML so a bordered, rounded
// "card" renders with rounded corners.
//
// MJML renders a bordered, rounded mj-section with the border-radius on the
// outer <div>/<table> but the full border on the inner content <td>. A radius on
// the table does not clip a border on the cell, so the visible border has square
// corners (mj-column drops border/border-radius entirely, so the styling cannot
// simply move there). This copies the nearest enclosing border-radius onto each
// element that carries a FULL border but no radius of its own, and sets
// border-collapse:separate inline on radiused tables so the cell's radius takes
// effect (inline wins over MJML's head-level border-collapse reset). box-shadow
// is intentionally left alone: MJML sections carry none, and major clients
// (Gmail) strip box-shadow regardless.
func reconcileCardBorders(input string) string {
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
		// A full-bordered element with no radius of its own inherits the nearest
		// enclosing card radius so its border rounds.
		if cssProp(style, "border") != "" && radius == "" && len(radii) > 0 {
			newStyle = appendStyleDecl(newStyle, "border-radius", radii[len(radii)-1].radius)
		}
		// A radiused table needs border-collapse:separate for a child cell's
		// border-radius to render; MJML tables carry cellspacing="0" so this adds
		// no gaps.
		if radius != "" && tag == "table" && cssProp(style, "border-collapse") == "" {
			newStyle = appendStyleDecl(newStyle, "border-collapse", "separate")
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

// appendStyleDecl appends a "prop:val" declaration to an inline style string.
func appendStyleDecl(style, prop, val string) string {
	style = strings.TrimSpace(style)
	if style != "" && !strings.HasSuffix(style, ";") {
		style += ";"
	}
	return style + prop + ":" + val
}
