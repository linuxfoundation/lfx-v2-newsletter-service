// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"fmt"
	"strconv"
	"strings"
)

// Per-block outer spacing.
//
// The authoring composer reserves two block-content keys — `_spacing_padding`
// and `_spacing_margin` — that hold CSS shorthand strings (e.g. "12px",
// "8px 16px"). In the composer canvas each TOP-LEVEL block is wrapped in a
// <div> carrying those as its `padding` and `margin`, so the two values stack
// into the visible gap around the block. The default value "0px" (or an
// absent/blank key) means "no spacing wrapper" — the block must then emit
// EXACTLY as it did before this feature existed.
//
// This file makes those keys affect the SENT email. Only top-level blocks carry
// spacing (a container's nested children do not), matching the composer, so the
// wrap happens once per top-level block at the render bind seam.
const (
	spacingPaddingKey = "_spacing_padding"
	spacingMarginKey  = "_spacing_margin"
	spacingDefault    = "0px"

	// spacingWrapperTag marks a synthetic node whose Children are one block's
	// bound layout nodes and whose "padding" attr holds the combined outer
	// spacing. translate()/writeBlock render it as a single <mj-wrapper>. The
	// value is deliberately not a real HTML/custom tag, so no authored template
	// can produce it and collide.
	spacingWrapperTag = "lfx-spacing-wrapper"
)

// applyBlockSpacing wraps one top-level block's bound nodes in a spacing
// wrapper node when its reserved `_spacing_padding` / `_spacing_margin` content
// keys request outer spacing. When both keys are absent/blank/"0px" it returns
// bound unchanged, so a block with no spacing emits byte-identically to before
// (the "0px = no wrapper" contract). The `_spacing_*` keys are read directly
// here and never bound into a template, so they cannot leak into the output.
func applyBlockSpacing(blk Block, bound []*node) []*node {
	padding := normalizeSpacing(blk.Content[spacingPaddingKey])
	margin := normalizeSpacing(blk.Content[spacingMarginKey])
	combined := combineSpacing(padding, margin)
	if combined == "" {
		return bound
	}
	return []*node{{
		Tag:      spacingWrapperTag,
		Attrs:    map[string]string{"padding": combined},
		Children: bound,
	}}
}

// normalizeSpacing mirrors the composer's spacingValue(): a non-string, blank,
// or "0px" value is treated as "no spacing" and returned as "". Anything else
// is the trimmed CSS shorthand.
func normalizeSpacing(raw any) string {
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	s = strings.TrimSpace(s)
	if s == "" || s == spacingDefault {
		return ""
	}
	return s
}

// combineSpacing folds the block's padding and margin into a single CSS padding
// shorthand for the mj-wrapper.
//
// Margin decision: MJML/email has no reliable outer `margin` — mj-wrapper
// exposes `padding` as the one dependable outer-spacing primitive, and nesting
// two mj-wrappers is rejected by the MJML compiler ("mj-wrapper cannot be used
// inside mj-wrapper"). So both values are mapped onto the wrapper's padding.
// This is also the most FAITHFUL mapping to the composer, where the block's
// wrapping <div> carries margin AND padding that visually stack into one gap:
// summing them component-wise (top/right/bottom/left) reproduces that same total
// gap. When only one value is set it is used verbatim. When the two shorthands
// can't be summed as pixels (mixed/other units — the composer only emits px),
// the padding value wins as the reliable primitive and the margin is dropped.
func combineSpacing(padding, margin string) string {
	switch {
	case padding == "" && margin == "":
		return ""
	case margin == "":
		return padding
	case padding == "":
		return margin
	}
	p, pok := expandPxShorthand(padding)
	m, mok := expandPxShorthand(margin)
	if !pok || !mok {
		return padding
	}
	var sum [4]int
	for i := range sum {
		sum[i] = p[i] + m[i]
	}
	return collapsePxShorthand(sum)
}

// expandPxShorthand parses a 1-4 value CSS length shorthand of pixel values into
// [top, right, bottom, left]. It reports false for any token that is not an
// integer pixel value (e.g. "1em", "auto", "calc(...)"), so callers can fall
// back instead of producing a wrong sum.
func expandPxShorthand(s string) ([4]int, bool) {
	fields := strings.Fields(s)
	vals := make([]int, 0, len(fields))
	for _, f := range fields {
		px, ok := parsePx(f)
		if !ok {
			return [4]int{}, false
		}
		vals = append(vals, px)
	}
	switch len(vals) {
	case 1:
		return [4]int{vals[0], vals[0], vals[0], vals[0]}, true
	case 2:
		return [4]int{vals[0], vals[1], vals[0], vals[1]}, true
	case 3:
		return [4]int{vals[0], vals[1], vals[2], vals[1]}, true
	case 4:
		return [4]int{vals[0], vals[1], vals[2], vals[3]}, true
	default:
		return [4]int{}, false
	}
}

// parsePx parses an integer pixel token ("16px") or a bare "0" into its integer
// value. Any other form is rejected.
func parsePx(tok string) (int, bool) {
	tok = strings.TrimSpace(tok)
	if tok == "0" {
		return 0, true
	}
	if !strings.HasSuffix(tok, "px") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(tok, "px"))
	if err != nil {
		return 0, false
	}
	return n, true
}

// collapsePxShorthand renders [top, right, bottom, left] back into the shortest
// equivalent CSS shorthand, following the standard 1/2/3/4-value collapse rules.
func collapsePxShorthand(v [4]int) string {
	top, right, bottom, left := v[0], v[1], v[2], v[3]
	switch {
	case top == right && right == bottom && bottom == left:
		return px(top)
	case top == bottom && right == left:
		return px(top) + " " + px(right)
	case right == left:
		return px(top) + " " + px(right) + " " + px(bottom)
	default:
		return px(top) + " " + px(right) + " " + px(bottom) + " " + px(left)
	}
}

// px formats an integer pixel value as a CSS length ("0" stays unit-less, matching
// CSS convention, though any value is valid).
func px(n int) string {
	if n == 0 {
		return "0"
	}
	return fmt.Sprintf("%dpx", n)
}
