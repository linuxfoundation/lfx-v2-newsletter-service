# Claude Development Guide for LFX V2 Newsletter Service

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

## Key Technologies

- **Language**: Go 1.25+
- **HTTP**: stdlib `net/http` with Go 1.22+ mux pattern
- **Database**: PostgreSQL via [pgx](https://github.com/jackc/pgx) + [bun](https://bun.uptrace.dev), provisioned by [CloudNativePG](https://cloudnative-pg.io)
- **Migrations**: [golang-migrate](https://github.com/golang-migrate/migrate) embedded with `//go:embed`
- **Auth**: Heimdall-issued JWTs verified via JWKS (`MicahParks/keyfunc`)
- **Observability**: OpenTelemetry (traces, metrics, logs) + slog structured logging
- **Container**: Chainguard distroless images
- **Orchestration**: Kubernetes with Helm charts

## Architecture

```text
cmd/newsletter-api/
├── main.go                   # OTel bootstrap, DB pool, migrations, HTTP server, graceful shutdown
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

internal/migrations/
├── migrations.go             # //go:embed *.sql
└── 000001_create_newsletters.{up,down}.sql

internal/handler/
├── http.go                   # Routes() + JSON helpers + error mapper
├── drafts.go                 # /newsletters/drafts CRUD
├── send.go                   # /send, /test-send, /recipients, /recipient-count
├── health.go                 # /livez, /readyz
└── middleware.go             # JWKS auth, request log

internal/infrastructure/
├── observability/
│   ├── log.go                # slog + OTel handler init
│   └── otel.go               # OTel SDK bootstrap
└── upstream/
    ├── committee_client.go   # HTTP client for committee/query service
    └── http_helpers.go       # bearer token context, JSON parser

pkg/api/
└── newsletter.go             # Public contract: request/response DTOs
```

## Build Commands

```bash
make build       # Compile binary to bin/lfx-v2-newsletter-service/newsletter-api
make test        # Run tests with race detector
make check       # fmt + lint + license-check + go vet
make lint        # golangci-lint
```

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
