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

	"golang.org/x/net/html"
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
	// IncludeComplianceFooter is true for recipient-facing sends, including
	// test sends. Previews and other callers that want no footer keep it
	// false.
	IncludeComplianceFooter bool
	// Required when IncludeComplianceFooter is true.
	EDName       string
	EDReplyEmail string
	// UnsubscribeURL is the per-recipient one-click opt-out link. Real sends
	// pass a placeholder here and substitute the real URL per recipient
	// inside the fan-out loop; test sends pass the recipient's real URL
	// directly. When empty the footer falls back to the legacy "reply with
	// UNSUBSCRIBE" copy so misconfigured environments still emit valid HTML.
	UnsubscribeURL string
	// MyNewslettersURL is the recipient-independent deep link to the LFX
	// Self-Serve "My Newsletters" page. When non-empty (and
	// IncludeComplianceFooter is true) the footer renders a dedicated line
	// above the unsubscribe small print. Empty omits the line.
	MyNewslettersURL string
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

// utf8CharsetGuard is a hidden zero-width non-joiner (U+200C) injected at the
// top of every rendered email HTML body to force a UTF-8 MIME charset.
//
// Observed on the SendGrid direct-send path (see linuxfoundation/lfx-self-serve#1744):
// an all-ASCII/Latin-1 newsletter was transmitted as charset=iso-8859-1 and
// Gmail clipped it ("[Message clipped]"). SendGrid appears to choose the MIME
// transfer charset by sniffing the content bytes, so the guard works by
// guaranteeing a byte the single-byte charsets cannot represent: U+200C is
// unrepresentable in iso-8859-1/windows-1252, so a byte-preserving encoder must
// select a multibyte charset (UTF-8). Forcing UTF-8 resolved the clipping in
// testing.
//
// Why the literal character and not an alternative: an HTML entity (e.g.
// &#8203;) is ASCII on the wire, so the encoder still sees an all-Latin-1
// document, and <meta charset> is ignored when the transfer charset is chosen.
// The separate ~102KB size threshold is an independent clipping cause handled
// operationally (see docs/sendgrid-go-live.md), not by this guard.
//
// The SES path (email-service) sets charset=UTF-8 in the MIME header itself and
// is unaffected. Only the HTML part carries the guard; the plain-text
// alternative stays untouched and text clients render it fine.
const utf8CharsetGuard = `<span style="display:none;max-height:0;overflow:hidden;mso-hide:all;">` + "\u200c" + `</span>`

// Per-tag inline styles applied to the authored body so Gmail/Outlook render
// header underlines, blockquotes, branded links etc. — Gmail strips <style>
// blocks so each tag has to carry its own `style=` attribute. Mirrors the
// in-app preview's component SCSS.
var bodyTagStyles = map[string]string{
	"p":          "margin:0 0 14px;line-height:1.65;",
	"h2":         "color:" + colorGray900 + ";font-size:22px;font-weight:700;line-height:1.3;letter-spacing:-0.01em;margin:32px 0 12px;padding-bottom:8px;border-bottom:1px solid " + colorGray200 + ";",
	"h3":         "color:" + colorGray700 + ";font-size:17px;font-weight:600;line-height:1.4;margin:24px 0 8px;",
	"ul":         "margin:12px 0 16px;padding-left:24px;list-style-type:disc;",
	"ol":         "margin:12px 0 16px;padding-left:24px;list-style-type:decimal;",
	"li":         "margin:0 0 8px;line-height:1.6;",
	"blockquote": "margin:16px 0;padding:12px 16px;border-left:3px solid " + colorBlue500 + ";background-color:" + colorBlue50 + ";border-radius:0 4px 4px 0;color:" + colorGray800 + ";font-style:normal;",
	"hr":         "border:0;border-top:1px dashed " + colorGray200 + ";margin:24px 0;",
	"a":          "color:" + colorBlue500 + ";text-decoration:underline;",
	"strong":     "color:" + colorGray900 + ";font-weight:600;",
	"b":          "color:" + colorGray900 + ";font-weight:600;",
}

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

// inlineBodyStyles injects an inline style="..." attribute on each supported
// body tag that does not already carry one, so Quill-emitted overrides win. It
// tokenizes with golang.org/x/net/html rather than regex-matching each tag: the
// prior regex was not quote-aware, so a quoted attribute value containing '>'
// or an unquoted value ending in '/' could corrupt the outbound markup. Tags
// that are not styled, and styled tags that already carry a style attribute,
// are re-emitted byte-for-byte (z.Raw); only a styled start tag missing a style
// attribute is reconstructed, with the style injected first and the existing
// attributes preserved in order and re-escaped exactly once.
func inlineBodyStyles(body string) string {
	z := html.NewTokenizer(strings.NewReader(body))
	var b strings.Builder
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			// ErrorToken is io.EOF once the input is fully consumed (the reader is
			// in-memory, so no other read error is possible); the document has been
			// emitted, so stop.
			return b.String()
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			b.Write(z.Raw())
			continue
		}
		name, hasAttr := z.TagName()
		style, styled := bodyTagStyles[string(name)]
		if !styled {
			b.Write(z.Raw())
			continue
		}
		type attr struct{ key, val string }
		var attrs []attr
		hasStyle := false
		for hasAttr {
			var k, v []byte
			k, v, hasAttr = z.TagAttr()
			if string(k) == "style" {
				hasStyle = true
			}
			attrs = append(attrs, attr{key: string(k), val: string(v)})
		}
		if hasStyle {
			// Author already set a style attribute — preserve the tag verbatim so
			// their override wins.
			b.Write(z.Raw())
			continue
		}
		b.WriteString("<" + string(name) + ` style="` + style + `"`)
		for _, a := range attrs {
			b.WriteString(" " + a.key + `="` + html.EscapeString(a.val) + `"`)
		}
		if tt == html.SelfClosingTagToken {
			b.WriteString("/>")
		} else {
			b.WriteString(">")
		}
	}
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

var multispaceRe = regexp.MustCompile(`[\t ]+`)
var multiNewlineRe = regexp.MustCompile(`\n{3,}`)

// htmlToText derives a plain-text body from an HTML fragment or full document
// for the text/plain part of an email. It tokenizes with golang.org/x/net/html
// rather than parsing HTML structure with regexes, which fixes two failure
// modes of the old regex approach:
//
//   - Non-rendered content never leaks: text inside <head>, <style>, <script>,
//     and <title> is dropped. A self-closing <style/> or a '>' inside an
//     attribute value could defeat the old regex skip-scanner and spill CSS/JS
//     into the recipient-visible text; the tokenizer tracks real element
//     boundaries, so it cannot.
//   - Link destinations survive: <a href="X">label</a> becomes "label (X)",
//     unless the label is already the raw URL (or the href is empty), matching
//     the old preserveLinkDestinations behavior.
//
// Entities are decoded by the tokenizer (so &amp; etc. need no manual table);
// decoded &nbsp; (U+00A0) is normalized to a plain space, then the same
// whitespace collapse as before is applied.
func htmlToText(input string) string {
	z := html.NewTokenizer(strings.NewReader(input))
	var out strings.Builder
	skip := 0 // depth inside <head>/<style>/<script>/<title> — their text is dropped
	inAnchor := false
	anchorHref := ""
	var anchorText strings.Builder

	flushAnchor := func() {
		if !inAnchor {
			return
		}
		label := anchorText.String()
		out.WriteString(label)
		if anchorHref != "" && strings.TrimSpace(label) != anchorHref {
			out.WriteString(" (" + anchorHref + ")")
		}
		inAnchor = false
		anchorHref = ""
		anchorText.Reset()
	}

loop:
	for {
		switch z.Next() {
		case html.ErrorToken:
			break loop // io.EOF or a parse error — emit what we have
		case html.TextToken:
			if skip > 0 {
				continue
			}
			if inAnchor {
				anchorText.Write(z.Text())
			} else {
				out.Write(z.Text())
			}
		case html.StartTagToken:
			name, hasAttr := z.TagName()
			switch string(name) {
			case "head", "style", "script", "title":
				skip++
			case "a":
				if skip == 0 && !inAnchor {
					href := ""
					for hasAttr {
						k, v, more := z.TagAttr()
						if string(k) == "href" {
							href = string(v)
						}
						hasAttr = more
					}
					inAnchor = true
					anchorHref = href
					anchorText.Reset()
				}
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "head", "style", "script", "title":
				if skip > 0 {
					skip--
				}
			case "a":
				flushAnchor()
			}
		case html.SelfClosingTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "style", "script", "title":
				// style/script/title are raw-text/RCDATA elements: the tokenizer
				// switches to raw-text mode after the start tag even when it is
				// written self-closing (HTML ignores the slash on these), so the
				// following content arrives as one text token and must be dropped
				// until the matching close (or EOF). This is exactly the
				// self-closing <style/> case the old regex skip-scanner missed.
				skip++
			}
			// Other self-closing/void tags (<br/>, <img/>, …) contribute no text.
		}
	}
	flushAnchor() // document ended mid-anchor (unterminated <a>)

	text := strings.ReplaceAll(out.String(), " ", " ")
	text = multispaceRe.ReplaceAllString(text, " ")
	text = multiNewlineRe.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

// StripHTMLForText derives a plain-text body from a full HTML email. It is the
// exported counterpart to EmailText for the layout-based send path, where the
// emitter already owns the whole email and there is no Chrome to wrap. Link
// destinations are preserved (`label (href)`); non-rendered <head>/<style>/
// <script> content is dropped by the tokenizer.
func StripHTMLForText(input string) string {
	return htmlToText(input)
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
		// clicktracking="off": this is an HMAC-signed one-click opt-out link, so it
		// must not be proxied through the provider's click-tracking redirect, and
		// it must not count toward click_rate (which measures engagement with
		// author content). SendGrid honors clicktracking="off" only when the
		// attribute appears BEFORE href.
		unsubLine = `<a clicktracking="off" href="` + escapeHTML(input.UnsubscribeURL) + `" style="color:` + colorBlue500 + `;text-decoration:underline;">Unsubscribe</a> from ` + displayNameSafe + ` newsletters.`
	}
	myNewslettersLine := ""
	if input.MyNewslettersURL != "" {
		// clicktracking="off" for the same reason as the Unsubscribe link above:
		// chrome/compliance links should not be proxied or counted as clicks.
		// SendGrid honors clicktracking="off" only when the attribute appears BEFORE href.
		myNewslettersLine = `<div style="margin-bottom:6px;">Missed an issue? View past newsletters any time in <a clicktracking="off" href="` + escapeHTML(input.MyNewslettersURL) + `" style="color:` + colorBlue500 + `;text-decoration:underline;">My Newsletters</a>.</div>`
	}
	return `<tr>
<td class="lfx-pad" style="background-color:` + colorGray50 + `;border-top:1px solid ` + colorGray200 + `;padding:24px 24px;font-size:12px;color:` + colorGray500 + `;font-family:` + fontStack + `;">
<div style="margin-bottom:6px;">Sent by <strong style="color:` + colorGray900 + `;">` + edNameSafe + `</strong> on behalf of <strong style="color:` + colorGray900 + `;">` + displayNameSafe + `</strong>.</div>
` + replyLine + `
` + myNewslettersLine + `
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
<style>
@media only screen and (max-width:600px){
.lfx-pad{padding-left:16px!important;padding-right:16px!important;}
}
</style>
</head>
<body style="margin:0;padding:0;background-color:` + colorGray50 + `;font-family:` + fontStack + `;">` + utf8CharsetGuard + `
<table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="background-color:` + colorGray50 + `;padding:16px 8px;">
<tr>
<td align="center">
<table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="width:100%;max-width:680px;background-color:` + colorWhite + `;border:1px solid ` + colorGray200 + `;border-radius:8px;overflow:hidden;">
<tr>
<td class="lfx-pad" style="` + headerBG + `color:` + colorWhite + `;padding:28px 24px;font-family:` + fontStack + `;">
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
<td class="lfx-pad" style="padding:28px 24px;font-size:16px;color:` + colorGray800 + `;line-height:1.65;font-family:` + fontStack + `;">` + styledBody + `</td>
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
	body := htmlToText(input.BodyHTML)

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
		if input.MyNewslettersURL != "" {
			lines = append(lines, "Missed an issue? View past newsletters any time in My Newsletters: "+input.MyNewslettersURL)
		}
		if input.UnsubscribeURL != "" {
			lines = append(lines, "Unsubscribe from "+display+" newsletters: "+input.UnsubscribeURL, "Delivered by LFX.")
		} else {
			lines = append(lines, "To unsubscribe from "+display+" newsletters, reply with UNSUBSCRIBE.", "Delivered by LFX.")
		}
	}

	return strings.Join(lines, "\n")
}
