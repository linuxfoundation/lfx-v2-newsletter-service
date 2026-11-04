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
| `httproute.yaml` | Gateway API routing for the project-scoped newsletter paths (this includes the audience routes), the open pixel, `/newsletters/unsubscribe`, and (when the webhook key is configured) the SendGrid event webhook. |
| `ruleset.yaml` | Heimdall rules for authenticated API routes plus the unauthenticated open pixel, unsubscribe, and (when the webhook key is configured) SendGrid-webhook endpoints. |
| `heimdall-middleware.yaml` | Optional Traefik middleware resources when this chart owns them locally. |
| `networkpolicy.yaml` | Optional egress controls for DNS, NATS, OTel, Postgres, and external HTTPS (e.g. JWKS). |
| `service.yaml` | ClusterIP service. |
| `serviceaccount.yaml` | ServiceAccount. |

## Runtime Values

| Value | Env var | Notes |
| --- | --- | --- |
| `service.port` | `PORT` | HTTP listen port. |
| `app.logLevel` | `LOG_LEVEL` | `debug`, `info`, `warn`, or `error`. |
| `app.nats.url` | `NATS_URL` | Single NATS connection used by the email dispatcher, the project metadata client and the user metadata client. Committee members are not read over NATS. |
| `app.nats.timeout` / `maxReconnect` / `reconnectWait` | `NATS_TIMEOUT` / `NATS_MAX_RECONNECT` / `NATS_RECONNECT_WAIT` | Empty falls back to app defaults (10s / unlimited / 2s). |
| `app.queryService.url` | `QUERY_SERVICE_URL` | Base URL of the LFX V2 query service, e.g. `https://lfx-api.dev.v2.cluster.linuxfound.info/`. Required. The service does not start without it, because committee members are only read from the query service. |
| `app.queryService.pageSize` | `QUERY_SERVICE_PAGE_SIZE` | Page size for committee member reads. Allowed range 1 to 1000. Default 1000, the largest value the query service accepts. |
| `app.queryService.maxPages` | `QUERY_SERVICE_MAX_PAGES` | Most pages read for one committee. Default 100, which is 100,000 members at the default page size. A committee that needs more fails its audience. |
| `app.queryService.timeout` | `QUERY_SERVICE_TIMEOUT` | Timeout for one page request. Default 15s. |
| `app.m2mAuth.domain` | `M2M_AUTH_DOMAIN` | Auth0 tenant domain, e.g. `linuxfoundation-dev.auth0.com`. Required. |
| `app.m2mAuth.audience` | `M2M_AUTH_AUDIENCE` | Audience of the access token. On dev it is the same value as `app.queryService.url`. Required. |
| `app.m2mAuth.clientIDSecretRef` | `M2M_AUTH_CLIENT_ID` | Sources the Auth0 client ID from a Kubernetes Secret. The default key is `auth0_client_id`. The secret name is required, and the chart fails to render without it. |
| `app.m2mAuth.privateKeySecretRef` | `M2M_AUTH_PRIVATE_KEY` | Sources the PEM private key of the Auth0 client from a Kubernetes Secret. The default key is `auth0_client_private_key`. The secret name is required, and the chart fails to render without it. |
| `app.audienceResolution.jobTimeout` | `AUDIENCE_JOB_TIMEOUT` | Bounds one background audience resolution job. Default 30m. |
| `app.audienceResolution.staleTTL` | `AUDIENCE_STALE_TTL` | Age after which an audience stuck in `processing` is marked `failed` by the recovery sweep. Default 40m. The app raises a smaller value to `AUDIENCE_JOB_TIMEOUT` plus 5m. |
| `app.send.fanoutEnabled` | `SEND_FANOUT_ENABLED` | Toggles real email dispatch; false validates the send and reads the audience snapshot without sending. |
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
| `app.send.sendgrid.apiKeySecretRef` | `SENDGRID_API_KEY` | Sources the SendGrid API key from a Kubernetes Secret (typically an ExternalSecret target declared in `lfx-v2-argocd`). Required for real sends. |
| `app.send.sendgrid.webhookPublicKeySecretRef` | `SENDGRID_WEBHOOK_PUBLIC_KEY` | Sources the ECDSA signed-event-webhook key from a Secret. Setting this ref (independent of `provider`) wires the webhook env, HTTPRoute path, and RuleSet rule, so ingestion survives a flip back to email-service. Required when set — configure it only once the key exists; empty renders no webhook wiring. |
| `app.selfServeBaseURL` | `LFX_SELF_SERVE_BASE_URL` | Base URL of the LFX Self-Serve app used to build the compliance footer's "My Newsletters" deep link (`<base>/newsletters/my`) and the "Read online" reader permalink above the card (`<base>/newsletters/<project slug>/<newsletter_uid>`). Empty falls back to the app default (`https://app.lfx.dev`); set per environment in deployment values (e.g. `https://app.staging.lfx.dev`). |
| `app.unsubscribe.publicBaseURL` | `NEWSLETTER_PUBLIC_BASE_URL` | Externally-reachable origin used to build unsubscribe links. Defaults to `https://lfx-api.<lfx.domain>`. Required when fan-out is enabled. |
| `app.unsubscribe.secret` / `secretRef` | `NEWSLETTER_UNSUBSCRIBE_SECRET` | HMAC key signing unsubscribe tokens. Required when fan-out is enabled; prefer `secretRef`. |
| `app.requireUserAuth` | `REQUIRE_USER_AUTH` | Disable only for local development. |
| `app.jwksURL` | `JWKS_URL` | Required when auth is enabled. |
| `app.audience` | `JWT_AUDIENCE` | Must match Heimdall `create_jwt` audience. |
| `database.*` | `DATABASE_URL` or `PG*` | See database modes below. |
| `app.extraEnv` | varies | Escape hatch for additional env vars. |
| `app.otel.*` | `OTEL_*` | OpenTelemetry configuration. |

## Query Service Identity

The service reads committee members from the query service as itself. It needs an Auth0 client that uses a private key JWT client assertion, and read access to every committee member in OpenFGA.

- The client is created in `auth0-terraform`. The client ID and the PEM private key are stored in `lfx-secrets-management`, and `lfx-v2-argocd` syncs them into the `lfx-v2-newsletter-service-secrets` secret in each environment under the keys `auth0_client_id` and `auth0_client_private_key`.
- The chart takes the client ID and the key only as secret references. It never takes the values. `helm template` fails if `app.m2mAuth.clientIDSecretRef.name`, `app.m2mAuth.privateKeySecretRef.name`, `app.queryService.url`, `app.m2mAuth.domain` or `app.m2mAuth.audience` is empty.
- The grant is a direct `auditor` tuple on the root project for the client, or membership of the global project auditors team. If a valid token returns empty pages, check the principal string in that tuple first.
- The network policy already allows egress on ports 80 and 443, which covers the query service and the Auth0 tenant.
- The `app.nats.*` values stay, because NATS is still used for email, project and user metadata lookups.

The values are described in `docs/recipient-resolution.md`.

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
- `/newsletters/sendgrid/events` (exact) — only when `app.send.sendgrid.webhookPublicKeySecretRef.name` is set

When `heimdall.enabled=true`, the HTTPRoute attaches `heimdall-forward-body`.

`ruleset.yaml` authentication and authorization:

- Most authenticated project routes (newsletter CRUD, analytics, etc.) authenticate via OIDC and fall back to `allow_all` when `openfga.enabled=false`. When OpenFGA is enabled, these routes enforce `viewer` (read) or `writer_guard` (write) on the project.
- The stateless `…/newsletters/render-preview` route carries a project-scoped `writer_guard` rule (mirroring `…/test-send`, a write-scoped authoring action), so it is FGA-gated and not JWT-only.
- The editor template routes (`…/newsletters/templates` and `…/newsletters/templates/{template_key}/manifest`) carry explicit project-scoped `viewer` rules. The list path would otherwise match the `{newsletter_uid}` GET rule only by coincidence, and the manifest path matches no other rule at all, so both are declared rather than inherited. Both rules are declared before the `{newsletter_uid}` GET rule because Heimdall uses the first matching rule.
- The scheduled-send routes (`POST …/newsletters/{newsletter_uid}/schedule` and `POST …/cancel-schedule`, LFXV2-2685) are copied verbatim from `newsletters:send` — same `writer_guard` relation, same `allow_all` fallback. Rationale: arming and cancelling a send carry exactly the authority `send` already carries — same actor, same blast radius, no new data exposure — so they deliberately do **not** follow the fail-closed pattern below.
- The opt-out list endpoint (`GET /projects/{project_uid}/newsletter-opt-outs`) returns PII (email addresses) and is **always fail-closed**: it uses direct `openfga_check` with the `auditor_guard` role and does NOT have an `allow_all` fallback. This route is unreachable when `openfga.enabled=false` or OpenFGA is misconfigured — that is intentional for PII security.
- The audience snapshot routes (`…/newsletters/{newsletter_uid}/audience`) are split by method. `POST` starts or restarts the background recipient resolution, which reads committee members with the service's M2M identity, so it carries the same `writer_guard` rule as `newsletters:send`. `GET` returns only the snapshot status, counts and committee UIDs, no email addresses, so it uses project `viewer` like `recipient-count`. Both fall back to `allow_all` when `openfga.enabled=false`.
- The opt-out delete endpoint (`DELETE /projects/{project_uid}/newsletter-opt-outs/{opt_out_id}`) mutates a user's consent record and is **always fail-closed**: it uses direct `openfga_check` with the `writer_guard` role and does NOT have an `allow_all` fallback, matching the security posture of the list endpoint.
- The per-recipient analytics endpoint (`GET /projects/{project_uid}/newsletters/{newsletter_uid}/analytics/recipients`) returns PII (recipient email addresses and per-recipient engagement history) and is **always fail-closed**: it uses direct `openfga_check` with the `auditor_guard` role — not `viewer`, whose project relation includes the public wildcard `[user:*]` — and does NOT have an `allow_all` fallback. This route is unreachable when `openfga.enabled=false` — that is intentional for PII security.
- The committee-scoped list (`GET /committees/{committee_uid}/newsletters`) is **always fail-closed**: it uses `openfga_or_check` with `member` OR `auditor` on `committee:{committee_uid}` (auditor folds in committee writers and project oversight roles) and does NOT have an `allow_all` fallback. The FGA check is the route's entire authorization boundary, so it is unreachable when `openfga.enabled=false` — that is intentional.
- The share-recording endpoint (`POST …/newsletters/{newsletter_uid}/shares`) is a write gated on project `viewer`, not `writer_guard`: any reader of a sent newsletter may record that they shared it. It authenticates via OIDC only — no `anonymous_authenticator` — because `viewer` includes `[user:*]` and the shared anonymous subject would otherwise pass. It falls back to `allow_all` when `openfga.enabled=false`, like the other project routes. Blast radius is bounded in-app: one share per (user, channel), sent newsletters only, and the anonymous principal is rejected.
- The page-view endpoint (`POST …/newsletters/{newsletter_uid}/page-views`) has the same posture as shares: project `viewer`, OIDC only (no `anonymous_authenticator`), `allow_all` fallback when `openfga.enabled=false`. Blast radius is one view per (newsletter, user), sent newsletters only.
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
