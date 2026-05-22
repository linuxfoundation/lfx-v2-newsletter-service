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

## Quick Start

Two supported paths for running the service locally:

- **Path A — Go binary against host Postgres.** Fastest inner loop. The
  service runs on the host; Postgres is whatever you already have installed
  (Homebrew, Postgres.app, Docker, etc.).
- **Path B — Helm + CloudNativePG on OrbStack/kind.** Mirrors production:
  the CNPG operator provisions an in-cluster Postgres and the chart wires
  everything together.

### Prerequisites

- Go 1.25+
- A running PostgreSQL 16+ instance (Path A) **or** OrbStack/kind with `kubectl`,
  `helm` 3.8+, and [`ko`](https://ko.build) (Path B)
- A reachable `lfx-v2-query-service` (or a stubbed `COMMITTEE_SERVICE_URL` —
  the service starts without it being live, but recipient resolution will fail)

---

### Path A — run the binary against host Postgres

**1. Create the database.**

```bash
psql postgres://localhost/postgres -c 'CREATE DATABASE newsletters;'
```

The service applies its DDL idempotently at startup (`internal/schema/schema.sql`);
you do **not** need to run any SQL files manually.

**2. Set the required environment variables.**

```bash
export DATABASE_URL='postgres://<your-user>@localhost:5432/newsletters?sslmode=disable'
export COMMITTEE_SERVICE_URL='http://localhost:8081'   # lfx-v2-query-service / API gateway
export REQUIRE_USER_AUTH=false                         # local only — production must verify JWTs
export LOG_LEVEL=debug
```

`sslmode=disable` is required for a vanilla Homebrew Postgres install, which
ships without TLS; pgx defaults to requiring SSL.

**3. Build and run.**

```bash
make run
```

On a successful start you should see something like:

```text
level=INFO msg="schema applied" tables=...
level=INFO msg="newsletter-api listening" addr=:8080
```

**4. Smoke-test.**

```bash
curl -s http://localhost:8080/livez && echo
# → ok
```

If you see `missing required env vars: DATABASE_URL, COMMITTEE_SERVICE_URL`,
the env vars above are not set in the shell you ran `make run` from — `make`
does **not** load your shell rc.

---

### Path B — Helm + CloudNativePG on OrbStack

**1. Install the CloudNativePG operator (once per cluster).**

```bash
make helm-install-operators
```

This installs the operator into `cnpg-system`. It is cluster-wide and only
needs to be installed once. Helm validates resources against installed CRDs
before applying any release, so the operator must exist before the umbrella
or standalone chart is installed — that's why it is not a subchart.

**2. Build the image with ko.**

```bash
make ko-build
```

This produces `ko.local/newsletter-api:local`. OrbStack shares the local
Docker image cache with Kubernetes, so no manual `kind load` or registry push
is needed.

**3. Create your local values override.**

```bash
cp charts/lfx-v2-newsletter-service/values.local.yaml.example \
   charts/lfx-v2-newsletter-service/values.local.yaml
```

The example file pins the chart to `database.mode=cluster+database`, points
`image.repository` at `ko.local/newsletter-api`, disables `requireUserAuth`,
and disables the NetworkPolicy for easier debugging. Adjust
`app.committeeServiceURL` to point at your local query-service if needed.

**4. Install the chart.**

```bash
make helm-install-local
```

Watch the operator provision the cluster, then the deployment come up:

```bash
kubectl get cluster,database,pods -n lfx --context orbstack
```

Once the pod is `1/1 Running`, the service has already applied its schema.
Tail the logs to confirm:

```bash
kubectl logs -n lfx -l app.kubernetes.io/name=lfx-v2-newsletter-service \
  --tail=50 --context orbstack
# → level=INFO msg="schema applied" ...
# → level=INFO msg="newsletter-api listening" addr=:8080
```

**5. Smoke-test via port-forward.**

```bash
kubectl port-forward -n lfx svc/lfx-v2-newsletter-service 18080:8080 \
  --context orbstack &

curl -s localhost:18080/livez && echo
# → ok
```

---

### Other install modes

The chart supports three `database.mode` values; pick the right Make target:

| Target                        | `database.mode`     | When to use                                                              |
| ----------------------------- | ------------------- | ------------------------------------------------------------------------ |
| `make helm-install-local`     | (from values.local) | Local OrbStack/kind dev (defaults to `cluster+database`)                 |
| `make helm-install-cnpg`      | `cluster+database`  | Standalone CNPG install — chart provisions both the Cluster and Database |
| `make helm-install-external`  | `external`          | Connect to an existing Postgres via a Kubernetes Secret                  |

`external` mode requires a secret in the target namespace whose key (default
`url`) holds the `DATABASE_URL`. Set `database.external.secretName` to that
secret's name in your values override.

### Teardown

```bash
make helm-uninstall                           # remove the chart release
kubectl delete namespace lfx --context orbstack  # remove the CNPG cluster + PVCs
```

The CNPG operator itself is left installed (it is cluster-wide). To remove it:
`helm uninstall cnpg -n cnpg-system`.

For Path A, drop the local database:

```bash
psql postgres://localhost/postgres -c 'DROP DATABASE IF EXISTS newsletters;'
```

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

| Method | Path                                  | Description                                  |
| ------ | ------------------------------------- | -------------------------------------------- |
| GET    | `/livez`                              | liveness probe                               |
| GET    | `/readyz`                             | readiness probe (DB ping)                    |
| POST   | `/newsletters/drafts`                 | create draft                                 |
| GET    | `/newsletters/drafts`                 | list drafts for a context                    |
| GET    | `/newsletters/drafts/{id}`            | fetch draft (returns ETag)                   |
| PUT    | `/newsletters/drafts/{id}`            | update draft (requires If-Match)             |
| DELETE | `/newsletters/drafts/{id}`            | delete draft                                 |
| POST   | `/newsletters/drafts/{id}/send`       | mark draft as sent (no email)                |
| POST   | `/newsletters/recipient-count`        | preview unique recipient count               |
| POST   | `/newsletters/recipients`             | preview recipient list                       |
| POST   | `/newsletters/test-send`              | validate-only stub (no email)                |
| GET    | `/newsletters`                        | unified list of newsletters for a context    |
| GET    | `/newsletter-analytics/{id}`          | per-newsletter analytics (opens, recipients) |
| GET    | `/newsletter-opens/{id}`              | open-tracking pixel (unauthenticated GIF)    |

Optimistic concurrency control: every draft carries an integer `version`
column atomically incremented on each `UPDATE`. `GET` returns
`ETag: "<version>"`; `PUT` requires `If-Match: "<version>"` and returns
`412 Precondition Failed` on a mismatch.

## Related Services

| Service                          | Relationship                                                  |
| -------------------------------- | ------------------------------------------------------------- |
| `lfx-v2-query-service`           | Source of committee member emails (via `/query/resources`)    |
| `lfx-v2-ui` (Express server)     | HTTP client; proxies UI requests to this service               |
