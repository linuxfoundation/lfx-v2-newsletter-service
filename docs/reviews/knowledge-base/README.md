<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Newsletter Service Review Knowledge Base

This is a **STARTER KB** — an empirical, repo-owned record of the patterns that human reviewers and review bots have actually flagged on `lfx-v2-newsletter-service` PRs, distilled into mechanically-detectable rules. It is **expected to grow** as the repo accumulates more merged PRs. It is read by the `lfx-skills:lfx-newsletter-service-learnings-reviewer` subagent, which matches a diff against these patterns and emits only findings that quote a pattern entry.

This KB is the *empirical* surface. It deliberately does NOT duplicate:

- `lfx-skills:lfx-general-code-reviewer` — generic correctness / security / test intuition.
- `lfx-skills:lfx-newsletter-service-code-reviewer` — the documented rule surface (`CLAUDE.md`, `.claude/skills/newsletter-service-dev`, contract docs, chart docs, Makefile).

## Methodology

Built per `lfx-architecture-scratch/2026-05-DevX-Time-to-Merge/service-kb-research-playbook.md` (thin-corpus mode). Corpus: merged PRs only. For each PR we pulled inline review threads, review bodies, PR conversation, the diff, and GraphQL `reviewThreads` resolution state, then clustered raw comments into candidate patterns and applied the promotion gate (repo-specific + mechanically detectable + currently relevant + not tooling-enforced; with at least one of recurrence / cost-of-miss / acted-on-by-maintainer). In a ~7-PR corpus, recurrence is rarely reachable, so most entries clear the B gate on **cost-of-miss** or **acted-on-by-maintainer** (resolved by a code change, often endorsed by the maintainer reviewer dealako or merged by andrest50 / niravpatel27).

## Corpus stats

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
| `recipient-resolution-and-http.md` | 6 (all Important) | `send_orchestrator.go`, `internal/infrastructure/nats/**`, `internal/handler/send.go`, `internal/handler/http.go`, or `internal/service/newsletter.go` changed |
| `chart.md` | 4 (2 Critical, 1 Important, 1 Nit) | any `charts/lfx-v2-newsletter-service/**` changed |

**Total: 22 patterns** + `known-false-positives.md` (7 entries).

## Highest-value patterns

The maintainer-blocking, cost-of-miss patterns from PR #3 and the contract-integrity patterns from PR #9:

- `security/unauthenticated-pixel-unbounded-write` — the open pixel takes attacker-controlled input on an unauthenticated path.
- `security/5xx-error-body-leak` and `security/jwt-error-leak-to-client` — infra detail leaking to unauthenticated callers.
- `persistence/non-immutable-generated-column-expr` and `persistence/on-conflict-target-mismatch` — both break `schema.sql` apply or runtime inserts; the latter recurred across PRs #3 and #6.
- `recipient/errgroup-cancellation-on-failure` and `recipient/unbounded-pagination-loop` — orphaned/infinite upstream work.
- `chart/db-password-composed-into-pod-spec` — DB password exposed in the Pod spec.

## Maintenance

This KB does not yet clear a "maintained KB" bar — it is a thin starter set. Add entries as new PRs surface repo-specific, mechanically-detectable, acted-on patterns. Move anything the team decides is noise into `known-false-positives.md`. Promote a Nit/Important to a higher tier only on recurrence or maintainer endorsement.

_Last built: 2026-05-29._
