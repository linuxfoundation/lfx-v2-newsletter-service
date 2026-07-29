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
| `httproute.yaml` | Gateway API routing for the project-scoped newsletter paths, the open pixel, and `/newsletters/unsubscribe`. |
| `ruleset.yaml` | Heimdall rules for authenticated API routes plus the unauthenticated open pixel and unsubscribe endpoint. |
| `heimdall-middleware.yaml` | Optional Traefik middleware resources when this chart owns them locally. |
| `externalsecret.yaml` | Optional ExternalSecret resources. |
| `networkpolicy.yaml` | Optional egress controls for DNS, NATS, OTel, Postgres, and external HTTPS (e.g. JWKS). |
| `service.yaml` | ClusterIP service. |
| `serviceaccount.yaml` | ServiceAccount. |

## Runtime Values

| Value | Env var | Notes |
| --- | --- | --- |
| `service.port` | `PORT` | HTTP listen port. |
| `app.logLevel` | `LOG_LEVEL` | `debug`, `info`, `warn`, or `error`. |
| `app.nats.url` | `NATS_URL` | Single NATS connection used by the email dispatcher, committee member client, and project metadata client. |
| `app.nats.timeout` / `maxReconnect` / `reconnectWait` | `NATS_TIMEOUT` / `NATS_MAX_RECONNECT` / `NATS_RECONNECT_WAIT` | Empty falls back to app defaults (10s / unlimited / 2s). |
| `app.send.fanoutEnabled` | `SEND_FANOUT_ENABLED` | Toggles real email dispatch; false validates and resolves recipients without sending. |
| `app.send.concurrency` | `SEND_CONCURRENCY` | Caps in-flight email-service requests during fan-out (default 5). |
| `app.send.jobTimeout` | `SEND_JOB_TIMEOUT` | Bounds the detached background fan-out after a send is accepted (default 30m). |
| `app.send.stuckSendTTL` | `STUCK_SEND_TTL` | Age after which a `sending` row stranded by a pod crash is recovered to `sent` (default 45m; app enforces ≥ jobTimeout + 5m). |
| `app.send.fromAddress` | `EMAIL_FROM_ADDRESS` | SMTP envelope From; domain must be in the email-service allowlist. |
| `app.send.fromAddressOverrides` | `EMAIL_FROM_ADDRESS_OVERRIDES` | Per-project From override as comma-separated `slug=address` pairs (e.g. `agentic-ai-foundation=newsletter@lfx.aaif.io`); each override domain must also be in the email-service allowlist. Empty disables overrides. |
| `app.send.replyToAllowedDomains` | `EMAIL_REPLY_TO_ALLOWED_DOMAINS` | Comma-separated domains a resolved sender email may use as Reply-To (subdomain suffix matching applies). A resolved address outside this list falls back to the draft's `ed_reply_email`. Must stay in sync with email-service's `SMTP_ALLOWED_REPLY_TO_DOMAINS` — a domain allowed here but not there still gets rejected by email-service. Empty falls back to the app default (`linuxfoundation.org`). |
| `app.unsubscribe.publicBaseURL` | `NEWSLETTER_PUBLIC_BASE_URL` | Externally-reachable origin used to build unsubscribe links. Defaults to `https://lfx-api.<lfx.domain>`. Required when fan-out is enabled. |
| `app.unsubscribe.secret` / `secretRef` | `NEWSLETTER_UNSUBSCRIBE_SECRET` | HMAC key signing unsubscribe tokens. Required when fan-out is enabled; prefer `secretRef`. |
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

`httproute.yaml` routes (regex path matches, so only the newsletter sub-paths of `/projects/...` route here):

- `^/projects/[^/]+/newsletters(/.*)?$`
- `^/projects/[^/]+/newsletter-opt-outs(/.*)?$`
- `^/projects/[^/]+/newsletter-opens/[^/]+$`
- `^/committees/[^/]+/newsletters$` (member-facing committee-scoped list)
- `/newsletters/unsubscribe` (exact)

When `heimdall.enabled=true`, the HTTPRoute attaches `heimdall-forward-body`.

`ruleset.yaml` authentication and authorization:

- Most authenticated project routes (newsletter CRUD, analytics, etc.) authenticate via OIDC and fall back to `allow_all` when `openfga.enabled=false`. When OpenFGA is enabled, these routes enforce `viewer` (read) or `writer` (write) roles.
- The opt-out list endpoint (`GET /projects/{project_uid}/newsletter-opt-outs`) returns PII (email addresses) and is **always fail-closed**: it uses direct `openfga_check` with the `auditor` role and does NOT have an `allow_all` fallback. This route is unreachable when `openfga.enabled=false` or OpenFGA is misconfigured — that is intentional for PII security.
- The opt-out delete endpoint (`DELETE /projects/{project_uid}/newsletter-opt-outs/{opt_out_id}`) mutates a user's consent record and is **always fail-closed**: it uses direct `openfga_check` with the `writer` role and does NOT have an `allow_all` fallback, matching the security posture of the list endpoint.
- The committee-scoped list (`GET /committees/{committee_uid}/newsletters`) is **always fail-closed**: it uses direct `openfga_check` with the `member` relation on `committee:{committee_uid}` and does NOT have an `allow_all` fallback. The live-membership check is the route's entire authorization boundary, so it is unreachable when `openfga.enabled=false` — that is intentional.
- The open pixel (`…/newsletter-opens/{newsletter_uid}`) and `/newsletters/unsubscribe` are intentionally unauthenticated because email clients request them without a user session (the unsubscribe link is authorized by its HMAC token).

`openfga.enabled=false` is the default. When false, most routes still work via `allow_all`, but the opt-out endpoints and the committee-scoped newsletter list become unreachable. Enable OpenFGA to access them.

## Local Development

Use `charts/lfx-v2-newsletter-service/values.local.yaml.example` as the starting point for local overrides. The local values file is gitignored at `charts/lfx-v2-newsletter-service/values.local.yaml`.

Common local paths:

- Run the binary against a local Postgres database.
- Run the chart in `cluster+database` mode with CloudNativePG on OrbStack or kind.
- Set `REQUIRE_USER_AUTH=false` only for local development.
- Point `app.nats.url` at the local platform NATS (default `nats://lfx-platform-nats.lfx.svc.cluster.local:4222`).
- Set `app.send.fanoutEnabled=false` to exercise recipient resolution without dispatching real mail.

## Change Checklist

- Update `CLAUDE.md` and this document when adding or renaming chart values.
- Update `docs/newsletter-service-contract.md` when chart changes affect public routes, auth behavior, or API behavior.
- Coordinate deployed values with `lfx-v2-argocd`.
- Coordinate shared chart conventions with `lfx-v2-helm`.
