<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Render and Email Chrome

Patterns for `internal/service/render/` — the HTML and plain-text email chrome wrapped around an author-supplied newsletter body. This is the one area of the repo where the same lesson has been re-learned on every implementation attempt, which is exactly what this KB exists to stop.

**Read when:** anything under `internal/service/render/` changed.

---

## `render/no-hand-rolled-regex-html-parsing` — Critical

**Pattern:** author-supplied `body_html` is parsed with regular expressions instead of a tokenizer — matching tags, testing for an attribute's presence, trimming a tag's inner text, or stripping tags to derive plain text. Regex cannot model HTML: an attribute value that contains `>` or `style=`, a self-closing `<script/>`, or an unquoted URL ending in `/` each break the match in a way that silently corrupts outbound mail or leaks markup into the recipient-visible preview.

**Detect:** in `internal/service/render/**`, flag any `regexp` used to parse HTML **structure** over author-supplied `body_html`, and require `golang.org/x/net/html` tokenization instead. Specifically:

- an attribute-presence regex with no quote-awareness (`\sstyle\s*=` and similar), which matches inside a quoted value;
- `strings.TrimSuffix(<tag inner text>, "/")`, which corrupts an unquoted attribute value that legitimately ends in `/`;
- `<[^>]+>` used to strip tags and derive text, which terminates at the first `>` inside an attribute value and leaks `<style>`/`<script>` content;
- a skip-region scanner keyed on `<tag>` … `</tag>` that a self-closing `<tag/>` leaves unopened.

**Empirical citation:** independently rediscovered by Copilot across four separate implementation attempts. PR #40 `3582961383` — `tagRe` mangles `<a title="1 > 0">` into `0">Hello` and `<style>` leaks CSS into the inbox preview; PR #40 `3583429233` — a self-closing `<script/>` leaves the skip region unopened, so the payload leaks; PR #46 `3588560089`, `3588649792` — the same class in a fresh `preheader.go`; PR #47 `3589162869` — *"`hasStyleAttr`'s `\sstyle\s*=` regex can match inside a quoted value. For example, `<img alt="choose style=wide" src="...">` has no style attribute but is returned unchanged, so the responsive image rules are skipped."*; PR #47 `3589219809` — `extractAttrs` strips a trailing `/` that is part of an unquoted URL.

Three instances were verified live in `internal/service/render/email_chrome.go` at HEAD `f13d015`: `:146-153` (`styleAttrRe = regexp.MustCompile(`(?i)\sstyle\s*=`)` feeding `hasStyleAttr`, quote-blind, affecting every tag in `inlineBodyStylesTagOrder` at `:110-118`); `:132-137` (`extractAttrs`' unconditional `strings.TrimSuffix(inner, "/")`); and `:200,208-209` (`stripHTML` = `tagRe.ReplaceAllString(html, "")` with `tagRe = regexp.MustCompile(`<[^>]+>`)`), which is **on the production path** — called at `:340` to build the plain-text body of every newsletter. PR #47 fixed the first two (`2124f749`, `23785cd7`) but has not merged, so all three remain.

**Failure message:** author-supplied HTML parsed with a regex in the render path — attribute values and self-closing tags break the match, corrupting outbound mail or leaking markup into the preview.

**Fix:** tokenize with `golang.org/x/net/html` and inspect real nodes and attributes. If a regex genuinely must stay, it must be quote-aware, must not trim structural characters out of attribute values, and must not be used to derive text from markup.
