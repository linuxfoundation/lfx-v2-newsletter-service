# Claude Development Guide for LFX V2 Newsletter Service

> **Central LFX skills:**
>
> - `lfx-skills:lfx`: cross-repo topology, ownership routing, repo discovery, and missing-checkout handling.
> - `lfx-skills:lfx-platform-architecture`: platform composition, service classes, query-service/FGA/indexer flow, Helm and ArgoCD handoffs, and cross-service responsibility boundaries.
>
> **Repo-local skills:**
>
> - `newsletter-service-dev`: auto-attaches on Go, chart, and service-owned doc paths. It owns this repo's Go conventions, HTTP handler shape, Postgres/Bun persistence, embedded schema, recipient resolution, public `pkg/api` DTO contract, tests, formatting, linting, and license headers. See `.claude/skills/newsletter-service-dev/SKILL.md`.
> - `newsletter-service-pr-readiness`: pre-PR shape check only: branch/JIRA/conventional commits/rebase/DCO + GPG/diff size/protected files. See `.claude/skills/newsletter-service-pr-readiness/SKILL.md`.
> - `newsletter-service-preflight`: Go mechanical before-PR pipeline: working tree, license, formatting, lint/vet, build, tests, protected files, commit verification, and PR change summary. See `.claude/skills/newsletter-service-preflight/SKILL.md`.
> - `newsletter-service-agentic-pr`: the PR driver's operating manual for driving an OPEN PR through the agentic review flow — read the lfx-reviewer check comment, fix or rebut blocking findings, answer every thread, push one round at a time, and loop until green. On PR open, the main session launches the PR driver (worktree-isolated background agent) with a minimal prompt pointing at this skill. See `.claude/skills/newsletter-service-agentic-pr/SKILL.md`.
> - `newsletter-service-cut-release`: cuts a new GitHub release/tag from `main`, verifies the tagged build published the image and Helm chart, then opens a version-bump PR on `lfx-v2-argocd` pinning the target environments (never `dev`) to the new version. Opens the argocd PR only — never merges it. See `.claude/skills/newsletter-service-cut-release/SKILL.md`.
>
> If the plugin is missing, install with `/plugin marketplace add linuxfoundation/lfx-skills` then `/plugin install lfx-skills@lfx-skills`.

## Project Overview

The LFX V2 Newsletter Service is a Go microservice in the LFX v2 platform. It owns:

- **Persistence** of project-scoped newsletter drafts and send history in PostgreSQL (CloudNativePG-backed).
- **Recipient resolution** via NATS request/reply to committee-service (`lfx.committee-api.list_members`).
- **Email dispatch**: the send orchestrator mints the email-service `group_id`, renders email chrome, and fans out per-recipient sends to `lfx-v2-email-service` over NATS (`lfx.email-service.send_email`).
- **State transitions** for drafts (draft → sent; a draft is marked sent only when at least one recipient was delivered to).
- **Unsubscribe**: per-recipient HMAC-signed, project-scoped opt-out links served at `GET /newsletters/unsubscribe`.

> AI content generation does not live in this service; it does not proxy AI calls.

## Repo Role

This repo owns project-scoped newsletter drafts, sent-state persistence, recipient preview/count behavior, email dispatch and send fan-out, unsubscribe opt-outs, local open tracking, newsletter analytics, the newsletter HTTP API, and the service-local Helm chart. It consumes committee-service for committee-member recipient lookup, project-service for project name/slug, and auth-service for the sender's display name and primary email — all over NATS request/reply.

Newsletter delivery is provider-selectable via `EMAIL_PROVIDER`. The default (`email-service`) fans out to lfx-v2-email-service over NATS (SES), which also serves engagement analytics over NATS; `sendgrid` brokers SendGrid directly over HTTPS and owns engagement in a local store its signed event webhook populates. Either way, a `group_id` is minted and persisted when a draft is marked sent, each send stamps `send_provider`, and analytics is routed by `send_provider` so a newsletter's opens/delivered/failed totals always come from the store that dispatched it — local open-pixel rows overlaid on top.

It does not render AI content, publish indexer messages, or emit FGA tuples.

## Authoritative Repo Docs

- `docs/newsletter-service-contract.md`: HTTP routes, public DTOs, ETag behavior, state transitions, errors, analytics, open tracking, and unsubscribe.
- `docs/recipient-resolution.md`: committee-service member lookup over NATS, unsubscribe exclusion, recipient normalization, and the email-service send fan-out / `group_id` handoff.
- `docs/service-helm-chart.md`: service-local chart values, Postgres database modes, Gateway/Heimdall wiring, and deployment handoffs.
- `charts/lfx-v2-newsletter-service/`: service-local Helm templates and defaults.

Read the relevant contract before changing `pkg/api`, handlers, database schema, recipient resolution, analytics, open tracking, or chart values. Update docs in the same PR as behavior changes.

## Consumed Cross-Repo Contracts

- Committee members over NATS (`lfx.committee-api.list_members`): `lfx-v2-committee-service`
- Project name/slug over NATS (`lfx.projects-api.get_name` / `get_slug`): `lfx-v2-project-service`
- Email send and engagement tracking over NATS (`lfx.email-service.*`): `lfx-v2-email-service/docs/email-service-contract.md`
- Sender display name over NATS (`lfx.auth-service.user_metadata.read`): `lfx-v2-auth-service/docs/user_metadata.md`
- Sender primary email over NATS (`lfx.auth-service.user_emails.read`), used to resolve send-time Reply-To: `lfx-v2-auth-service/docs/subjects/user_emails.md`
- Shared service chart conventions: `lfx-v2-helm/docs/service-chart-patterns.md`
- Deployed values, image tags, database secrets, ExternalSecret wiring: `lfx-v2-argocd`

Use `lfx-skills:lfx` if an owner repo is missing locally, a path has moved, or the task needs additional peer repos.

## Key Technologies

- **Language**: Go 1.25+
- **HTTP**: stdlib `net/http` with Go 1.22+ mux pattern
- **Messaging**: NATS request/reply for service-to-service calls (committee, project, email, auth). Exception: when `EMAIL_PROVIDER=sendgrid`, newsletter delivery and engagement go direct to SendGrid over HTTPS (a signed event webhook feeds a local engagement store); email-service/SES over NATS is the default
- **Database**: PostgreSQL via [pgx](https://github.com/jackc/pgx) + [bun](https://bun.uptrace.dev), provisioned by [CloudNativePG](https://cloudnative-pg.io)
- **Schema**: single embedded `schema.sql` applied idempotently on startup (CREATE … IF NOT EXISTS), serialized across pods via a Postgres advisory transaction lock
- **Auth**: Heimdall-issued JWTs verified via JWKS (`MicahParks/keyfunc`)
- **Observability**: OpenTelemetry (traces, metrics, logs) + slog structured logging
- **Container**: Chainguard distroless images
- **Orchestration**: Kubernetes with Helm charts

## Architecture

```text
cmd/newsletter-api/
├── main.go                   # OTel bootstrap, DB pool, schema, HTTP server, graceful shutdown
└── service/
    ├── config.go             # ALL env var reads — no os.Getenv in other layers
    └── implementations.go    # Wires infrastructure into service structs

internal/domain/
├── model/                    # Pure data: Newsletter, Status, CommitteeMember, Unsubscribe
├── port/                     # Interfaces: NewsletterRepository, CommitteeClient, ProjectMetadataClient, EmailDispatcher, UserMetadataReader
└── errors.go                 # Sentinel errors: ErrNotFound, ErrVersionMismatch, ErrInvalidRequest, ErrAlreadySent, ErrSendInProgress

internal/service/
├── newsletter.go             # CRUD + validation + state transitions
├── send_orchestrator.go      # Resolve recipients, render chrome, fan out sends, mark draft sent
├── analytics.go              # Local opens overlaid with email-service engagement totals
├── unsubscribe.go            # HMAC token mint/verify + project-scoped opt-out persistence
└── render/
    └── email_chrome.go       # HTML/text email chrome around the draft body

internal/repository/
└── postgres.go               # bun-backed NewsletterRepository with optimistic locking

internal/schema/
├── schema.go                 # //go:embed schema.sql + Apply()
└── schema.sql                # Consolidated DDL (CREATE … IF NOT EXISTS)

internal/handler/
├── http.go                   # Routes() + JSON helpers + error mapper
├── drafts.go                 # /projects/{project_uid}/newsletters CRUD + ETag helpers
├── send.go                   # …/send, …/test-send, …/recipients, …/recipient-count
├── list.go                   # GET /projects/{project_uid}/newsletters unified list
├── analytics.go              # GET /projects/{project_uid}/newsletters/{newsletter_uid}/analytics
├── open.go                   # GET /projects/{project_uid}/newsletter-opens/{newsletter_uid} tracking pixel
├── unsubscribe.go            # GET /newsletters/unsubscribe (unauthenticated, HMAC token)
├── health.go                 # /livez, /readyz
├── middleware.go             # JWKS auth (AuthValidator), request log
└── request_id.go             # X-Request-ID propagation

internal/infrastructure/
├── observability/
│   ├── log.go                # slog + OTel handler init
│   └── otel.go               # OTel SDK bootstrap
├── nats/
│   ├── client.go             # NATS connection + request/reply helper
│   ├── subjects.go           # Centralized upstream subject constants
│   ├── committee_client.go   # lfx.committee-api.list_members
│   ├── project_client.go     # lfx.projects-api.get_name / get_slug
│   ├── email_dispatcher.go   # lfx.email-service.send_email + engagement analytics
│   └── user_metadata_client.go # lfx.auth-service.user_metadata.read + lfx.auth-service.user_emails.read
└── upstream/                 # Retired HTTP client package (placeholder only)

pkg/api/
└── newsletter.go             # Public contract: request/response DTOs

pkg/errors/
├── base.go                   # base typed-error helpers
├── client.go                 # client (4xx) error types
└── server.go                 # server (5xx) error types incl. ServiceUnavailable
```

## Build Commands

```bash
make build       # Compile binary to bin/lfx-v2-newsletter-service/newsletter-api
make test        # Run tests with race detector
make check       # fmt + lint + license-check + go vet
make lint        # golangci-lint
```

## Work cycle — post-commit and pre-PR reviews

> **CRITICAL — while the branch is pre-PR, local review is mandatory.** After every
> commit on the local branch, run `/lfx-skills:lfx-local-review`. It runs three reviewers in
> parallel — the central `general` brain plus this repo's own two brains — on Pi
> when Pi is available, and Claude subagents otherwise. Each returns an ordinary
> Markdown report. Before opening a PR, local review must come back with no
> findings (or with the remaining ones explicitly documented as trade-offs), AND
> `/newsletter-service-pr-readiness` must clear every Critical finding before
> `/newsletter-service-preflight` runs.
>
> **Local review stops at PR-open.** Once the PR exists, the agentic review flow
> owns iteration — do not run local review on iteration commits.

This repo owns two of the three reviewer brains, so they are versioned with the
code they describe:

- `.claude/skills/newsletter-service-code-reviewer/SKILL.md` — audits the change
  against this repo's written rule surface (`CLAUDE.md`, the repo-local skills, the
  `docs/` contracts). Every finding quotes a repo rule verbatim.
- `.claude/skills/newsletter-service-learnings-reviewer/SKILL.md` — matches the
  change against `docs/reviews/knowledge-base/`, the empirical patterns real
  reviewers have flagged on this repo's PRs. Every finding quotes a KB entry.

The `local-code-review` and `local-learnings-review` symlinks beside them are the
launcher's stable discovery aliases. They are directory symlinks — each points at
the sibling brain *directory*, not at its `SKILL.md` — and the launcher resolves
the alias to the physical file inside. Keep them pointing at those two
directories, and keep exactly one prose copy of each brain. `.agents/skills/` links
to the same two directories for non-Claude hosts. The `general` brain stays central; this repo never
holds a copy.

When you change this repo's conventions, contracts or KB, update the brain that
cites them in the same PR.

### Post-commit (pre-PR phase, after every commit)

1. **Commit your work.** `git commit -s -S`.
2. **Run `/lfx-skills:lfx-local-review`** — exactly that, from this repo, with no
   argument. It reviews **the newest commit only**: `HEAD^..HEAD`, the diff that
   commit introduced against its first parent. A caller may supply a direct base
   range instead; there is no repository-wide, cumulative or main-relative review.
   The host pins the target and base
   commits once and gives all three reviewers the same values, and the *code
   evidence* — the diff, the repo files under review, and the knowledge base —
   comes from those pinned Git objects, not from your checkout. Two things do
   not: the reviewers' own instruction files, which the launcher loads from your
   checkout by path, and any build, test or linter a reviewer chooses to run,
   which necessarily uses the working tree. So editing while a review runs is
   safe for ordinary code — but editing a reviewer's own `SKILL.md` mid-run
   changes the rulebook it is judging your commit by, and an optional check must
   be skipped or explicitly disclaimed as non-evidence if tracked content moved
   while it ran.
3. **Read the three Markdown reports in this session.** There is no report file and
   nothing is retained — the run lives and dies with the session, and a lost
   session means a fresh full run.
4. **Act on what they say:**
   - **No findings** — nothing to do; keep working.
   - **Findings** — the main session, never a reviewer, makes the fixes. Roll every
     supported finding into a follow-up commit, then rerun the complete trio on it.
     A finding you disagree with is a trade-off you document, not one you silently
     drop.
   - **A report whose first line is `INCOMPLETE — <reason>`, or a reviewer the host
     reports as failed or empty** — **not a pass.** The whole cycle is incomplete
     and the other two reports do not rescue it. Resolve the cause and rerun the
     **complete trio under one harness**. Never hand-patch a failed role, never
     rerun a single role, and never mix Pi and Claude results for the same run.
5. **Reviewer-driven follow-ups are ordinary commits.** Sign them (`git commit -s -S`)
   and use a conventional `fix(<scope>): ...` — or plain `fix: ...` where no scope
   fits — then rerun the complete trio.
6. **If the run used the Claude Opus fallback**, say so when you report it. It is
   not the intended Pi/GitHub-Copilot cross-model review.

### Pre-PR (drain, then open)

When the work is done and no more code commits are planned:

1. **Drain the reviews** — every commit has had its own clean local review, with no
   outstanding findings and no incomplete run. Local review is per-commit only:
   there is no cumulative sweep to run at the end, so a commit that never got a
   clean review does not get one retroactively here.
2. **Run `/newsletter-service-pr-readiness`** for branch name, JIRA reference,
   conventional commits, rebase status, DCO + GPG signing, diff size, and protected
   files.
3. **Run `/newsletter-service-preflight`** for working tree status, license headers,
   formatting, lint/vet, build, tests, protected files, commit verification, and the
   PR change summary.
4. **Only then push and open the PR** — and immediately launch the PR driver (see
   Post-PR iteration below).

Local review is **author-side only**. Reviewers may use ordinary local tooling —
shell, git, read-only GitHub inspection, and non-fixing builds, tests and linters
— but they never edit tracked source or config, run auto-fixing formatters or
generators, commit, reset, push, or create or update a label, status, check,
review, approval, comment or merge. Their checks may leave caches, binaries or
coverage files behind; that debris is yours to clean up, not theirs. If a reviewer
reports that a command modified tracked files, it is telling you deliberately
rather than fixing it silently. Nothing they produce feeds the conductor, the
escalation judge or the merge gate; their reports inform you, and you decide.

### Post-PR iteration (responding to bot feedback on an open PR)

Once the PR is open, the agentic review flow owns iteration, and the main
session's only job is to hand it off: **immediately after opening any PR —
without waiting to be asked — launch the PR driver**, a worktree-isolated
background general-purpose agent whose prompt is, in essence, "load the
`newsletter-service-agentic-pr` skill — or, if it is unavailable, read
`<main-checkout>/.claude/skills/newsletter-service-agentic-pr/SKILL.md`,
where `<main-checkout>` is a placeholder the main session substitutes with
its checkout's actual absolute filesystem path (never the literal
placeholder, and never the driver's own worktree copy, whose snapshot may
be stale) — and drive PR #N by it to a green check on the
current head with every thread answered, then report which ending applies",
plus the PR number, head SHA, and current status anchor per the skill's
"Launching the PR driver" section. The skill is the driver's operating
manual — do not restate its loop, conventions, liveness protocol, or
authority bounds in the prompt. The main session stays free for other work;
relay the driver's round notes to the user, including the final ending: the
driver drives the check to green even when the `needs-human` label is set,
and reports either "needs human review before merge" (label set) or "clear
for the gate/automerge path" — the latter only once the gate's approval
exists, or a current-head `needs-human: no` verdict does with no later
`needs-human` unlabel (the gate rejects a verdict superseded by an
unlabel it cannot attribute to an allowlisted human; label absence alone
means escalation is still pending, not clear). The driver has no merge
authority under any circumstances — a green, gate-approved PR is merged
from the main session only, and only on explicit human instruction. Only
skip the launch if the user asked to work the loop in this session.

## Conventions

### Config injection
All `os.Getenv` calls belong in `cmd/newsletter-api/service/config.go` →
`AppConfigFromEnv()`. Services receive a typed config struct, never call
`os.Getenv` themselves.

### Adding a new endpoint
1. Add the request/response DTO to `pkg/api/newsletter.go`.
2. Add the business-logic method to `internal/service/newsletter.go` or
   `send_orchestrator.go`.
3. Add the handler method to `internal/handler/`.
4. Register the route in `internal/handler/http.go`.

### Error handling
- Domain errors live in `internal/domain/errors.go` (`ErrNotFound`, `ErrVersionMismatch`, `ErrInvalidRequest`, `ErrAlreadySent`, `ErrSendInProgress`).
- Map domain errors to HTTP status codes in `internal/handler/http.go`.
- Always pass `ctx` for OTel trace correlation.

### Logging
- Use `slog.DebugContext`, `slog.InfoContext`, `slog.WarnContext`, `slog.ErrorContext`.
- Pass `ctx` so OTel trace correlation works.

### Optimistic concurrency control
Every draft row has a `version BIGINT` column. `Update` queries gate on
`id = $1 AND version = $2` and `version = version + 1`. If `RowsAffected = 0`,
follow up with an `Exists` check to distinguish `ErrNotFound` from
`ErrVersionMismatch`. Surface as `ETag: "<version>"` response header and
`If-Match: "<version>"` request header at the HTTP layer.

### License headers
Every `.go` file must start with:
```go
// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT
```

## Related Services

| Service                    | Relationship                                                              |
| -------------------------- | ------------------------------------------------------------------------- |
| `lfx-v2-committee-service` | Source of committee member emails (`lfx.committee-api.list_members` NATS) |
| `lfx-v2-project-service`   | Project name/slug lookup (`lfx.projects-api.get_name` / `get_slug` NATS)  |
| `lfx-v2-email-service`     | Per-recipient send fan-out and engagement analytics (NATS)                |
| `lfx-v2-auth-service`      | Sender display-name and primary-email lookup (`lfx.auth-service.user_metadata.read` / `user_emails.read` NATS) |
| LFX UI / Self Serve        | HTTP consumer of this service's project-scoped newsletter API             |
