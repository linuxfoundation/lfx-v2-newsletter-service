---
name: newsletter-service-pr-readiness
description: >
  Repo-local pre-PR shape check for lfx-v2-newsletter-service. Audits branch
  name, JIRA reference, conventional commits, rebase status, DCO + GPG signing
  on every commit, total diff size, and protected newsletter-service files
  touched against the selected base branch. Shape check only: does not review
  Go code, run tests, format files, lint, build, or create a PR.
context: fork
allowed-tools: Bash, Read, Glob, Grep
---

<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Newsletter Service PR Readiness

You are checking whether local `lfx-v2-newsletter-service` commits are shaped correctly to open as a PR. This is a preflight gate for PR hygiene only: branch name, JIRA references, conventional commit subjects, rebase status, DCO + GPG signing, diff size, and protected files touched.

Do not audit implementation quality here. Do not run format, lint, build, or tests here. Run `/newsletter-service-preflight` after this shape check passes or after any shape issues are addressed.

**Output:** structured shape report with verdict `NOT READY | READY WITH CHANGES | READY`. No git mutations, no PR side effects.

## Phase 1: Parse Arguments

Args format: `[base-branch] [extra instructions]`.

- First token, if it looks like a ref or branch name, is the base branch.
- Default base: `origin/main`.
- If the base contains no `/`, normalize it to `origin/<base>`.
- Treat everything else as extra context for the report, not as permission to expand scope.

## Phase 2: Gather Shape Inputs

Run:

```bash
git fetch origin
git rev-parse --abbrev-ref HEAD
git diff --shortstat <base>...HEAD
git diff --name-only <base>...HEAD
git log --format='%H %s' <base>..HEAD
git log --format='%G? %h %s' <base>..HEAD
git log --format=%B <base>..HEAD
git merge-base --is-ancestor <base> HEAD; echo $?
```

If there are no commits between `<base>` and `HEAD`, stop with: `No commits to audit against <base>`.

## Phase 3: Protected Files

Flag protected files by intersecting `git diff --name-only <base>...HEAD` with this repo-specific list. Protected files are not forbidden, but the PR should call them out explicitly and the reviewer should understand the contract or deployment impact.

| Protected area | Paths |
| --- | --- |
| Newsletter DTO/API contracts | `pkg/api/newsletter.go`, `internal/handler/http.go`, `internal/handler/*.go`, `docs/newsletter-service-contract.md` |
| Embedded schema | `internal/schema/schema.go`, `internal/schema/schema.sql` |
| Postgres/Bun repository | `internal/repository/postgres.go` |
| Recipient resolution | `internal/service/send_orchestrator.go`, `internal/infrastructure/upstream/*.go`, `docs/recipient-resolution.md` |
| Charts | `charts/lfx-v2-newsletter-service/**` |
| Go module metadata | `go.mod`, `go.sum` |
| Build system | `Makefile` |
| Claude guidance | `CLAUDE.md`, `.claude/skills/**` |
| Contract docs | `docs/newsletter-service-contract.md`, `docs/recipient-resolution.md`, `docs/service-helm-chart.md` |

## Phase 4: Shape Checks

Emit at most one finding per check.

| Check | Rule | Severity when failing |
| --- | --- | --- |
| Branch name | Branch should include `LFXV2-<number>` and a descriptive slug. | `SHOULD_FIX` |
| JIRA reference | At least one commit subject or body should include `LFXV2-<number>`. | `CRITICAL` |
| Conventional commits | Every commit subject should match `type(scope): subject` or `type: subject`, where type is one of `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, or `revert`. | `CRITICAL` |
| Branch rebased | `git merge-base --is-ancestor <base> HEAD` should return `0`. | `SHOULD_FIX` |
| DCO + GPG signing | Every commit must have a `Signed-off-by:` trailer and `%G?` status `G` or `U`. | `CRITICAL` |
| Diff size | Report additions/deletions. Flag `SHOULD_FIX` above 800 additions unless the report explains why the size is expected. | `SHOULD_FIX` |
| Protected files | List every protected path touched and the protected area. | `SHOULD_FIX` |

Use this finding shape:

```json
{
  "severity": "CRITICAL | SHOULD_FIX | NIT",
  "rule": "newsletter-service-pr-readiness/<check-id>",
  "message": "...",
  "suggestion": "..."
}
```

## Phase 5: Render Report

```markdown
# Newsletter Service PR Readiness

**Branch:** `<current-branch>` -> `<base>`
**Commits:** N | **Additions:** +A | **Deletions:** -D
**Verdict:** NOT READY | READY WITH CHANGES | READY

## PR shape

| Check | Status | Detail |
| --- | --- | --- |
| Branch name | PASS | feat/LFXV2-1234-newsletter-drafts |
| JIRA ticket | PASS | Found LFXV2-1234 in commits |
| Conventional commits | PASS | 3/3 commit subjects valid |
| Branch rebased | PASS | origin/main is an ancestor |
| Diff size | PASS | +342 / -41 |
| DCO + GPG signing | PASS | 3/3 commits signed and signed off |
| Protected files | SHOULD_FIX | pkg/api/newsletter.go, docs/newsletter-service-contract.md |

## Findings

<one line per CRITICAL/SHOULD_FIX finding>
```

## Verdict Rules

- `NOT READY`: any `CRITICAL` finding.
- `READY WITH CHANGES`: zero `CRITICAL`, at least one `SHOULD_FIX`.
- `READY`: zero `CRITICAL`, zero `SHOULD_FIX`.

## Boundaries

- Do not review Go implementation patterns.
- Do not run mechanical checks; `/newsletter-service-preflight` owns license, format, lint, build, tests, protected-file reporting, commit verification, and PR summary.
- Do not create commits, branches, pushes, or PRs from this skill.
