<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Known false positives — applied LAST in every review pass

Findings that match any pattern below MUST be dropped, regardless of which source (KB pattern file, code-reviewer rule, bot comment) produced them. This list is the floor — even a quotable KB pattern doesn't survive if it matches a known false positive.

Used by the repo-owned `newsletter-service-learnings-reviewer` brain (its Step 4 floor) and as filter discipline for the `newsletter-service-code-reviewer` brain, both under `.claude/skills/`.

---

## Already-covered-by-tooling

### License-header complaints on a file that already has one

**Pattern matched:** finding states a `.go` / `.md` file is missing the MIT (or CC-BY) license header when `head -2 <file>` confirms it is present.

**Why false:** `make check` runs the license-check step and `/newsletter-service-preflight` enforces it; CI (`license-header-check.yml`) also gates it. If those pass, the header is present.

**Source:** `Makefile` (`make check`); `.github/workflows/license-header-check.yml`.

### gofmt / golangci-lint / go vet style findings

**Pattern matched:** formatting, import ordering, unused-variable, or generic lint nits on `.go` files.

**Why false:** `make fmt`, `make lint` (repo-pinned golangci-lint), and `go vet` in `make check` already enforce these, and `/newsletter-service-preflight` runs them pre-PR. CI enforces it too — `.github/workflows/mega-linter.yml` runs on top of `license-header-check.yml`. Surfacing them in a learnings review is duplicate signal.

**Source:** `Makefile`; `.claude/skills/newsletter-service-preflight/SKILL.md`; `.github/workflows/mega-linter.yml`.

---

## Org / transitive-dependency license alerts

### `github-license-compliance` BSD / golang patent-license alerts on `go.mod`

**Pattern matched:** `github-license-compliance[bot]` alerts on `go.mod` for `BSD-2-Clause` / `BSD-3-Clause AND LicenseRef-scancode-google-patent-license-golang` / `NOASSERTION` in transitive `golang.org/x/*` (and OTel/pgx) dependencies.

**Why false:** these are stdlib-adjacent transitive deps required by the OpenTelemetry SDK, pgx, and other LFX-standard libraries. They are not actionable in a service PR; resolving them needs an org-level license-allowlist update. The maintainer explicitly left these threads as a tracking signal, not a blocker.

**Source:** PR #3 `go.mod` — niravpatel27 — "Addressing these needs an admin update to the org license policy / allowlist, which is outside the scope of this PR … Leaving these 8 threads unresolved as a tracking signal." PR #3 review — dealako — "github-license-compliance[bot]: … Agreed with the author — not actionable in this PR; needs an org-level allowlist update."

---

## Copilot review-automation quirks

<!--
Removed 2026-07-30 (LFXV2-2894 audit, verified at HEAD f13d015): the
"Add Copilot custom instructions for smarter, more guided reviews" promotional-CTA
entry. Every Copilot review body across PRs #26-#63 was scanned: zero occurrences.
The repo added `.github/copilot-instructions.md`, so the CTA is no longer emitted and
the entry guarded against something that cannot happen. Recorded here rather than
deleted silently so the disposition stays auditable.
-->

### Action-pin version-comment consistency nits (`# v4` vs `# v6.0.2`)

**Pattern matched:** Copilot flagging that the same pinned action SHA carries different `# v…` annotation comments across workflow files.

**Why false:** the SHA is what pins the action; the comment is cosmetic. The team resolved these by aligning comments (PR #5), but the underlying pin is correct either way — this is a low-value annotation nit, not a security or correctness issue. Demote unless the SHA itself is wrong.

**Source:** PR #5 `.github/workflows/*` — Copilot — "the same SHA is annotated as `# v4` / `# v5` in other workflows."

---

## Stale / out-of-scope suggestions

### `EDName` "required but unused" re-flag

**Pattern matched:** a finding that `EDName` (or a similar field) is required-but-unused on a send/test-send input.

**Why false:** this was already resolved on PR #3 — the hard `EDName == ""` rejection was dropped and the field was explicitly kept on the input struct "for forward-compatibility with the legacy UI contract." Flagging the retained field as dead code re-litigates a closed maintainer decision. (The field has since left the public DTOs in the project-scoping rewrite; today `EDName` only exists on the internal `render.Chrome`, populated from the resolved sender name — do not re-flag that either.)

**Source:** PR #3 `internal/service/send_orchestrator.go:62` — resolved in `959e23d`: "dropped the `EDName == ""` rejection … Kept the field … with a comment explaining it's accepted for forward-compatibility."

### "Skip the existence check before insert on the open pixel"

**Pattern matched:** Copilot suggesting `RecordOpenWithHash` drop the `repo.Get` existence check and rely on the FK violation path to save a round-trip.

**Why false:** the maintainer-reviewed design landed with the validation + dedup mechanism (regex + per-hour unique index + `ON CONFLICT DO NOTHING`) as the agreed hardening; the extra `Get` is intentional and the suggested micro-optimization was not adopted. Demote unless it recurs with maintainer endorsement.

**Source:** PR #3 `internal/service/newsletter.go:177` — Copilot — "performs a `repo.Get` existence check before inserting … doubles DB round-trips." Not acted on; superseded by the dedup-index design.

---

## How to add a new entry

When you find a bot or KB finding the team has explicitly decided is not relevant for this repo:

1. Add an entry here with **Pattern matched**, **Why false**, and **Source** (PR # + quote where possible).
2. If the pattern was previously in a category file, remove it there — don't keep it in both.
3. Keep this list small. If it grows past ~25 entries, re-audit; that signals the KB is too permissive.
