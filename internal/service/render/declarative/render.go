// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"context"
	"fmt"
	"time"

	mjml "github.com/Boostport/mjml-go"
)

// mjmlCompileTimeout bounds the mjml-go (wasm) compilation — a synchronous,
// CPU-heavy step. The HTTP request context carries no fixed deadline (the server
// only sets ReadHeaderTimeout, which does not bound the handler body), so without
// this a pathological layout could pin a core for as long as the client holds the
// connection. mjml.ToHTML honors context cancellation, so a service-owned
// deadline applied centrally here bounds EVERY render path (preview / create /
// update / test-send / send). A compile that exceeds it surfaces as a render
// failure (→ 422 at the callers), consistent with any other layout the emitter
// cannot process within budget. The ceiling is generous — a ~100 KB document
// compiles in well under a second — so only genuinely pathological input hits it.
const mjmlCompileTimeout = 15 * time.Second

// Render binds a structured newsletter Layout against the declarative
// templates, assembles the bound blocks into the wrapper's <slot name="body">,
// translates the result into MJML, and compiles it into email-safe HTML.
//
// wrapperContent supplies the runtime values the wrapper template binds against
// (e.g. edition.date, edition.unsubscribe_url) so its {{mustache}} fields and
// if= guards resolve. Pass nil when the wrapper needs no runtime data; its
// if=-guarded chrome rows then drop.
//
// The returned HTML is the complete email body for a LAYOUT newsletter — the
// layout send path dispatches it directly, with no email_chrome envelope. (On
// the legacy path the chrome layer still wraps an authored body_html; Render
// itself never adds the outer header/footer chrome.)
//
// ctx bounds the mjml-go (wasm) compile so a slow render can be canceled — e.g.
// a /render-preview request that hits its deadline.
func Render(ctx context.Context, layout Layout, templates Templates, wrapperContent map[string]any) (string, error) {
	mjmlDoc, err := RenderMJML(layout, templates, wrapperContent)
	if err != nil {
		return "", err
	}
	// Bound the wasm compile with a service-owned deadline (derived from the
	// caller's ctx, so an earlier client cancellation still wins). See
	// mjmlCompileTimeout.
	compileCtx, cancel := context.WithTimeout(ctx, mjmlCompileTimeout)
	defer cancel()
	out, err := mjml.ToHTML(compileCtx, mjmlDoc, mjml.WithMinify(false))
	if err != nil {
		// Compilation failures are predominantly client-driven — richtext or
		// content that yields invalid/oversized MJML, or the compile deadline —
		// so classify as an unrenderable layout (422). A translator bug on valid
		// input is a code defect caught by the render tests, not a runtime 500.
		return "", fmt.Errorf("%w: compile mjml: %v", ErrUnrenderableLayout, err)
	}
	// MJML splits a bordered rounded section's border (inner cell) from its
	// border-radius (outer table), squaring the corners — reconcile them so cards
	// render rounded.
	return reconcileCardBorders(out), nil
}

// RenderMJML performs binding, assembly, and MJML translation but stops before
// the mjml-go compile step. It is exported so callers (and tests) can inspect
// the generated MJML without invoking the wasm compiler.
//
// wrapperContent is the runtime binding context for the wrapper template; see
// Render. Pass nil when the wrapper needs no runtime data.
func RenderMJML(layout Layout, templates Templates, wrapperContent map[string]any) (string, error) {
	wrapperSrc, err := templates.wrapper(layout.WrapperKey)
	if err != nil {
		return "", err
	}
	wrapperRoots, err := parseTemplate(wrapperSrc)
	if err != nil {
		return "", fmt.Errorf("declarative: parse wrapper: %w", err)
	}

	// Bind every top-level block into one assembled body slice. Each block's
	// bound nodes are then wrapped in a per-block outer-spacing wrapper when its
	// reserved `_spacing_padding` / `_spacing_margin` content keys request it
	// (a no-op — byte-identical output — when both are absent/"0px").
	var body []*node
	for _, blk := range layout.Blocks {
		bound, bindErr := bindBlock(blk, templates)
		if bindErr != nil {
			return "", bindErr
		}
		body = append(body, applyBlockSpacing(blk, bound)...)
	}

	// Bind the wrapper itself (its own {{}}/if= against wrapperContent, e.g.
	// edition.* runtime fields) and splice the assembled body into its
	// <slot name="body" />.
	assembled, err := bindWrapper(wrapperRoots, body, wrapperContent)
	if err != nil {
		return "", err
	}

	// Give the semantic authoring classes their canonical styles before
	// translation (inline styles win per property).
	applyClassStyles(assembled)

	return translate(assembled), nil
}

// bindWrapper resolves the wrapper template, replacing the <slot name="body" />
// with the already-bound edition body. The wrapper's own bindings resolve
// against wrapperContent (e.g. {{edition.date}}, {{edition.unsubscribe_url}}),
// so if= guards on edition.* fields drop those chrome rows when the field is
// absent. A nil wrapperContent is treated as an empty map (every guarded row
// drops).
func bindWrapper(roots []*parsedNode, body []*node, wrapperContent map[string]any) ([]*node, error) {
	if wrapperContent == nil {
		wrapperContent = map[string]any{}
	}
	ctx := bindCtx{content: wrapperContent}
	var out []*node
	for _, r := range roots {
		resolved, err := bindWrapperNode(r, ctx, body)
		if err != nil {
			return nil, err
		}
		out = append(out, resolved...)
	}
	return out, nil
}

// bindWrapperNode is bindNode specialized for the wrapper: a
// <slot name="body" /> expands to the assembled edition body instead of nested
// child blocks.
//
// golang.org/x/net/html parses <slot name="body" /> as a non-void element, so
// any wrapper markup authored after it (e.g. the footer Section) is nested as
// the slot's children rather than left as siblings. Those children are the
// post-body chrome and must still render: the body is spliced in first, then
// the slot's own children are bound (against ctx) and appended after it.
func bindWrapperNode(n *parsedNode, ctx bindCtx, body []*node) ([]*node, error) {
	if n.isSlot && n.slotName == "body" {
		out := append([]*node{}, body...)
		for _, c := range n.children {
			resolved, err := bindWrapperNode(c, ctx, body)
			if err != nil {
				return nil, err
			}
			out = append(out, resolved...)
		}
		return out, nil
	}
	// if= guard on the wrapper element.
	if n.ifField != "" && isEmpty(ctx.content, n.ifField) {
		return nil, nil
	}
	// Text and richtext/slot(children) fall back to the standard binder.
	// NOTE: an `each=` wrapper element also falls back to bindNode, which does
	// not thread `body` — so a `<slot name="body" />` must NOT be nested inside
	// an `each=` element in a wrapper template (it would render empty). The
	// shipped default.html keeps the body slot at the top level.
	if n.Tag == "" || n.isSlot || n.richtextField != "" || n.each != "" {
		return bindNode(n, ctx)
	}
	el := &node{Tag: n.Tag, Attrs: bindAttrs(n.attrs, ctx.content)}
	for _, c := range n.children {
		resolved, err := bindWrapperNode(c, ctx, body)
		if err != nil {
			return nil, err
		}
		el.Children = append(el.Children, resolved...)
	}
	return []*node{el}, nil
}
