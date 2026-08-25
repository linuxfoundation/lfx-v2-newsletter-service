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
| `httproute.yaml` | Gateway API routing for the project-scoped newsletter paths, the open pixel, `/newsletters/unsubscribe`, and (when the webhook key is configured) the SendGrid event webhook. |
| `ruleset.yaml` | Heimdall rules for authenticated API routes plus the unauthenticated open pixel, unsubscribe, and (when the webhook key is configured) SendGrid-webhook endpoints. |
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
| `app.send.fromAddress` | `EMAIL_FROM_ADDRESS` | Envelope From. Its domain must be authenticated by the active provider: the email-service allowlist (`SMTP_ALLOWED_FROM_DOMAINS`) in the default path, or `SENDGRID_AUTHENTICATED_DOMAINS` when `provider=sendgrid`. |
| `app.send.fromAddressOverrides` | `EMAIL_FROM_ADDRESS_OVERRIDES` | Per-project From override as comma-separated `slug=address` pairs (e.g. `agentic-ai-foundation=newsletter@lfx.aaif.io`); each override domain must also be authenticated by the active provider (see `fromAddress`). Empty disables overrides. |
| `app.send.replyToAllowedDomains` | `EMAIL_REPLY_TO_ALLOWED_DOMAINS` | Comma-separated domains a resolved sender email may use as Reply-To (subdomain suffix matching applies). A resolved address outside this list falls back to the draft's `ed_reply_email`. Must stay in sync with email-service's `SMTP_ALLOWED_REPLY_TO_DOMAINS` — a domain allowed here but not there still gets rejected by email-service. Empty falls back to the app default (`linuxfoundation.org`). |
| `app.send.schedule.maxHorizon` | `NEWSLETTER_SCHEDULE_MAX_HORIZON` | Caps how far in the future a schedule can be armed (default and hard ceiling `72h`, SendGrid Mail Send's `send_at` limit). A value above 72h fails app startup validation. Scheduling is SendGrid-only (see `app.send.provider`). |
| `app.send.schedule.minLead` | `NEWSLETTER_SCHEDULE_MIN_LEAD` | Minimum lead time required to arm a new schedule (default `5m`). |
| `app.send.schedule.cancelBuffer` | `NEWSLETTER_SCHEDULE_CANCEL_BUFFER` | Rejects a cancel-schedule request inside this window before the armed `scheduled_at` (minimum `10m`, default `10m`) — SendGrid's API does not permit cancellation within 10 minutes of `send_at`. |
| `app.send.provider` | `EMAIL_PROVIDER` | `email-service` (default; fan-out to email-service/SES over NATS) or `sendgrid` (direct SendGrid). Gates only outbound send env; enable SendGrid here, not via `extraEnv`. The webhook route/rule are gated separately on `app.send.sendgrid.webhookPublicKeySecretRef`. An unknown value fails the Helm render. |
| `app.send.sendgrid.authenticatedDomains` | `SENDGRID_AUTHENTICATED_DOMAINS` | Comma-separated authenticated sending domains; a From outside it is rejected before SendGrid. Required for `provider=sendgrid` outside local/dev. |
| `app.send.sendgrid.apiKeySecretRef` | `SENDGRID_API_KEY` | Sources the SendGrid API key from a Kubernetes Secret (typically the chart's ExternalSecret target). Required for real sends. |
| `app.send.sendgrid.webhookPublicKeySecretRef` | `SENDGRID_WEBHOOK_PUBLIC_KEY` | Sources the ECDSA signed-event-webhook key from a Secret. Setting this ref (independent of `provider`) wires the webhook env, HTTPRoute path, and RuleSet rule, so ingestion survives a flip back to email-service. Required when set — configure it only once the key exists; empty renders no webhook wiring. |
| `app.selfServeBaseURL` | `LFX_SELF_SERVE_BASE_URL` | Base URL of the LFX Self-Serve app used to build the compliance footer's "My Newsletters" deep link (`<base>/newsletters/my`). Empty falls back to the app default (`https://app.lfx.dev`); set per environment in deployment values (e.g. `https://app.staging.lfx.dev`). |
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

### Rolling deployment and schema column additions

`schema.Apply` runs at every pod startup (idempotent, advisory-locked), so during a rolling update a new pod can apply an `ALTER TABLE ... ADD COLUMN` while old pods still serve traffic. Repository code uses `Returning("*")` on newsletter/publication inserts and updates, which asks Postgres for every current column — not just the ones the calling struct knows about — so an old pod's scan of that row can fail once the new column exists.

As of this release, `cmd/newsletter-api/service/implementations.go` sets `bun.WithDiscardUnknownColumns()` on the shared `bun.DB`, so a pod built with this flag silently ignores a `Returning("*")` column its struct doesn't map instead of erroring the scan. This covers future **additive, nullable-or-defaulted** column changes (the shape `publication_id`/`publication_id_set`/`editor_type` used) across an ordinary rolling deploy. It does not cover a future `NOT NULL` column with no default, or a rename/drop — those still need a two-release expand/contract migration regardless of this flag. It also trades away a piece of drift detection service-wide: a mistyped or stale `bun:"..."` struct tag on any `Returning("*")` scan used to fail loudly and now silently leaves that field at its zero value instead — `internal/repository`'s own integration tests deliberately build their `bun.DB` without this flag specifically to keep catching that case in CI.

**Residual risk, documented rather than engineered away:** this flag cannot protect a pod that predates it. That means (a) during this release's own rollout, a pod still on the prior binary can fail a `Returning("*")` scan after a new pod has migrated the schema — for a non-scheduled send specifically, that failure happens after `MarkSending` has already committed `status='sending'`, so the stuck-send sweep can later settle it to `sent` with no recipient dispatched to (see the "Unarmed sends" repair in `docs/newsletter-service-contract.md`'s Crash recovery section, which covers this same row shape); and (b) a rollback of this release to the prior binary after the migration has run reopens the same failure mode indefinitely, not just for one rollout window, since every pod would then predate both the new columns and the flag. Operators cutting or rolling back this release should be aware of both; minimizing old/new pod coexistence during the rollout (and treating a rollback of this release as roll-forward-preferred) reduces exposure but this doc does not prescribe a specific deployment-tooling procedure for it.

## Gateway And Heimdall

`httproute.yaml` routes (regex path matches, so only the newsletter sub-paths of `/projects/...` route here):

- `^/projects/[^/]+/newsletters(/.*)?$`
- `^/projects/[^/]+/newsletter-publications(/.*)?$` (a distinct match — the `-` in `newsletter-publications` breaks the literal `newsletters` in the rule above)
- `^/projects/[^/]+/newsletter-opt-outs(/.*)?$`
- `^/projects/[^/]+/newsletter-opens/[^/]+$`
- `^/committees/[^/]+/newsletters$` (member-facing committee-scoped list)
- `/newsletters/unsubscribe` (exact)
- `/newsletters/sendgrid/events` (exact) — only when `app.send.sendgrid.webhookPublicKeySecretRef.name` is set

When `heimdall.enabled=true`, the HTTPRoute attaches `heimdall-forward-body`.

`ruleset.yaml` authentication and authorization:

- Most authenticated project routes (newsletter CRUD, analytics, etc.) authenticate via OIDC and fall back to `allow_all` when `openfga.enabled=false`. When OpenFGA is enabled, these routes enforce `viewer` (read) or `writer` (write) roles.
- The newsletter-publication routes (`POST`/`GET`/`PUT …/newsletter-publications[/{publication_uid}]`, LFXV2-2582) use the same `project:{project_uid}` object and the `allow_all` fallback when `openfga.enabled=false`, but the read relation differs from the newsletter routes above: create/update require `writer`, and **list/get require `auditor`, not `viewer`**. On an active project every user, including anonymous, holds `viewer`, so a `viewer` read would expose a publication's configuration — its wrapper and branding, block template, editor type, and the From address its editions inherit — to anyone. The same posture is already used by the per-recipient analytics rule (`newsletters:analytics/recipients`) and the opt-out list rule, both `auditor`. Note the newsletter analytics rule itself is still `viewer`, as are the edition read routes; those carry the same exposure and are deliberately out of scope here so they can move with the relation grants they need. Consumers therefore need `auditor` on the project to list or open a publication. The newsletter read routes still use `viewer` and carry the same exposure; moving them is deliberately out of scope here so it can be rolled out with the relation grants it needs. Editions of a publication are read via the flat newsletter list filtered by `?publication_id=` (covered by the newsletter list rule), so there is no dedicated editions route.
- The scheduled-send routes (`POST …/newsletters/{newsletter_uid}/schedule` and `POST …/cancel-schedule`, LFXV2-2685) are copied verbatim from `newsletters:send` — same `writer` relation, same `allow_all` fallback. Rationale: arming and cancelling a send carry exactly the authority `send` already carries — same actor, same blast radius, no new data exposure — so they deliberately do **not** follow the fail-closed pattern below.
- The opt-out list endpoint (`GET /projects/{project_uid}/newsletter-opt-outs`) returns PII (email addresses) and is **always fail-closed**: it uses direct `openfga_check` with the `auditor` role and does NOT have an `allow_all` fallback. This route is unreachable when `openfga.enabled=false` or OpenFGA is misconfigured — that is intentional for PII security.
- The opt-out delete endpoint (`DELETE /projects/{project_uid}/newsletter-opt-outs/{opt_out_id}`) mutates a user's consent record and is **always fail-closed**: it uses direct `openfga_check` with the `writer` role and does NOT have an `allow_all` fallback, matching the security posture of the list endpoint.
- The per-recipient analytics endpoint (`GET /projects/{project_uid}/newsletters/{newsletter_uid}/analytics/recipients`) returns PII (recipient email addresses and per-recipient engagement history) and is **always fail-closed**: it uses direct `openfga_check` with the `auditor` role — not `viewer`, whose project relation includes the public wildcard `[user:*]` — and does NOT have an `allow_all` fallback. This route is unreachable when `openfga.enabled=false` — that is intentional for PII security.
- The committee-scoped list (`GET /committees/{committee_uid}/newsletters`) is **always fail-closed**: it uses `openfga_or_check` with `member` OR `auditor` on `committee:{committee_uid}` (auditor folds in committee writers and project oversight roles) and does NOT have an `allow_all` fallback. The FGA check is the route's entire authorization boundary, so it is unreachable when `openfga.enabled=false` — that is intentional.
- The open pixel (`…/newsletter-opens/{newsletter_uid}`) and `/newsletters/unsubscribe` are intentionally unauthenticated because email clients request them without a user session (the unsubscribe link is authorized by its HMAC token).
- The SendGrid event webhook (`POST /newsletters/sendgrid/events`, rendered only when the webhook key ref is set) is `allow_all` at the gateway because SendGrid posts events without a session; authenticity is the ECDSA signature plus a freshness window, verified in-app.

`openfga.enabled=false` is the default. When false, most routes still work via `allow_all`, but the opt-out endpoints, the committee-scoped newsletter list, and the per-recipient analytics endpoint become unreachable. Enable OpenFGA to access them.

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
