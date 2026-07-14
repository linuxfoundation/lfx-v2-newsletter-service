// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package declarative renders a structured newsletter layout into email-safe
// HTML by binding declarative block/wrapper templates, translating the result
// into MJML, and compiling that MJML with mjml-go.
//
// The model is intentionally a single recursive Block: there is no separate
// "brick" type. A container/slot block nests child Blocks, and a template's
// <slot name="children" /> marks where those children render.
//
// Trust boundary: Content values originate from authenticated writers via the
// authoring UI. {{mustache}} substitutions are HTML-escaped; richtext fields
// are emitted verbatim (the authoring WYSIWYG whitelist is not a security
// sanitizer). The produced body_html is the COMPLETE outbound email document:
// the layout send path dispatches it directly, without wrapping it in the
// render.Chrome envelope, so verbatim richtext reaches recipients as-is. If
// content ever comes from a less privileged source, sanitize upstream — and
// any web "view online" surface must sanitize before serving it.
package declarative

// Layout is the structured newsletter: a wrapper plus the ordered top-level
// blocks that render inside the wrapper's <slot name="body" />.
type Layout struct {
	// WrapperKey selects which wrapper template to use. When empty, the "default"
	// wrapper key is used (see Templates.wrapper).
	WrapperKey string `json:"wrapper_key"`
	// Blocks are the ordered top-level blocks of the edition body.
	Blocks []Block `json:"blocks"`
}

// Block is a single recursive content node. BlockType selects the template
// (filename without extension). Content holds the field bindings consumed by
// {{mustache}}, richtext, if=, and each= directives. Blocks holds nested child
// blocks rendered into the template's <slot name="children" />.
type Block struct {
	BlockType string         `json:"block_type"`
	Content   map[string]any `json:"content"`
	Blocks    []Block        `json:"blocks"`
}
