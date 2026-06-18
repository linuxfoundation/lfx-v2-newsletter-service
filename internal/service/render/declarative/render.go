// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"context"
	"fmt"

	mjml "github.com/Boostport/mjml-go"
)

// Render binds a structured newsletter Layout against the declarative
// templates, assembles the bound blocks into the wrapper's <slot name="body">,
// translates the result into MJML, and compiles it into email-safe HTML.
//
// wrapperContent supplies the runtime values the wrapper template binds against
// (e.g. edition.date, edition.unsubscribe_url) so its {{mustache}} fields and
// if= guards resolve. Pass nil when the wrapper needs no runtime data; its
// if=-guarded chrome rows then drop.
//
// The returned HTML is intended to populate render.Chrome.BodyHTML. Render does
// not add the outer email envelope (header/footer chrome) — that is the
// chrome layer's job.
func Render(layout Layout, templates Templates, wrapperContent map[string]any) (string, error) {
	mjmlDoc, err := RenderMJML(layout, templates, wrapperContent)
	if err != nil {
		return "", err
	}
	out, err := mjml.ToHTML(context.Background(), mjmlDoc, mjml.WithMinify(false))
	if err != nil {
		return "", fmt.Errorf("declarative: compile mjml: %w", err)
	}
	return out, nil
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

	// Bind every top-level block into one assembled body slice.
	var body []*node
	for _, blk := range layout.Blocks {
		bound, bindErr := bindBlock(blk, templates)
		if bindErr != nil {
			return "", bindErr
		}
		body = append(body, bound...)
	}

	// Bind the wrapper itself (its own {{}}/if= against wrapperContent, e.g.
	// edition.* runtime fields) and splice the assembled body into its
	// <slot name="body" />.
	assembled, err := bindWrapper(wrapperRoots, body, wrapperContent)
	if err != nil {
		return "", err
	}

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
