// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package declarative

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	nethtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// urlAttrs are forwarded attributes whose value is a URL and must pass scheme
// validation before emission.
var urlAttrs = map[string]bool{"href": true, "src": true}

// allowedURLSchemes are the schemes permitted in a bound href/src. Anything else
// (javascript:, data:, vbscript:, …) is dropped so writer-supplied content can't
// inject an active-scheme URL into the rendered HTML — which is also returned
// over the API and may be shown in a future web "view online" surface where the
// email-client scheme stripping that normally neutralises this does not apply.
var allowedURLSchemes = map[string]bool{"http": true, "https": true, "mailto": true}

// placeholderSentinel matches a WHOLE-VALUE send-time sentinel such as
// %%UNSUBSCRIBE_URL%% — the entire href/src is the sentinel. Requiring an exact
// match (not a substring) prevents a writer value like "javascript:alert(1)%%"
// from masquerading as a sentinel to bypass scheme validation.
var placeholderSentinel = regexp.MustCompile(`^%%[A-Z0-9_]+%%$`)

// node is a resolved element in the bound tree. After binding, every node is
// either an element (Tag set, possibly with Children) or raw text/HTML (Tag
// empty, Raw set). Directive attributes (if/each/field) are consumed during
// binding and never appear in Attrs.
type node struct {
	Tag      string
	Attrs    map[string]string
	Children []*node
	// Raw holds literal text or pre-escaped HTML for text nodes. When Tag is
	// "" the node is a text node and Raw is emitted verbatim by the MJML
	// translator (already escaped or intentionally raw richtext).
	Raw string
}

// mustachePattern matches {{field}} / {{nested.field}} bindings.
var mustachePattern = regexp.MustCompile(`{{\s*([a-zA-Z0-9_.]+)\s*}}`)

// forwardedAttrs is the allowlist of attributes carried from a template into
// the bound tree. Everything else (including directives) is dropped.
//
// clicktracking is an inert SendGrid directive (value "off") that suppresses
// click-tracking rewrites on a link. Layout newsletters bypass email_chrome on
// send, so their compliance-footer opt-out links carry it here to keep opt-out
// clicks out of click_rate/top_links — matching the guarantee the legacy chrome
// path already provides.
var forwardedAttrs = map[string]bool{
	"class": true, "style": true, "href": true, "src": true,
	"alt": true, "target": true, "width": true, "height": true, "align": true,
	"clicktracking": true,
}

// bindBlock binds a single Block against its template, returning the resolved
// element nodes that represent the block's body. Nested child Blocks are
// recursed into wherever the template has <slot name="children" />.
func bindBlock(b Block, templates Templates) ([]*node, error) {
	tmpl, err := templates.block(b.BlockType)
	if err != nil {
		return nil, err
	}
	roots, err := parseTemplate(tmpl)
	if err != nil {
		return nil, fmt.Errorf("declarative: parse block %q: %w", b.BlockType, err)
	}
	ctx := bindCtx{content: b.Content, children: b.Blocks, templates: templates}
	return bindNodes(roots, ctx)
}

// bindCtx carries the data a template node resolves against.
type bindCtx struct {
	content   map[string]any
	children  []Block
	templates Templates
}

// bindNodes resolves a slice of parsed template nodes against ctx.
func bindNodes(nodes []*parsedNode, ctx bindCtx) ([]*node, error) {
	var out []*node
	for _, n := range nodes {
		resolved, err := bindNode(n, ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, resolved...)
	}
	return out, nil
}

// bindNode resolves one parsed template node, expanding directives. It may
// return zero nodes (if= false), one node, or many (each= loop).
func bindNode(n *parsedNode, ctx bindCtx) ([]*node, error) {
	// Text node: substitute {{mustache}} and escape.
	if n.Tag == "" && !n.isSlot && n.richtextField == "" {
		text := mustachePattern.ReplaceAllStringFunc(n.text, func(m string) string {
			field := mustacheField(m)
			return html.EscapeString(lookupString(ctx.content, field))
		})
		return []*node{{Raw: text}}, nil
	}

	// <slot name="children" />: render the block's nested child blocks here.
	if n.isSlot {
		if n.slotName != "children" {
			// Only "children" is meaningful inside a block template; other
			// slots (e.g. the wrapper "body" slot) are handled by the
			// assembler, not here. Render nothing.
			return nil, nil
		}
		// A <separator> child of the slot is rendered BETWEEN consecutive child
		// blocks (never before the first or after the last), giving slotted
		// children an inline, email-safe divider. Bind it fresh per gap so no
		// node is shared across the tree — translation mutates nodes in place.
		var separator []*parsedNode
		for _, c := range n.children {
			if c != nil && c.Tag == "separator" {
				separator = c.children
				break
			}
		}
		var out []*node
		for i, child := range ctx.children {
			if i > 0 && len(separator) > 0 {
				sep, err := bindNodes(separator, ctx)
				if err != nil {
					return nil, err
				}
				out = append(out, sep...)
			}
			rendered, err := bindBlock(child, ctx.templates)
			if err != nil {
				return nil, err
			}
			out = append(out, rendered...)
		}
		return out, nil
	}

	// <richtext field="x" />: emit the field's HTML verbatim (unescaped).
	if n.richtextField != "" {
		raw := lookupString(ctx.content, n.richtextField)
		rt := &node{Tag: "richtext", Attrs: bindAttrs(n.attrs, ctx.content), Raw: raw}
		return []*node{rt}, nil
	}

	// each="arrayfield": repeat this element per array item. Inside, the
	// item's fields become the binding context.
	if n.each != "" {
		items := lookupSlice(ctx.content, n.each)
		var out []*node
		for _, item := range items {
			itemCtx := ctx
			itemCtx.content = item
			// Clone the node without the each directive and bind it.
			clone := *n
			clone.each = ""
			resolved, err := bindNode(&clone, itemCtx)
			if err != nil {
				return nil, err
			}
			out = append(out, resolved...)
		}
		return out, nil
	}

	// if="field": skip the element when the field is empty.
	if n.ifField != "" && isEmpty(ctx.content, n.ifField) {
		return nil, nil
	}

	el := &node{Tag: n.Tag, Attrs: bindAttrs(n.attrs, ctx.content)}
	children, err := bindNodes(n.children, ctx)
	if err != nil {
		return nil, err
	}
	el.Children = children
	return []*node{el}, nil
}

// bindAttrs forwards allowlisted attributes, substituting {{mustache}} in
// their values. Values are kept RAW here: serialization escapes every
// attribute exactly once (writeAttr), so escaping at bind time would
// double-escape — a URL like ?a=1&b=2 would ship as &amp;amp; and the link
// would resolve with a broken amp;b parameter. URL attributes (href/src) are
// additionally gated through safeURL — a bound value with a non-allowlisted
// scheme (javascript:, data:, …) or an empty/relative value is dropped rather
// than emitted.
func bindAttrs(attrs map[string]string, content map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range attrs {
		if !forwardedAttrs[k] {
			continue
		}
		bound := mustachePattern.ReplaceAllStringFunc(v, func(m string) string {
			return lookupString(content, mustacheField(m))
		})
		// A whole-value deferred send-time sentinel (e.g. %%UNSUBSCRIBE_URL%%)
		// resolves to a server-generated URL at send time, so pass it through.
		// Any other URL-attr value must use a safe scheme.
		if urlAttrs[k] && !placeholderSentinel.MatchString(bound) && !safeURL(bound) {
			continue
		}
		out[k] = bound
	}
	return out
}

// safeURL reports whether a bound href/src value uses an allowed scheme. Empty
// and relative/scheme-less values are rejected (they don't resolve in email
// anyway); only http/https/mailto pass. The scheme is the leading token, so the
// check is unaffected by the HTML-escaping already applied to the value.
func safeURL(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" {
		return false
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	return allowedURLSchemes[strings.ToLower(u.Scheme)]
}

// mustacheField extracts the field path from a "{{ field }}" match.
func mustacheField(m string) string {
	inner := strings.TrimSpace(m[2 : len(m)-2])
	return inner
}

// lookupString resolves a (possibly dotted) field path to its string form.
// Non-string scalars are formatted with fmt; missing/nil values yield "".
func lookupString(content map[string]any, path string) string {
	v := lookup(content, path)
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		// JSON numbers decode into map[string]any as float64, and fmt's %v
		// switches float64 to exponent form at magnitudes >= 1e6 — so a count of
		// 1000000 would render as "1e+06" in the sent email. Format without an
		// exponent and drop any trailing zeros (-1 precision) so integers stay
		// integers and fractions keep their significant digits.
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// lookupSlice resolves a field path to a slice of string-keyed maps for each=
// iteration. Each item that is a map[string]any is yielded; other shapes are
// skipped.
func lookupSlice(content map[string]any, path string) []map[string]any {
	v := lookup(content, path)
	arr, ok := v.([]any)
	if !ok {
		// Also accept []map[string]any directly.
		if maps, ok2 := v.([]map[string]any); ok2 {
			return maps
		}
		return nil
	}
	var out []map[string]any
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// lookup resolves a dotted path against nested maps.
func lookup(content map[string]any, path string) any {
	if content == nil {
		return nil
	}
	parts := strings.Split(path, ".")
	var cur any = content
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

// isEmpty reports whether the if= field is considered empty (absent, nil,
// empty string, or empty slice).
func isEmpty(content map[string]any, path string) bool {
	v := lookup(content, path)
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		return len(t) == 0
	case []map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

// parsedNode is a node in the parsed (pre-binding) template tree. Directive
// attributes are lifted into typed fields; everything else stays in attrs.
type parsedNode struct {
	Tag           string
	attrs         map[string]string
	children      []*parsedNode
	text          string // for text nodes (Tag == "")
	ifField       string // if="..."
	each          string // each="..."
	isSlot        bool   // <slot ...>
	slotName      string // slot name attr
	richtextField string // <richtext field="...">
}

// customTags are the declarative-specific tags. They are renamed to an "x-"
// prefix before parsing so golang.org/x/net/html treats them as generic
// elements rather than applying HTML special-element semantics. Without this,
// HTML's void <link> and the <img>/<hr> void rules would mis-nest a custom
// <Link>…</Link>'s text content as a sibling. The prefix is stripped in
// convertNode.
var customTags = []string{
	"Section", "Row", "Column", "Text", "Heading",
	"Img", "Button", "Link", "Hr", "richtext", "slot",
}

// customTagPattern matches an opening or closing tag for any custom tag,
// case-insensitively, capturing the leading "/" (if any) and the rest of the
// tag.
var customTagPattern = func() *regexp.Regexp {
	names := strings.Join(customTags, "|")
	return regexp.MustCompile(`(?i)<(/?)(` + names + `)(\s|/|>)`)
}()

// prefixCustomTags rewrites <Section …> / </Section> etc. to <x-section …> /
// </x-section> so net/html parses them generically.
func prefixCustomTags(src string) string {
	return customTagPattern.ReplaceAllString(src, `<${1}x-${2}${3}`)
}

// selfClosedCustomTag matches a self-closing prefixed custom tag, e.g.
// <x-richtext field="body" /> or <x-hr style="…"/>.
var selfClosedCustomTag = regexp.MustCompile(`(?i)<(x-[a-z]+)((?:[^<>"]|"[^"]*")*?)/>`)

// expandSelfClosedCustomTags rewrites <x-foo … /> into <x-foo …></x-foo>.
// HTML5 parsing IGNORES the self-closing slash on unknown (non-void)
// elements, so a self-closed custom tag would otherwise stay open and
// swallow every following sibling as a child — a template author writing
// <richtext … /> followed by a <Text> would silently lose the Text. The
// shipped templates all place self-closed elements last, so nothing
// rendered wrong today; this removes the footgun for the next template.
func expandSelfClosedCustomTags(src string) string {
	return selfClosedCustomTag.ReplaceAllString(src, `<${1}${2}></${1}>`)
}

// parseTemplate strips the SCHEMA comment, prefixes custom tags, parses the
// markup with golang.org/x/net/html, and converts it into a parsedNode tree.
func parseTemplate(src string) ([]*parsedNode, error) {
	body := expandSelfClosedCustomTags(prefixCustomTags(stripSchema(src)))
	// Parse as a fragment so we don't get an implicit html/head/body wrapper.
	frag, err := nethtml.ParseFragment(strings.NewReader(body), &nethtml.Node{
		Type:     nethtml.ElementNode,
		Data:     "body",
		DataAtom: atom.Body,
	})
	if err != nil {
		return nil, err
	}
	var out []*parsedNode
	for _, f := range frag {
		if pn := convertNode(f); pn != nil {
			out = append(out, pn)
		}
	}
	return out, nil
}

// convertNode converts a net/html node into a parsedNode, dropping whitespace-
// only text nodes and comments.
func convertNode(n *nethtml.Node) *parsedNode {
	switch n.Type {
	case nethtml.TextNode:
		if strings.TrimSpace(n.Data) == "" {
			return nil
		}
		return &parsedNode{text: n.Data}
	case nethtml.ElementNode:
		// net/html lowercases tag names. Custom tags carry the "x-" prefix
		// added by prefixCustomTags; strip it so the translator sees
		// "section", "link", "richtext", "slot", etc. Inert HTML tags keep
		// their lowercased names.
		tag := strings.TrimPrefix(n.Data, "x-")
		pn := &parsedNode{Tag: tag, attrs: map[string]string{}}
		for _, a := range n.Attr {
			switch a.Key {
			case "if":
				pn.ifField = a.Val
			case "each":
				pn.each = a.Val
			case "field":
				if tag == "richtext" {
					pn.richtextField = a.Val
				}
			case "name":
				if tag == "slot" {
					pn.slotName = a.Val
				} else {
					pn.attrs[a.Key] = a.Val
				}
			default:
				pn.attrs[a.Key] = a.Val
			}
		}
		if tag == "slot" {
			pn.isSlot = true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if child := convertNode(c); child != nil {
				pn.children = append(pn.children, child)
			}
		}
		return pn
	default:
		return nil
	}
}
