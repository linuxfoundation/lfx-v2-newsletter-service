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
>
> If the plugin is missing, install with `/plugin marketplace add linuxfoundation/lfx-skills` then `/plugin install lfx-skills@lfx-skills`.

## Project Overview

The LFX V2 Newsletter Service is a Go microservice in the LFX v2 platform. It owns:

- **Persistence** of newsletter drafts and send history in PostgreSQL (CloudNativePG-backed).
- **Recipient resolution** via HTTP calls to the LFX v2 query service.
- **State transitions** for drafts (draft → sent).

> **Out of scope right now:** actual email delivery. `/newsletters/test-send`
> and `/newsletters/drafts/{id}/send` validate input and mark the persisted
> draft as sent — but do not dispatch any email. Wiring up a real email
> publisher (e.g. publishing to `lfx-v2-email-service` over NATS) is a
> planned follow-up.
>
> AI content generation continues to live in `lfx-v2-ui`; this service does
> not proxy AI calls.

## Repo Role

This repo owns newsletter drafts, sent-state persistence, recipient preview/count behavior, local open tracking, newsletter analytics, the newsletter HTTP API, and the service-local Helm chart. It consumes query-service for committee-member recipient lookup and persists the email-service `groupId` supplied by the caller when a draft is marked sent.

It does not currently dispatch email, render AI content, publish indexer messages, or emit FGA tuples.

## Authoritative Repo Docs

- `docs/newsletter-service-contract.md`: HTTP routes, public DTOs, ETag behavior, state transitions, errors, analytics, and open tracking.
- `docs/recipient-resolution.md`: query-service consumption, bearer-token propagation, recipient normalization, and email-service `groupId` handoff.
- `docs/service-helm-chart.md`: service-local chart values, Postgres database modes, Gateway/Heimdall wiring, and deployment handoffs.
- `charts/lfx-v2-newsletter-service/`: service-local Helm templates and defaults.

Read the relevant contract before changing `pkg/api`, handlers, database schema, recipient resolution, analytics, open tracking, or chart values. Update docs in the same PR as behavior changes.

## Consumed Cross-Repo Contracts

- Query API and pagination: `lfx-v2-query-service/docs/query-service-contract.md`
- Committee-member indexed data and tags: `lfx-v2-committee-service/docs/indexer-contract.md`
- Email send and engagement tracking: `lfx-v2-email-service/docs/email-service-contract.md`
- Shared service chart conventions: `lfx-v2-helm/docs/service-chart-patterns.md`
- Deployed values, image tags, database secrets, ExternalSecret wiring: `lfx-v2-argocd`

Use `lfx-skills:lfx` if an owner repo is missing locally, a path has moved, or the task needs additional peer repos.

## Key Technologies

- **Language**: Go 1.25+
- **HTTP**: stdlib `net/http` with Go 1.22+ mux pattern
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
├── model/                    # Pure data: Newsletter, Status, ContextType, CommitteeMember
├── port/                     # Interfaces: NewsletterRepository, CommitteeClient
└── errors.go                 # Sentinel errors: ErrNotFound, ErrVersionMismatch, ErrInvalidRequest, ErrAlreadySent

internal/service/
├── newsletter.go             # CRUD + validation + state transitions
└── send_orchestrator.go      # Resolve recipients, mark draft sent (no email dispatch)

internal/repository/
└── postgres.go               # bun-backed NewsletterRepository with optimistic locking

internal/schema/
├── schema.go                 # //go:embed schema.sql + Apply()
└── schema.sql                # Consolidated DDL (CREATE … IF NOT EXISTS)

internal/handler/
├── http.go                   # Routes() + JSON helpers + error mapper
├── drafts.go                 # /newsletters/drafts CRUD + ETag helpers
├── send.go                   # /send, /test-send, /recipients, /recipient-count
├── list.go                   # GET /newsletters unified list
├── analytics.go              # GET /newsletter-analytics/{id}
├── open.go                   # GET /newsletter-opens/{id} tracking pixel
├── health.go                 # /livez, /readyz
├── middleware.go             # JWKS auth (AuthValidator), request log
└── request_id.go             # X-Request-ID propagation

internal/infrastructure/
├── observability/
│   ├── log.go                # slog + OTel handler init
│   └── otel.go               # OTel SDK bootstrap
└── upstream/
    ├── committee_client.go   # HTTP client for committee/query service
    └── http_helpers.go       # bearer token context, JSON parser

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

> **CRITICAL — while the branch is pre-PR, post-commit reviews are mandatory.** After every commit on the local branch, launch the `lfx-skills:lfx-general-code-reviewer`, `lfx-skills:lfx-newsletter-service-code-reviewer`, and `lfx-skills:lfx-newsletter-service-learnings-reviewer` subagents in parallel via the Agent tool (`subagent_type: lfx-skills:lfx-general-code-reviewer` / `subagent_type: lfx-skills:lfx-newsletter-service-code-reviewer` / `subagent_type: lfx-skills:lfx-newsletter-service-learnings-reviewer`, all `run_in_background: true`) — then keep working while they run. If Claude displays plugin agents without the `lfx-skills:` namespace, use the equivalent displayed reviewer names. Before opening a PR, every running review must return clean (or remaining findings explicitly documented as trade-offs), the **full-branch sweep** must run clean if the branch has more than one commit (`branch` arg), AND `/newsletter-service-pr-readiness` must clear every Critical finding before `/newsletter-service-preflight` runs.
>
> **Once the PR is open, do NOT invoke these reviewers on iteration commits.** CodeRabbit + Copilot auto-trigger on every push and own the audit surface from that point. The central reviewers are pre-PR insurance only.

### Post-commit (pre-PR phase, after every commit, parallel, asynchronous)

1. **Commit your work.** `git commit -s -S`. Do not wait for any prior review to finish.
2. **Immediately launch all three reviewer subagents in parallel.** Issue three **Agent tool calls in a single message**:
   - **`lfx-skills:lfx-general-code-reviewer`** (`subagent_type: lfx-skills:lfx-general-code-reviewer`, `run_in_background: true`) — general senior code review for correctness, security, error handling, maintainability, tests, performance, and code truthfulness. Carries no repo-specific Newsletter Service rulebook.
   - **`lfx-skills:lfx-newsletter-service-code-reviewer`** (`subagent_type: lfx-skills:lfx-newsletter-service-code-reviewer`, `run_in_background: true`) — Newsletter Service convention and contract audit against this repo's `CLAUDE.md`, local skills, docs, contracts, Makefile, chart, and relevant sibling-service handoffs. Every repo-convention finding must be source-backed.
   - **`lfx-skills:lfx-newsletter-service-learnings-reviewer`** (`subagent_type: lfx-skills:lfx-newsletter-service-learnings-reviewer`, `run_in_background: true`) — empirical-pattern matching against `docs/reviews/knowledge-base/` (patterns sampled from past PR review comments on this repo). Every finding must quote a KB pattern entry.

   **Post-commit mode prompt (exact, all three subagents):** `target repo: lfx-v2-newsletter-service\n\nReview the latest commit.` Append `extra: <focus>` on a new line only when there is a priority hint to add. Do NOT pass `branch` here. If this work cycle is launched from the LFX workspace parent, the `target repo:` line is required so the reviewers operate in this repo.
3. **Keep working.** Start the next commit while the reviewers run. Do not block on them.
4. **When the reviews return:** read both reports. Roll every Critical finding and every reasonable Important finding into the next commit.

### Pre-PR (drain the queue, sweep cumulative state, then open)

When the work is done and no more code commits are planned:

1. **Wait for every running review to complete.**
2. **If any returned review flags Critical or reasonable Important:** add a fix commit, launch both reviewers again on the new state, wait, and loop until clean or explicitly documented as a trade-off.
3. **Full-branch sweep — only if the branch has more than one commit.** Launch all three reviewer subagents again in parallel via the Agent tool. The Agent `prompt` parameter for each subagent must include the `branch` keyword so the subagent audits the branch's diff against `origin/main` instead of just the latest commit:
   - **`lfx-skills:lfx-general-code-reviewer`**, prompt: **`target repo: lfx-v2-newsletter-service\nbranch\n\nReview the branch's diff against origin/main.`**
   - **`lfx-skills:lfx-newsletter-service-code-reviewer`**, prompt: **`target repo: lfx-v2-newsletter-service\nbranch\n\nReview the branch's diff against origin/main.`**
   - **`lfx-skills:lfx-newsletter-service-learnings-reviewer`**, prompt: **`target repo: lfx-v2-newsletter-service\nbranch\n\nReview the branch's diff against origin/main.`**

   Address any new findings, then re-run the sweep until clean.
4. **Run `/newsletter-service-pr-readiness`** for branch name, JIRA reference, conventional commits, rebase status, DCO + GPG signing, diff size, and protected files.
5. **Run `/newsletter-service-preflight`** for working tree status, license headers, formatting, lint/vet, build, tests, protected files, commit verification, and PR change summary.
6. **Only then push and open the PR.**

### Post-PR iteration (responding to bot feedback on an open PR)

1. Wait for CodeRabbit + Copilot to comment after each push.
2. Triage every Critical and reasonable Important finding against current code.
3. Roll fixes into a `fix(review): ...` commit.
4. Push. Repeat until clean.

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
- Domain errors live in `internal/domain/errors.go` (`ErrNotFound`, `ErrVersionMismatch`, `ErrInvalidRequest`, `ErrAlreadySent`).
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

| Service                    | Relationship                                                      |
| -------------------------- | ----------------------------------------------------------------- |
| `lfx-v2-query-service`     | Source of committee member emails (via `/query/resources` HTTP)   |
| `lfx-v2-ui` Express server | HTTP client; proxies UI requests to this service                  |
