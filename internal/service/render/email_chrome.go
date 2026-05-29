// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package render builds the outer HTML/text envelope wrapped around the
// authored newsletter body.
//
// Trust boundary: BodyHTML is populated by authenticated writers via the
// authoring UI. There is no programmatic HTML sanitizer (e.g. DOMPurify) here.
// The authoring side's WYSIWYG format whitelist is NOT a security sanitizer.
// If we ever accept body content from a less privileged source, sanitize
// upstream of this call.
package render

import (
	"regexp"
	"strings"
)

// Chrome carries everything the renderer needs to wrap a body in the outer
// envelope.
type Chrome struct {
	Subject string
	// BodyHTML is interpolated verbatim into the envelope after the per-tag
	// inline-style pass. See the package trust boundary comment.
	BodyHTML    string
	DisplayName string
	// LogoURL is optional. When empty the header omits the logo cell and the
	// header renders text-only — there is no NATS subject for project logo
	// today, so this is typically empty for now.
	LogoURL string
	// IncludeComplianceFooter is true for real recipient-facing sends.
	// Test sends and previews keep it false.
	IncludeComplianceFooter bool
	// Required when IncludeComplianceFooter is true.
	EDName       string
	EDReplyEmail string
	// UnsubscribeURL is the per-recipient one-click opt-out link. The send
	// orchestrator passes a placeholder here and substitutes the real URL
	// per recipient inside the fan-out loop. When empty the footer falls
	// back to the legacy "reply with UNSUBSCRIBE" copy so test sends and
	// misconfigured environments still emit valid HTML.
	UnsubscribeURL string
}

// LFX brand colors used by the email chrome. Mirrored from
// `packages/shared/src/constants/design-tokens.constants.ts`. Hard-coded here
// because Go has no equivalent design-token import and the values rarely
// change.
const (
	colorWhite   = "#FFFFFF"
	colorBlue50  = "#EFF6FF"
	colorBlue500 = "#3B82F6"
	colorBlue600 = "#2563EB"
	colorGray50  = "#F9FAFB"
	colorGray200 = "#E5E7EB"
	colorGray400 = "#9CA3AF"
	colorGray500 = "#6B7280"
	colorGray700 = "#374151"
	colorGray800 = "#1F2937"
	colorGray900 = "#111827"
)

// Email clients reset font-family on every cell; declare the stack on every
// cell that hosts text so Outlook desktop doesn't fall back to Times.
const fontStack = `-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen-Sans, Ubuntu, Cantarell, 'Helvetica Neue', sans-serif`

// Per-tag inline styles applied to the authored body so Gmail/Outlook render
// header underlines, blockquotes, branded links etc. — Gmail strips <style>
// blocks so each tag has to carry its own `style=` attribute. Mirrors the
// in-app preview's component SCSS.
var bodyTagStyles = map[string]string{
	"p":          "margin:0 0 14px;line-height:1.65;",
	"h2":         "color:" + colorGray900 + ";font-size:22px;font-weight:700;line-height:1.3;letter-spacing:-0.01em;margin:32px 0 12px;padding-bottom:8px;border-bottom:1px solid " + colorGray200 + ";",
	"h3":         "color:" + colorGray700 + ";font-size:17px;font-weight:600;line-height:1.4;margin:24px 0 8px;",
	"ul":         "margin:12px 0 16px;padding-left:32px;list-style-type:disc;",
	"ol":         "margin:12px 0 16px;padding-left:32px;list-style-type:decimal;",
	"li":         "margin:0 0 8px;line-height:1.6;",
	"blockquote": "margin:16px 0;padding:12px 16px;border-left:3px solid " + colorBlue500 + ";background-color:" + colorBlue50 + ";border-radius:0 4px 4px 0;color:" + colorGray800 + ";font-style:normal;",
	"hr":         "border:0;border-top:1px dashed " + colorGray200 + ";margin:24px 0;",
	"a":          "color:" + colorBlue500 + ";text-decoration:underline;",
	"strong":     "color:" + colorGray900 + ";font-weight:600;",
	"b":          "color:" + colorGray900 + ";font-weight:600;",
}

// inlineBodyStylesTagOrder is the deterministic walk order for inlineBodyStyles.
// Map iteration in Go is randomized; without this every render would produce a
// different style attribute (harmless functionally, noisy in tests/diffs).
var inlineBodyStylesTagOrder = []string{"p", "h2", "h3", "ul", "ol", "li", "blockquote", "hr", "a", "strong", "b"}

// htmlEscaper escapes the five characters that need HTML entity treatment in
// chrome strings (subject, display name, sender, reply-to). bodyHtml is NOT
// run through this — see the package trust boundary comment.
var htmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&#39;",
)

func escapeHTML(value string) string {
	return htmlEscaper.Replace(value)
}

// inlineBodyStyles injects an inline `style="..."` attribute on each supported
// tag. Tags that already carry a `style=` attribute are skipped so Quill-emitted
// overrides win — the matched paragraph loses the base margin/line-height but
// keeps its alignment, which is the right precedence for an authoring tool
// that intentionally set the style.
func inlineBodyStyles(html string) string {
	result := html
	for _, tag := range inlineBodyStylesTagOrder {
		style := bodyTagStyles[tag]
		// (?i) case-insensitive. Optional trailing `/` so `<hr/>` is also styled.
		re := regexp.MustCompile(`(?i)<` + tag + `(\s[^>]*)?/?>`)
		result = re.ReplaceAllStringFunc(result, func(match string) string {
			attrs := extractAttrs(match, tag)
			if hasStyleAttr(attrs) {
				return match
			}
			if attrs != "" {
				return "<" + tag + ` style="` + style + `"` + attrs + ">"
			}
			return "<" + tag + ` style="` + style + `">`
		})
	}
	return result
}

// extractAttrs returns the attribute string of a tag match (everything between
// the tag name and the closing `>`/`/>`), or empty.
func extractAttrs(match, tag string) string {
	// match is like "<p attrs...>" or "<p>" or "<hr/>".
	inner := strings.TrimPrefix(match, "<")
	inner = strings.TrimSuffix(inner, ">")
	inner = strings.TrimSuffix(inner, "/")
	inner = strings.TrimSpace(inner)
	// Strip the leading tag name (case-insensitive).
	lower := strings.ToLower(inner)
	if strings.HasPrefix(lower, strings.ToLower(tag)) {
		inner = inner[len(tag):]
	}
	return inner
}

var styleAttrRe = regexp.MustCompile(`(?i)\sstyle\s*=`)

func hasStyleAttr(attrs string) bool {
	if attrs == "" {
		return false
	}
	return styleAttrRe.MatchString(attrs)
}

// ctaButtonStyle is the inline button style for standalone-link CTAs.
const ctaButtonStyle = "display:inline-block;background-color:" + colorBlue500 + ";color:" + colorWhite +
	";padding:12px 28px;border-radius:6px;text-decoration:none;font-weight:600;font-size:14px;letter-spacing:0.02em;"

var standaloneCtaRe = regexp.MustCompile(`(?is)<p\b[^>]*>\s*(?:<(?:strong|em|b|i)>\s*)*<a\b([^>]*)>([^<]+)</a>(?:\s*</(?:strong|em|b|i)>)*\s*</p>`)
var anchorStyleRe = regexp.MustCompile(`(?i)\sstyle\s*=\s*"[^"]*"`)

// convertStandaloneCtas converts a <p> whose only meaningful content is a
// single <a> (optionally wrapped in strong/em/b/i) into a centered blue-pill
// CTA button. Runs after inlineBodyStyles so we can strip the style attribute
// that pass added on the matched <a>.
func convertStandaloneCtas(html string) string {
	return standaloneCtaRe.ReplaceAllStringFunc(html, func(match string) string {
		sub := standaloneCtaRe.FindStringSubmatch(match)
		if len(sub) < 3 {
			return match
		}
		aAttrs := anchorStyleRe.ReplaceAllString(sub[1], "")
		text := sub[2]
		return `<p style="margin:28px 0;text-align:center;"><a` + aAttrs + ` style="` + ctaButtonStyle + `">` + text + `</a></p>`
	})
}

var linkRe = regexp.MustCompile(`(?is)<a\b[^>]*href=("|')(.*?)("|')[^>]*>([\s\S]*?)</a>`)

// preserveLinkDestinations rewrites `<a href="X">label</a>` to `label (X)` so
// link targets survive the strip-html pass for the plain-text fallback. Skips
// the parenthesized URL when the label already equals the href (raw URL pasted
// as link text).
func preserveLinkDestinations(html string) string {
	return linkRe.ReplaceAllStringFunc(html, func(match string) string {
		sub := linkRe.FindStringSubmatch(match)
		if len(sub) < 5 {
			return match
		}
		href := sub[2]
		label := sub[4]
		trimmedLabel := strings.TrimSpace(label)
		if href == "" || trimmedLabel == href {
			return label
		}
		return label + " (" + href + ")"
	})
}

var tagRe = regexp.MustCompile(`<[^>]+>`)
var entityRe = regexp.MustCompile(`&(amp|lt|gt|quot|#39|nbsp);`)
var multispaceRe = regexp.MustCompile(`[\t ]+`)
var multiNewlineRe = regexp.MustCompile(`\n{3,}`)

// stripHTML removes tags and decodes the small entity set we emit. The output
// is suitable for the plain-text fallback (not for indexing or other downstream
// consumers).
func stripHTML(html string) string {
	out := tagRe.ReplaceAllString(html, "")
	out = entityRe.ReplaceAllStringFunc(out, func(e string) string {
		switch e {
		case "&amp;":
			return "&"
		case "&lt;":
			return "<"
		case "&gt;":
			return ">"
		case "&quot;":
			return `"`
		case "&#39;":
			return "'"
		case "&nbsp;":
			return " "
		}
		return e
	})
	out = multispaceRe.ReplaceAllString(out, " ")
	out = multiNewlineRe.ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out)
}

// renderComplianceFooterHTML emits the sender attribution + reply-to + UNSUBSCRIBE
// block. Empty when input.IncludeComplianceFooter is false.
func renderComplianceFooterHTML(input Chrome, displayNameSafe string) string {
	if !input.IncludeComplianceFooter {
		return ""
	}
	edName := input.EDName
	if edName == "" {
		edName = "Executive Director"
	}
	edNameSafe := escapeHTML(edName)
	replyEmailSafe := escapeHTML(input.EDReplyEmail)
	replyLine := ""
	if input.EDReplyEmail != "" {
		replyLine = `<div style="margin-bottom:6px;">To reply, email <a href="mailto:` + replyEmailSafe + `" style="color:` + colorBlue500 + `;text-decoration:underline;">` + replyEmailSafe + `</a></div>`
	}
	unsubLine := `To unsubscribe from ` + displayNameSafe + ` newsletters, reply with <strong>UNSUBSCRIBE</strong>.`
	if input.UnsubscribeURL != "" {
		unsubLine = `<a href="` + escapeHTML(input.UnsubscribeURL) + `" style="color:` + colorBlue500 + `;text-decoration:underline;">Unsubscribe</a> from ` + displayNameSafe + ` newsletters.`
	}
	return `<tr>
<td style="background-color:` + colorGray50 + `;border-top:1px solid ` + colorGray200 + `;padding:24px 40px;font-size:12px;color:` + colorGray500 + `;font-family:` + fontStack + `;">
<div style="margin-bottom:6px;">Sent by <strong style="color:` + colorGray900 + `;">` + edNameSafe + `</strong> on behalf of <strong style="color:` + colorGray900 + `;">` + displayNameSafe + `</strong>.</div>
` + replyLine + `
<div style="color:` + colorGray400 + `;font-size:11px;">` + unsubLine + ` Delivered by <span style="font-weight:700;color:` + colorBlue500 + `;letter-spacing:-0.02em;">LFX</span>.</div>
</td>
</tr>`
}

// EmailHTML builds the outer HTML envelope wrapping the authored body.
// See the package trust boundary comment.
func EmailHTML(input Chrome) string {
	subject := input.Subject
	if subject == "" {
		subject = "Untitled"
	}
	subjectSafe := escapeHTML(subject)
	display := input.DisplayName
	if display == "" {
		display = "Project"
	}
	displayNameSafe := escapeHTML(display)
	styledBody := convertStandaloneCtas(inlineBodyStyles(input.BodyHTML))
	complianceFooter := renderComplianceFooterHTML(input, displayNameSafe)

	logoCell := ""
	if input.LogoURL != "" {
		logoCell = `<td width="56" valign="middle" style="padding-right:16px;width:56px;">` +
			`<img src="` + escapeHTML(input.LogoURL) + `" alt="` + displayNameSafe + `" width="56" height="56" ` +
			`style="display:block;width:56px;height:56px;border-radius:6px;background-color:` + colorWhite + `;padding:4px;object-fit:contain;border:0;" />` +
			`</td>`
	}

	headerBG := "background-color:" + colorBlue500 + ";background-image:linear-gradient(135deg, " + colorBlue500 + " 0%, " + colorBlue600 + " 100%);"

	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width,initial-scale=1" />
<meta http-equiv="X-UA-Compatible" content="IE=edge" />
<title>` + subjectSafe + `</title>
</head>
<body style="margin:0;padding:0;background-color:` + colorGray50 + `;font-family:` + fontStack + `;">
<table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="background-color:` + colorGray50 + `;padding:24px 12px;">
<tr>
<td align="center">
<table role="presentation" cellpadding="0" cellspacing="0" border="0" width="680" style="width:680px;max-width:680px;background-color:` + colorWhite + `;border:1px solid ` + colorGray200 + `;border-radius:8px;overflow:hidden;">
<tr>
<td style="` + headerBG + `color:` + colorWhite + `;padding:32px 40px;font-family:` + fontStack + `;">
<table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%">
<tr>
` + logoCell + `
<td valign="middle">
<div style="font-size:13px;font-weight:600;letter-spacing:0.8px;text-transform:uppercase;opacity:0.9;color:` + colorWhite + `;font-family:` + fontStack + `;">` + displayNameSafe + ` &middot; Newsletter</div>
<div style="font-size:22px;font-weight:700;line-height:1.3;color:` + colorWhite + `;margin-top:8px;font-family:` + fontStack + `;">` + subjectSafe + `</div>
</td>
</tr>
</table>
</td>
</tr>
<tr>
<td style="padding:32px 40px;font-size:16px;color:` + colorGray800 + `;line-height:1.65;font-family:` + fontStack + `;">` + styledBody + `</td>
</tr>
` + complianceFooter + `
</table>
</td>
</tr>
</table>
</body>
</html>`
}

// EmailText builds the plain-text counterpart to EmailHTML.
func EmailText(input Chrome) string {
	display := input.DisplayName
	if display == "" {
		display = "Project"
	}
	subject := input.Subject
	if subject == "" {
		subject = "Untitled"
	}
	body := stripHTML(preserveLinkDestinations(input.BodyHTML))

	lines := []string{display + " · Newsletter", subject, "", body}

	if input.IncludeComplianceFooter {
		edName := input.EDName
		if edName == "" {
			edName = "Executive Director"
		}
		lines = append(lines, "", "---", "Sent by "+edName+" on behalf of "+display+".")
		if input.EDReplyEmail != "" {
			lines = append(lines, "To reply, email "+input.EDReplyEmail)
		}
		if input.UnsubscribeURL != "" {
			lines = append(lines, "Unsubscribe from "+display+" newsletters: "+input.UnsubscribeURL, "Delivered by LFX.")
		} else {
			lines = append(lines, "To unsubscribe from "+display+" newsletters, reply with UNSUBSCRIBE.", "Delivered by LFX.")
		}
	}

	return strings.Join(lines, "\n")
}
