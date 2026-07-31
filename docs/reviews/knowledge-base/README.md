<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Newsletter Service Review Knowledge Base

This is a **GROWING KB** — an empirical, repo-owned record of the patterns that human reviewers and review bots have actually flagged on `lfx-v2-newsletter-service` PRs, distilled into mechanically-detectable rules. It is read by the repo-owned `newsletter-service-learnings-reviewer` brain (`.claude/skills/newsletter-service-learnings-reviewer/SKILL.md`, reached by the `local-learnings-review` discovery alias), which matches a patch against these patterns and emits only findings that quote a pattern entry.

This KB is the *empirical* surface. It deliberately does NOT duplicate:

- the central `general` review brain — generic correctness / security / test intuition, including generic Go gotchas.
- the repo-owned `newsletter-service-code-reviewer` brain — the documented rule surface (`CLAUDE.md`, `.claude/skills/newsletter-service-dev`, contract docs, chart docs, Makefile).

## Methodology

Built per `lfx-architecture-scratch/2026-05-DevX-Time-to-Merge/service-kb-research-playbook.md` (thin-corpus mode). Corpus: merged PRs only. For each PR we pulled inline review threads, review bodies, PR conversation, the diff, and GraphQL `reviewThreads` resolution state, then clustered raw comments into candidate patterns and applied the promotion gate (repo-specific + mechanically detectable + currently relevant + not tooling-enforced; with at least one of recurrence / cost-of-miss / acted-on-by-maintainer). In a ~7-PR corpus, recurrence is rarely reachable, so most entries clear the B gate on **cost-of-miss** or **acted-on-by-maintainer** (resolved by a code change, often endorsed by the maintainer reviewer dealako or merged by andrest50 / niravpatel27).

## Refresh — 2026-07-30 (LFXV2-2894, verified at `f13d015`)

A second pass mined **PRs #26–#63** (38 PRs, created on/after 2026-06-29, all states) and re-audited all 22 existing patterns and 7 false-positives against HEAD. Every promoted claim was independently re-verified against the checkout rather than taken from the mining pass.

- **Qualifying source for new promotions:** Copilot (`login: "Copilot"`, `type: Bot`, id `175728472`, app `copilot-pull-request-reviewer`) raising a finding a developer then fixed by an observable commit, with the construct still relevant at HEAD. **`lfx-reviewer` is a distinct `User` identity** — this repo's own agentic/pi PR reviewer, and the highest-volume inline commenter on recent PRs — and none of its comments were promoted under that rule. On this repo, "the bot flagged it" usually means `lfx-reviewer`, not Copilot.
- **Existing entries:** 14 retained with fixes verified in place, 4 retained unchanged, 3 revised, 1 retained and escalated.
- **New:** 12 patterns promoted (§ Categories below). 7 further candidates are **held, not promoted**, because their constructs live on unmerged branches; they are recorded in the LFXV2-2894 evidence handoff and get re-evaluated if those PRs merge.
- **False-positives:** 6 retained (one strengthened), 1 removed as dead noise — see `known-false-positives.md`.
- **Known gaps deliberately left open**, pending a policy decision rather than resolved by inference: entries whose only cited source is a human maintainer (8 of the original 22) stay as-is; `chart/mode-shape-selector-no-fail-fast` is real but was **raised and never acted on**, and stays unchanged rather than being downgraded; `persistence/on-conflict-target-mismatch` is retained and escalated — its prescribed fix was never applied, but that reading is static and unconfirmed against a live Postgres, so it is not restated here as an observed failure.

## Corpus stats — first build (PRs #3–#9)

- **Merged PRs sampled:** 7 (PRs #3–#9). All merged PRs in repo history as of 2026-05-29.
- **Comment surfaces by author bucket:**
  - **Human reviewers:** dealako (maintainer, PR #3 — 7 blocking + 5 minor inline, the highest-value source), andrest50 (PR #3 Makefile/README; PR #5 workflow-pin consistency), niravpatel27 (author resolution replies + PR #3 reviewer notes).
  - **Copilot (`Copilot`):** ~24 inline comments across PRs #3, #4, #5, #6, #7, #9 plus a PR overview on every PR.
  - **`github-license-compliance[bot]`:** 8+ transitive-dependency license alerts on `go.mod` (PRs #3, #5) — all routed to known-false-positives.
- **CodeRabbit:** confirmed **OFF / not enabled** — 0 inline comments and 0 PR-summary issue comments across all 7 PRs. (Sampling confirmed.)
- **Acted-on signal:** PR #3's 16 maintainer + Copilot threads were all resolved by code changes (commits `e9e61b0`, `652c118`, `959e23d`); PR #9's 3 Copilot threads resolved in `7232a31`. PR #6 (schema immutability) shipped as its own fix PR.

## Categories

| File | Patterns | Read when |
| --- | --- | --- |
| `security.md` | 6 (5 Critical, 1 Important) | handler / middleware / config / schema changes touching response bodies, JWT validation, auth env, or unauthenticated-endpoint persistence |
| `persistence-and-schema.md` | 6 (2 Critical, 4 Important) | `internal/schema/schema.sql`, `internal/schema/schema.go`, or `internal/repository/postgres.go` changed |
| `recipient-resolution-and-http.md` | 6 (all Important) | `send_orchestrator.go`, `internal/infrastructure/**` (any upstream client, not only `nats/`), `internal/handler/send.go`, `internal/handler/http.go`, `internal/service/newsletter.go`, `internal/domain/model/**`, `pkg/api/newsletter.go`, or `internal/handler/drafts.go` changed |
| `send-orchestration.md` | 2 (both Important) | `internal/service/send_orchestrator.go`, `internal/service/unsubscribe.go`, or `internal/infrastructure/nats/**` changed |
| `render-and-email-chrome.md` | 1 (Critical) | anything under `internal/service/render/` changed |
| `service-and-tests.md` | 3 (2 Important, 1 Nit) | anything under `internal/service/`, `cmd/newsletter-api/service/implementations.go`, or any `*_test.go` changed |
| `chart.md` | 7 (3 Critical, 3 Important, 1 Nit) | any `charts/lfx-v2-newsletter-service/**` **or `internal/handler/http.go`** changed |
| `ci-and-workflows.md` | 3 (1 Critical, 2 Important) | anything under `.github/workflows/` or `.github/skills/` changed |

**Total: 34 patterns** + `known-false-positives.md` (6 entries).

The `Read when` column above mirrors the routing table in
`.claude/skills/newsletter-service-learnings-reviewer/SKILL.md`, which is the one
the reviewer actually executes. Two rows route on files outside their own area
(see that skill's Step 1) — **change both tables together, or this one silently
lies about what gets read.**

## Highest-value patterns

The maintainer-blocking, cost-of-miss patterns from PR #3 and the contract-integrity patterns from PR #9:

- `security/unauthenticated-pixel-unbounded-write` — the open pixel takes attacker-controlled input on an unauthenticated path.
- `security/5xx-error-body-leak` and `security/jwt-error-leak-to-client` — infra detail leaking to unauthenticated callers.
- `persistence/non-immutable-generated-column-expr` and `persistence/on-conflict-target-mismatch` — both break `schema.sql` apply or runtime inserts; the latter recurred across PRs #3 and #6.
- `recipient/errgroup-cancellation-on-failure` and `recipient/unbounded-pagination-loop` — orphaned/infinite upstream work. (The pagination entry is **forward-looking only**; its construct is gone from the repo.)
- `chart/db-password-composed-into-pod-spec` — DB password exposed in the Pod spec.

From the 2026-07-30 refresh, the two with the strongest evidence:

- `render/no-hand-rolled-regex-html-parsing` — the repo's most-recurring lesson. Copilot independently rediscovered this class across **four** implementation attempts, and instances are still live in the production plain-text path. Its value is not catching a new bug; it is stopping the next re-implementation.
- `chart/pii-route-must-not-fall-back-to-allow-all` — a RuleSet rule guarding project-scoped PII whose `allow_all` else-branch is the one that actually renders under the shipped `openfga.enabled: false` default.

Also worth knowing: `ci/order-github-events-by-id-not-created-at` is a merge-gate approval-bypass class, and three of the new entries (`chart/new-route-must-be-wired-in-httproute-and-ruleset`, `test/fake-must-capture-scoping-arguments`, `ci/bot-login-regex-must-allow-bot-suffix`) were promoted specifically because they recurred across independent PRs — the textbook case for a KB.

## Corrections on record

Every correction made to this KB, with the probe that produced the wrong answer. A
check that returns clean is only evidence if it *could* have returned dirty for the
thing being claimed, so the failed probe is recorded next to the fix — a future
refresher who reruns the weak version should recognise it rather than trust it.
Nothing here changed a pattern's PR / comment / fixing-commit provenance.

- **`ci/gh-paginate-jq-length-per-page` anchor.** Inherited from the LFXV2-2894 brief
  as `.github/workflows/agentic-gate.yml:418,565`. `:565` paginates ids and never
  counts anything; the real paginate-then-count site is `:418-420`, and `:416-417`
  carries an inline comment stating this very lesson. *Weak probe:* reading only the
  lines the brief cited, instead of searching the file for the counting idiom —
  which also shows it recurring at `:164`, `:360` and `:512`. The workflow is
  byte-identical to `f13d015`, so this was brief imprecision, not drift.
- **`chart/middleware-namespace-must-match-httproute` paths.** The PR #7 quote
  contains a chart-relative `templates/httproute.yaml`. It is **preserved exactly**;
  a separate *Current anchor* paragraph carries the repo-relative locations. Never
  rewrite a path inside quoted evidence to make it resolve — that falsifies the
  quote. *Weak probe:* a path audit that resolves every citation against the repo
  root cannot tell a historical quote from a current claim.
- **`persistence/on-conflict-target-mismatch` remains a live true positive.** *Weak probe:* `grep 'ON CONFLICT' --include='*.go'` returns nothing but
  a comment, because bun passes the clause as a **string argument to a builder
  method** (`On("CONFLICT …")`). A second weak probe — grepping `schema.sql` for
  `ADD CONSTRAINT <name>` and finding none — examines only the schema half of a
  two-file mismatch and cannot see the caller at all. The entry's own `**Detect:**`
  clause is the correct probe and it fires; run that, not a literal SQL grep. Tracked
  for fix outside this KB.
- **Removed false-positive entry.** The dead "Add Copilot custom instructions"
  promotional-CTA entry was removed but retained as an HTML comment with its evidence
  in `known-false-positives.md`, so the call is reversible in one line.
- **Counts re-verified shape-agnostically.** 34 patterns across 8 category files, and
  6 entries in `known-false-positives.md`. *Weak probe:* counting `##` headings
  undercounts the false-positive file, whose entries are `###` under category
  headings; one `##` there is `How to add a new entry`, which is not an entry.
- **Path-citation audit.** Counts are published per tier, because a single total
  hides which boundary produced it — the earlier one-number report of "85, of which
  21 directory-only" was reproducible only if you happened to classify tiers the same
  way. Distinct normalised tokens across both brains, all nine KB files and
  `CLAUDE.md`, on two axes:

  | | file-shaped | directory-shaped | tier total |
  | --- | --- | --- | --- |
  | backticked | 63 | 15 | 78 |
  | unquoted | 1 | 6 | 7 |
  | **total** | **64** | **21** | **85** |

  All 85 resolve; 0 dangling in every cell. *Weak probes, all mine:*
  building the known-directory set from each file's immediate parent only (so
  `docs/reviews/` and `internal/infrastructure/` read as dangling), not stripping a
  `.Symbol` suffix (`pkg/errors.ServiceUnavailable`), and splicing adjacent prose
  into a path (`Makefile/README`). Backtick-only extraction also misses unquoted and
  directory-only references; widening it requires filtering language and library
  identifiers so `net/url` and `golang.org/x/…` are not misread as repo paths.
  **Read those last two as probe examples, not anchors:** `pkg/errors.ServiceUnavailable`
  is a Go package plus an exported symbol and `Makefile/README` is two separate
  filenames spliced out of prose — neither is a repo path, and neither is expected to
  resolve. `docs/reviews/`, `internal/infrastructure/` and `.github/skills/` in this
  entry *are* real anchors; they are listed because the broken probe wrongly called
  them dangling. A path sweep run over this section will flag the two non-paths by
  design — that is the documented behaviour, not a defect to fix.
- **Quote convention: double quotes mean attributable, findable-in-source evidence.**
  Editorial or paraphrasing phrasing uses italics or plain prose instead, so a reader can
  tell at a glance what is worth grepping. Two false-positive entry headings were
  paraphrases of a reviewer's point wrapped in double quotes, which read as citations;
  they are now plain headings, and each entry's verbatim quote stays on its `**Source:**`
  line untouched. *Weak probe:* a quote extractor must be **escape-aware** — `\"` is not
  a delimiter, and a naive `"([^"]*)"` splices across adjacent unrelated quotes in code
  fragments, which is what produced phantom not-verbatim hits in an earlier sweep.
- **Escaping can make a present pattern un-greppable.**
  `ci/bot-login-regex-must-allow-bot-suffix` showed its regex in single-escaped form
  while the live instance sits inside a `--jq` string in YAML and is written
  `\\[bot\\]`. Grepping the form the entry showed returned nothing although the pattern
  was there. The entry now carries that warning. Generalise it: before concluding a
  pattern is absent from YAML, JSON, TOML or a shell-quoted string, probe the
  *authoring* escaping, not the logical form.
- **Recurrence counts keep the promotion gates.** "Recurred across independent PRs" is
  counted only over PRs passing the same eligibility the promotion set uses —
  merged/current and developer-fixed. Widening a recurrence search does not widen
  eligibility: matches on open or unmerged branches stay in the held set above and never
  raise a promotion count. Recurrence *within one file at HEAD* — as recorded for the
  paginate-count anchor — is a code-locality observation, not PR recurrence, and feeds
  no count here.

## Maintenance

Add entries as new PRs surface repo-specific, mechanically-detectable, acted-on patterns. Move anything the team decides is noise into `known-false-positives.md`. Promote a Nit/Important to a higher tier only on recurrence or maintainer endorsement. Record a removal or revision with its evidence (see the comment in `known-false-positives.md`) rather than deleting silently — the audit trail is what lets a call be reversed cheaply.

Two structural notes for whoever refreshes this next:

- A gate that admits only *observably fixed* findings systematically retains what the team already fixed and discards what it keeps skipping — and those are not obviously the least important patterns. A gate that also requires the construct to be *live on `main`* discards evidence from repeatedly-abandoned attempts, which is the strongest recurrence signal there is. Keep rejections **listed** in the evidence handoff rather than dropped, so a later merge reverses the call in one line.
- Nothing in `.github/skills/` references this KB, so its patterns do not reach the reviewer that runs on the PR itself. That is a review-system design question owned outside this repo, not a KB defect.

_First built: 2026-05-29 (PRs #3–#9). Last refreshed: 2026-07-30 (PRs #26–#63, verified at `f13d015`)._
