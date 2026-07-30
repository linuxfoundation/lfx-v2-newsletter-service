<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# CI and Workflows

Patterns for the repo's GitHub Actions workflows and the shell inside them. The three entries here were promoted for one reason: each recurred across sites or PRs, and each breaks a check that decides whether a PR may merge. A wrong `sort`, a per-page `jq` count, or a login regex that misses a `[bot]` suffix does not fail loudly — it silently changes a gating decision.

Scope note: these are review patterns for *changed workflow shell and expressions*. Whether a given pipeline change is allowed at all is a separate question this KB does not speak to, and nothing here is a licence to widen a workflow's permissions or write paths.

**Read when:** anything under `.github/workflows/` or `.github/skills/` changed.

---

## `ci/order-github-events-by-id-not-created-at` — Critical

**Pattern:** workflow shell picks the latest of a set of GitHub API objects (check runs, statuses, reviews, comments) by sorting on `created_at`. That field has only second precision, so ties are broken by the secondary sort — which on string state ordering puts `success` after `failure`/`pending`. The step can select an older success and approve a PR whose current head regressed.

**Detect:** in `.github/workflows/**`, flag any `sort_by(.created_at)` / `sort -k` on a timestamp used to choose the most recent GitHub API object where the decision gates a merge or approval. Require a numeric sort on the object's `id` (`sort -n` on `(.id|tostring)`), which is monotonic and unique.

**Empirical citation:** PR #36 (MERGED) — Copilot `3573563274` plus three sibling comments — *"`created_at` has only second precision … because `success` sorts after `failure`/`pending`, this can select an older success and approve a regressed PR."* Resolved in `2c9ecea2`. Three sites in one PR, and the failure class is a merge-gate approval bypass. Verified at HEAD `f13d015`: `.github/workflows/agentic-gate.yml:199-200,317-318` sort `-n` on `(.id|tostring)`.

**Failure message:** latest-object selection sorts on `created_at` (second precision) instead of `id` — a stale success can gate a regressed head.

**Fix:** sort numerically on the API object's `id` and take the last; never rely on `created_at` ordering for a gating decision.

---

## `ci/gh-paginate-jq-length-per-page` — Important

**Pattern:** `gh api --paginate` applies its `--jq` filter to **each page separately**, so a filter ending in `| length` emits one count per page rather than a single total. The consuming shell then compares a multi-line string as an integer and fails with `integer expression expected`, breaking the check that was supposed to run.

**Detect:** in `.github/workflows/**`, flag any `gh api --paginate` whose `--jq` ends in `length` (or otherwise aggregates) and whose output is used as a single number. Require counting the streamed lines instead — `grep -c .` — or aggregating outside the per-page filter.

**Empirical citation:** PR #29 — Copilot `3507277710` plus three siblings — *"`gh api --paginate` applies the `--jq` filter to each page separately, so `[...] | length` emits one count per page … fails with 'integer expression expected'."* Resolved in `5d35d150` and `bebc75e1`. Three-plus sites across two files, and it breaks the gate's blocking checks.

Verified at `d6e35e5` (the file is byte-identical to `f13d015`): the paginate-then-count site is `.github/workflows/agentic-gate.yml:418-420`, where the `--jq` emits ids and `grep -c .` counts the streamed lines — and `:416-417` carries an inline comment stating this exact lesson. The same `grep -c .` counting idiom appears at `:164`, `:360` and `:512`. No `[...] | length` remains inside any `--jq` in that file; the only occurrence of "length" is the comment explaining why not.

_Anchor note: an earlier draft of this entry cited `:418,565`. Those are `--paginate` invocation lines, not counting sites — `:565` paginates to extract ids and never counts. Corrected against the file at `d6e35e5`; the PR, comment and fixing-commit provenance above is unchanged._

**Failure message:** `gh api --paginate --jq '… | length'` emits one count per page — the shell comparison breaks instead of counting.

**Fix:** stream the items and count lines with `grep -c .`, or aggregate after pagination rather than inside the per-page `--jq`.

---

## `ci/bot-login-regex-must-allow-bot-suffix` — Important

**Pattern:** a workflow matches an actor by login with a regex that omits the `[bot]` suffix, or omits the app's full login form. The same bot then fails the match depending on which surface reported it, so an allowlist or trust check silently does not apply.

**Detect:** in `.github/workflows/**`, for any comparison of an actor login against an expected bot identity, confirm the pattern tolerates both the bare and `[bot]`-suffixed forms and every login variant the app uses — e.g. `"^(copilot(-pull-request-reviewer)?|github-actions)(\[bot\])?$"`. Flag an exact-string or anchored match that would miss a suffixed login.

**Empirical citation:** PR #29 — Copilot `3571611498` — and then **independently again on PR #38** — Copilot `3579341527`, `3579341562`. Resolved in `9fc8c942` with `"^(copilot(-pull-request-reviewer)?|github-actions)(\[bot\])?$"`. Promoted specifically because it recurred across two separate PRs: a durable rule would have caught the second. Verified at HEAD `f13d015`: `.github/workflows/agentic-apply.yml:208`.

**Failure message:** bot-login match omits the `[bot]` suffix or an app login variant — the identity check silently fails to apply.

**Fix:** match with an alternation covering the app's login forms and an optional `(\[bot\])?` suffix, anchored at both ends.
