# LFX One Newsletter Service

A Go microservice in the LFX v2 platform that owns newsletter persistence and
the draft → sent state transition.

## Responsibilities

- Persist newsletter drafts and sent history (CloudNativePG-backed Postgres).
- Resolve recipient lists from committees (read-only HTTP calls to the LFX v2
  query service).
- Expose an HTTP REST API consumed by the lfx-v2-ui Express server.

> **Out of scope right now:** actual email delivery. `/newsletters/test-send`
> and `/newsletters/drafts/{id}/send` validate inputs, resolve recipient counts,
> and (for `/send`) flip the draft to `status=sent` in the database — but they
> do **not** dispatch any email. Wiring up a real email publisher
> (e.g. publishing to `lfx-v2-email-service` over NATS) is a planned follow-up.
> AI content generation continues to live in lfx-v2-ui.

## Key Technologies

- **Language**: Go 1.25+
- **HTTP**: stdlib `net/http` with Go 1.22+ mux pattern
- **Database**: PostgreSQL via [pgx](https://github.com/jackc/pgx) + [bun](https://bun.uptrace.dev),
  provisioned by [CloudNativePG](https://cloudnative-pg.io) in cluster
- **Schema**: single embedded `schema.sql` applied idempotently on startup (CREATE … IF NOT EXISTS), serialized across pods via a Postgres advisory transaction lock
- **Observability**: OpenTelemetry (traces, metrics, logs) + slog structured logging
- **Container**: Chainguard distroless images
- **Orchestration**: Kubernetes with Helm charts

## Architecture

```text
cmd/newsletter-api/
├── main.go                   # bootstrap: OTel, DB pool, schema, HTTP, graceful shutdown
└── service/
    ├── config.go             # env var reads — single source of truth
    └── implementations.go    # wires infrastructure into service structs

internal/domain/
├── model/                    # Newsletter, Status, ContextType, CommitteeMember
├── port/                     # interfaces: NewsletterRepository, CommitteeClient
└── errors.go                 # ErrNotFound, ErrVersionMismatch, ErrInvalidRequest, ErrAlreadySent

internal/service/
├── newsletter.go             # CRUD + validation + state transitions
└── send_orchestrator.go      # resolve recipients, mark draft sent (no email dispatch)

internal/repository/
└── postgres.go               # bun-backed NewsletterRepository with optimistic locking

internal/schema/
├── schema.go                 # //go:embed schema.sql + Apply()
└── schema.sql                # consolidated DDL (CREATE … IF NOT EXISTS)

internal/handler/
├── http.go                   # Routes(), JSON helpers
├── drafts.go                 # /newsletters/drafts CRUD handlers
├── send.go                   # send / test-send / recipients handlers
├── health.go                 # /livez and /readyz
└── middleware.go             # JWKS auth, request log

internal/infrastructure/
├── observability/            # OTel SDK + slog handler
└── upstream/                 # HTTP client for committee/query service

pkg/api/
└── newsletter.go             # public DTOs (mirror lfx-v2-ui shared interfaces)

charts/lfx-v2-newsletter-service/   # Helm chart with three database.mode options
```

## Build Commands

```bash
make build           # compile to bin/lfx-v2-newsletter-service/newsletter-api
make test            # go test -race
make check           # fmt + lint + license-check + go vet
make docker-build    # build OCI image
make helm-templates  # render Helm chart locally
```

## Database modes

The Helm chart supports three database modes (matching the upstream CloudNativePG
example):

| Mode               | Description                                                                                 |
| ------------------ | ------------------------------------------------------------------------------------------- |
| `external`         | Connect to an existing Postgres via a Kubernetes Secret containing `DATABASE_URL`           |
| `database`         | Create a CloudNativePG `Database` CR pointing at an existing `Cluster`                      |
| `cluster+database` | Create both a `Cluster` and a `Database` CR (standalone deployment without an umbrella)     |

`external` is the default for the standalone chart and the recommended mode for
production (per-service Postgres roles with least-privilege secrets).

## HTTP API

| Method | Path                                  | Description                       |
| ------ | ------------------------------------- | --------------------------------- |
| GET    | `/livez`                              | liveness probe                    |
| GET    | `/readyz`                             | readiness probe (DB ping)         |
| POST   | `/newsletters/drafts`                 | create draft                      |
| GET    | `/newsletters/drafts`                 | list drafts for a context         |
| GET    | `/newsletters/drafts/{id}`            | fetch draft (returns ETag)        |
| PUT    | `/newsletters/drafts/{id}`            | update draft (requires If-Match)  |
| DELETE | `/newsletters/drafts/{id}`            | delete draft                      |
| POST   | `/newsletters/drafts/{id}/send`       | mark draft as sent (no email)     |
| POST   | `/newsletters/recipient-count`        | preview unique recipient count    |
| POST   | `/newsletters/recipients`             | preview recipient list            |
| POST   | `/newsletters/test-send`              | validate-only stub (no email)     |

Optimistic concurrency control: every draft carries an integer `version`
column atomically incremented on each `UPDATE`. `GET` returns
`ETag: "<version>"`; `PUT` requires `If-Match: "<version>"` and returns
`412 Precondition Failed` on a mismatch.

## Related Services

| Service                          | Relationship                                                  |
| -------------------------------- | ------------------------------------------------------------- |
| `lfx-v2-query-service`           | Source of committee member emails (via `/query/resources`)    |
| `lfx-v2-ui` (Express server)     | HTTP client; proxies UI requests to this service               |
