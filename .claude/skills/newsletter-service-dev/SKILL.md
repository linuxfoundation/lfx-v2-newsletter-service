---
name: newsletter-service-dev
description: Repo-local Go coding conventions and implementation guidance for lfx-v2-newsletter-service. Auto-attaches when editing Go code, HTTP handlers, Postgres/Bun repository code, embedded schema, NATS upstream clients, recipient resolution, email fan-out, newsletter API DTOs, Makefile, Helm chart templates, or service-owned docs. Owns the newsletter HTTP API, draft/send state transitions, email dispatch, unsubscribe, local analytics/open tracking, recipient resolution, tests, formatting, linting, and license headers. Central platform composition stays in lfx-skills:lfx-platform-architecture; cross-repo routing stays in lfx-skills:lfx.
paths:
  - "**/*.go"
  - "go.mod"
  - "go.sum"
  - "Makefile"
  - "cmd/**"
  - "internal/**"
  - "pkg/**"
  - "charts/**"
  - "docs/**"
  - ".claude/skills/newsletter-service-dev/**"
allowed-tools: Read, Glob, Grep, Edit, Write, Bash
---

<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Development Conventions

Repo-owned conventions for `lfx-v2-newsletter-service`. This service owns project-scoped newsletter drafts, sent-state persistence, recipient resolution, email dispatch (per-recipient fan-out to email-service over NATS), unsubscribe opt-outs, local open tracking, and newsletter analytics. It is not a Goa service and does not emit indexer or FGA messages.

Use this skill alongside:

- `lfx-skills:lfx` for cross-repo topology, owner lookup, and missing local checkouts.
- `lfx-skills:lfx-platform-architecture` for platform composition, service classes, query-service/FGA/indexer flows, and deployment handoffs.
- `docs/newsletter-service-contract.md` for this repo's HTTP API and public DTO contract.
- `docs/recipient-resolution.md` for committee-service member lookup over NATS and the email-service fan-out.
- `docs/service-helm-chart.md` for service-local chart values, database modes, Gateway/Heimdall wiring, and deployment surfaces.

## Repo Layout

```text
cmd/newsletter-api/                 entry point, runtime config, dependency wiring
internal/domain/model/              newsletter aggregate, open event, unsubscribe, analytics DTOs
internal/domain/port/               repository and upstream interfaces (committee, project, email, user metadata)
internal/service/                   draft CRUD, validation, send orchestration + fan-out, unsubscribe, analytics, email chrome render
internal/repository/                Postgres/Bun implementation and pagination cursor codec
internal/schema/                    embedded idempotent schema.sql, advisory-lock bootstrap
internal/handler/                   stdlib net/http routes, auth middleware, JSON/error mapping
internal/infrastructure/nats/       NATS request/reply clients (committee, project, email dispatcher, user metadata) and subject constants
internal/infrastructure/upstream/   retired HTTP client package (placeholder only)
internal/infrastructure/observability/ slog and OpenTelemetry setup
pkg/api/                            public request/response DTOs
pkg/errors/                         typed client/server error helpers
charts/lfx-v2-newsletter-service/   service-local Helm chart
docs/                               service-owned contract and deployment docs
```

Match the existing package boundaries before adding a new abstraction.

## Public API And DTOs

- `pkg/api/newsletter.go` is the public DTO contract consumed by Self Serve and other callers.
- Route registration lives in `internal/handler/http.go`. Keep transport concerns in handlers and business rules in `internal/service/`.
- `decodeJSON` rejects unknown fields and caps request bodies at 1 MiB. Preserve this for user-supplied newsletter HTML.
- Errors map at `internal/handler/http.go::classifyError`. Domain sentinel errors live in `internal/domain/errors.go`.
- Update `docs/newsletter-service-contract.md` in the same PR as any route, payload, status code, ETag, or error-shape change.

## Drafts And Send State

- Draft lifecycle is `draft -> sent`; sent drafts cannot be modified, deleted, or sent again.
- Update and send use optimistic locking through strong ETags and `If-Match`.
- `SendNewsletter` mints the email-service `group_id` (`uuid.NewString()`) and persists it on the newsletter row when the draft is marked sent. The DB enforces UUID format and `status='sent' => group_id IS NOT NULL`.
- The send orchestrator owns email dispatch: it renders chrome, resolves the sender display name, and fans out per-recipient `lfx.email-service.send_email` requests with bounded concurrency. The draft flips to sent only when at least one recipient was delivered to; a fully-failed fan-out stays a draft for retry.
- `SEND_FANOUT_ENABLED=false` short-circuits dispatch for dev/staging shake-out. Keep `docs/recipient-resolution.md` and `docs/newsletter-service-contract.md` updated in the same PR as any send-behavior change.

## Postgres And Schema

- All schema is embedded in `internal/schema/schema.sql` and applied at startup by `schema.Apply`.
- Schema bootstrap runs in a transaction under `pg_advisory_xact_lock`, with a local 60-second statement timeout.
- Repository code uses Bun over pgx/stdlib. Keep SQL ownership in `internal/repository/postgres.go` unless a new repository implementation is introduced.
- `ListAll` uses keyset pagination over `(updated_at, id)` and opaque base64-url page tokens. Treat tokens as service-owned and never document their internal shape as stable.
- `newsletter_opens` stores SHA-256 recipient hashes, not raw email addresses, and deduplicates repeat opens per recipient per hour.

## Recipient Resolution

- Recipient resolution calls committee-service over NATS (`lfx.committee-api.list_members`), one request per committee UID, concurrently via `errgroup.WithContext`.
- The inbound bearer token is validated by this service but never attached to context or forwarded — outbound NATS calls carry no token (trust is enforced upstream of NATS). Keep it that way.
- The orchestrator deduplicates recipients by lowercased email, filters empty or obviously invalid addresses, and excludes the project's unsubscribed addresses.
- Subject constants live in `internal/infrastructure/nats/subjects.go`; keep them in sync with the owning services. Read `docs/recipient-resolution.md` before changing member lookup, unsubscribe exclusion, or recipient selection.

## Logging And Observability

- Use `log/slog` with `slog.*Context` variants.
- Use `internal/infrastructure/observability.AppendCtx` for request-scoped fields when needed.
- Never log bearer tokens, JWTs, raw Authorization headers, database passwords, newsletter HTML bodies, or recipient lists.
- OpenTelemetry setup lives in `internal/infrastructure/observability/`; chart values map to `OTEL_*` env vars.
- Keep request logging in middleware, not duplicated in service methods.

## Errors

- Domain sentinels: `ErrNotFound`, `ErrVersionMismatch`, `ErrInvalidRequest`, `ErrAlreadySent`, `ErrSendInProgress`, `ErrEmailNotDispatched`, `ErrEmailServiceUnreachable`.
- The last two are the exception to the transport mapping below: they classify per-recipient send failures inside the background fan-out (retry-safety and outage fail-fast) and never reach `classifyError`.
- Transport mapping:
  - not found -> 404
  - version mismatch -> 412
  - already sent -> 409
  - invalid request -> 400
  - upstream dependency failure -> 503
  - unexpected -> 500 with a generic client message
- Wrap upstream and database errors with `%w`. Do not return raw database errors or upstream NATS reply bodies directly to clients. NATS upstream clients return typed `pkgerrors.*` wrappers matched with `errors.As` in `classifyError`.

## Tests

- Prefer table-driven tests and co-locate `*_test.go` with the code under test.
- Depend on `internal/domain/port` interfaces for repositories and upstream clients.
- Use fake repositories or fake `CommitteeClient`/`EmailDispatcher` implementations for service tests. Do not require a live NATS server for unit tests.
- Handler tests should exercise `http.Handler` paths and assert status, ETag, and JSON bodies where the route contract changes.
- Run `make test` before handoff; it enables the race detector.

## Formatting, Linting, License

- `make fmt` runs `go fmt ./...` and `gofmt -s`.
- `make lint` installs/runs the repo-pinned golangci-lint version when needed.
- `make check` runs format, lint, license check, and `go vet ./...`.
- Every new `.go` file starts with:

  ```go
  // Copyright The Linux Foundation and each contributor to LFX.
  // SPDX-License-Identifier: MIT
  ```

- Every new `.md` file in this repo starts with the HTML-comment license header.
- Document exported Go symbols when the linter requires it. Add implementation comments only where they clarify non-obvious behavior.

## References

- `references/go-http-postgres-conventions.md`: package boundaries, handler shape, Postgres conventions, upstream calls, tests, and review checklist for this repo.

## Chart Work

- Service-local chart truth lives under `charts/lfx-v2-newsletter-service/` and `docs/service-helm-chart.md`.
- Shared chart conventions live in `lfx-v2-helm/docs/service-chart-patterns.md`.
- Deployed values, image tags, database secret names, and environment promotion live in `lfx-v2-argocd`.
- `openfga.enabled` is currently reserved for future use. Do not add FGA/indexer contract docs unless this service starts emitting those contracts.

## Boundaries

- This repo owns newsletter persistence, newsletter API behavior, and the newsletter email fan-out.
- `lfx-v2-committee-service` owns committee-member data and the `lfx.committee-api.list_members` payload.
- `lfx-v2-project-service` owns project name/slug lookup payloads.
- `lfx-v2-email-service` owns transactional email delivery, the `send_email` payload, and engagement tracking records.
- `lfx-v2-auth-service` owns the user-metadata payload used for sender display names.
- The LFX UI consumes this service's HTTP API; it no longer talks to email-service directly for newsletters.
- If a change requires a peer repo, use `lfx-skills:lfx` to locate or clone it and read that repo's `CLAUDE.md` plus contract docs.
