<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Service Helm Chart

This document owns the local chart interface for `lfx-v2-newsletter-service`.

Shared chart conventions live in `lfx-v2-helm/docs/service-chart-patterns.md`. Deployed values, image tags, database secret names, ExternalSecret mappings, and environment promotion live in `lfx-v2-argocd`.

## Chart Location

`charts/lfx-v2-newsletter-service/`

Important templates:

| Template | Purpose |
| --- | --- |
| `deployment.yaml` | Container image, env vars, Postgres secret wiring, probes, resources. |
| `database.yaml` | Optional CloudNativePG `Cluster` and `Database` resources. |
| `httproute.yaml` | Gateway API routing for `/newsletters`, `/newsletter-analytics`, and `/newsletter-opens`. |
| `ruleset.yaml` | Heimdall rules for authenticated API routes and the unauthenticated open pixel. |
| `heimdall-middleware.yaml` | Optional Traefik middleware resources when this chart owns them locally. |
| `externalsecret.yaml` | Optional ExternalSecret resources. |
| `networkpolicy.yaml` | Optional egress controls for OTel, upstream HTTP services, and Postgres. |
| `service.yaml` | ClusterIP service. |
| `serviceaccount.yaml` | ServiceAccount. |

## Runtime Values

| Value | Env var | Notes |
| --- | --- | --- |
| `service.port` | `PORT` | HTTP listen port. |
| `app.logLevel` | `LOG_LEVEL` | `debug`, `info`, `warn`, or `error`. |
| `app.committeeServiceURL` | `COMMITTEE_SERVICE_URL` | Base URL that routes `/query/resources` to query-service. Required in production. |
| `app.requireUserAuth` | `REQUIRE_USER_AUTH` | Disable only for local development. |
| `app.jwksURL` | `JWKS_URL` | Required when auth is enabled. |
| `app.audience` | `JWT_AUDIENCE` | Must match Heimdall `create_jwt` audience. |
| `database.*` | `DATABASE_URL` or `PG*` | See database modes below. |
| `app.extraEnv` | varies | Escape hatch for additional env vars. |
| `app.otel.*` | `OTEL_*` | OpenTelemetry configuration. |

## Database Modes

| Mode | Behavior |
| --- | --- |
| `external` | Reads an existing Postgres connection from a Kubernetes Secret. |
| `database` | Creates a CloudNativePG `Database` CR pointing at an existing `Cluster`. |
| `cluster+database` | Creates both the CloudNativePG `Cluster` and `Database` resources. |

In CNPG modes and `external.shape=fields`, the deployment forwards `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, and `PGDATABASE` separately. The app composes `DATABASE_URL` in-process so the password is not expanded into the pod spec.

## Gateway And Heimdall

`httproute.yaml` routes:

- `/newsletters`
- `/newsletters/`
- `/newsletter-analytics/`
- `/newsletter-opens/`

When `heimdall.enabled=true`, the HTTPRoute attaches `heimdall-forward-body`.

`ruleset.yaml`:

- Authenticates normal API routes through OIDC and creates the service JWT.
- Uses `allow_all` today rather than direct `openfga_check` rules.
- Leaves `/newsletter-opens/{id}` unauthenticated because email clients request it without a user session.

`openfga.enabled` is currently reserved for future use. Do not add FGA contract docs unless the service starts enforcing or emitting concrete FGA behavior.

## Local Development

Use `charts/lfx-v2-newsletter-service/values.local.yaml.example` as the starting point for local overrides. The local values file is gitignored at `charts/lfx-v2-newsletter-service/values.local.yaml`.

Common local paths:

- Run the binary against a local Postgres database.
- Run the chart in `cluster+database` mode with CloudNativePG on OrbStack or kind.
- Set `REQUIRE_USER_AUTH=false` only for local development.
- Point `app.committeeServiceURL` at a local query-service or API gateway.

## Change Checklist

- Update `CLAUDE.md` and this document when adding or renaming chart values.
- Update `docs/newsletter-service-contract.md` when chart changes affect public routes, auth behavior, or API behavior.
- Coordinate deployed values with `lfx-v2-argocd`.
- Coordinate shared chart conventions with `lfx-v2-helm`.
