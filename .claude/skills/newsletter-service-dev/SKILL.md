---
name: newsletter-service-dev
description: Repo-local Go coding conventions and implementation guidance for lfx-v2-newsletter-service. Auto-attaches when editing Go code, HTTP handlers, Postgres/Bun repository code, embedded schema, query-service recipient resolution, newsletter API DTOs, Makefile, Helm chart templates, or service-owned docs. Owns the newsletter HTTP API, draft/send state transitions, local analytics/open tracking, recipient resolution, tests, formatting, linting, and license headers. Central platform composition stays in lfx-skills:lfx-platform-architecture; cross-repo routing stays in lfx-skills:lfx.
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

# Development Conventions

Repo-owned conventions for `lfx-v2-newsletter-service`. This service owns newsletter drafts, sent-state persistence, recipient resolution, local open tracking, and newsletter analytics. It is not a Goa service, does not currently emit indexer or FGA messages, and does not currently dispatch emails itself.

Use this skill alongside:

- `lfx-skills:lfx` for cross-repo topology, owner lookup, and missing local checkouts.
- `lfx-skills:lfx-platform-architecture` for platform composition, service classes, query-service/FGA/indexer flows, and deployment handoffs.
- `docs/newsletter-service-contract.md` for this repo's HTTP API and public DTO contract.
- `docs/recipient-resolution.md` for query-service consumption and the email-service handoff.
- `docs/service-helm-chart.md` for service-local chart values, database modes, Gateway/Heimdall wiring, and deployment surfaces.

## Repo Layout

```text
cmd/newsletter-api/                 entry point, runtime config, dependency wiring
internal/domain/model/              newsletter aggregate, open event, analytics DTOs
internal/domain/port/               repository and upstream interfaces
internal/service/                   draft CRUD, validation, send-state orchestration, recipient hash helpers
internal/repository/                Postgres/Bun implementation and pagination cursor codec
internal/schema/                    embedded idempotent schema.sql, advisory-lock bootstrap
internal/handler/                   stdlib net/http routes, auth middleware, JSON/error mapping
internal/infrastructure/upstream/   query-service HTTP client and bearer-token propagation
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
- `UpdateDraft` and `SendDraft` use optimistic locking through strong ETags and `If-Match`.
- `SendDraft` requires a valid UUID `groupId`. That value is the `lfx-v2-email-service` correlation ID persisted on the newsletter row.
- Current send behavior is a state transition only. Email dispatch is performed outside this Go service today. Do not add a publisher without updating `docs/recipient-resolution.md`, `docs/newsletter-service-contract.md`, and the caller contract.

## Postgres And Schema

- All schema is embedded in `internal/schema/schema.sql` and applied at startup by `schema.Apply`.
- Schema bootstrap runs in a transaction under `pg_advisory_xact_lock`, with a local 60-second statement timeout.
- Repository code uses Bun over pgx/stdlib. Keep SQL ownership in `internal/repository/postgres.go` unless a new repository implementation is introduced.
- `ListAll` uses keyset pagination over `(updated_at, id)` and opaque base64-url page tokens. Treat tokens as service-owned and never document their internal shape as stable.
- `newsletter_opens` stores SHA-256 recipient hashes, not raw email addresses, and deduplicates repeat opens per recipient per hour.

## Recipient Resolution

- Recipient resolution uses the query service via HTTP `GET /query/resources`, with `type=committee_member` and committee UID tags.
- The inbound bearer token is validated by this service and then propagated to the upstream query-service request through context. Do not forward invalid tokens in local auth-disabled mode.
- The orchestrator deduplicates recipients by lowercased email and filters empty or obviously invalid addresses.
- Read `docs/recipient-resolution.md` and `lfx-v2-query-service/docs/query-service-contract.md` before changing query parameters, pagination handling, bearer-token propagation, or recipient selection.

## Logging And Observability

- Use `log/slog` with `slog.*Context` variants.
- Use `internal/infrastructure/observability.AppendCtx` for request-scoped fields when needed.
- Never log bearer tokens, JWTs, raw Authorization headers, database passwords, newsletter HTML bodies, or recipient lists.
- OpenTelemetry setup lives in `internal/infrastructure/observability/`; chart values map to `OTEL_*` env vars.
- Keep request logging in middleware, not duplicated in service methods.

## Errors

- Domain sentinels: `ErrNotFound`, `ErrVersionMismatch`, `ErrInvalidRequest`, `ErrAlreadySent`.
- Transport mapping:
  - not found -> 404
  - version mismatch -> 412
  - already sent -> 409
  - invalid request -> 400
  - upstream dependency failure -> 503
  - unexpected -> 500 with a generic client message
- Wrap upstream and database errors with `%w`. Do not return raw database or upstream HTTP response bodies directly to clients.

## Tests

- Prefer table-driven tests and co-locate `*_test.go` with the code under test.
- Depend on `internal/domain/port` interfaces for repositories and upstream clients.
- Use fake repositories or fake `CommitteeClient` implementations for service tests. Do not require a live query service for unit tests.
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

- This repo owns newsletter persistence and newsletter API behavior.
- `lfx-v2-query-service` owns generic query API behavior.
- `lfx-v2-committee-service` owns committee-member indexing and committee-member data emitted into query-service.
- `lfx-v2-email-service` owns transactional email delivery and engagement tracking records.
- `lfx-self-serve` owns the current UI/BFF fan-out to email-service and consumes this service's HTTP API.
- If a change requires a peer repo, use `lfx-skills:lfx` to locate or clone it and read that repo's `CLAUDE.md` plus contract docs.
