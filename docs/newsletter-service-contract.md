<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Newsletter Service Contract

This document is the authoritative contract for the HTTP API and persisted newsletter behavior owned by `lfx-v2-newsletter-service`.

Update this document in the same PR as any change to `pkg/api/newsletter.go`, route registration, status codes, ETag behavior, state transitions, or analytics/open-tracking/unsubscribe behavior.

## Ownership

`lfx-v2-newsletter-service` owns:

- Project-scoped newsletter draft persistence in Postgres.
- Draft-to-sent state transition.
- Email dispatch: the send orchestrator mints the email-service `group_id`, renders email chrome, and fans out per-recipient sends to `lfx-v2-email-service` over NATS.
- Recipient count and recipient preview through committee-service member lookup over NATS.
- Per-recipient HMAC-signed, project-scoped unsubscribe opt-outs.
- Newsletter list and analytics reads (local opens overlaid with email-service engagement totals).
- Local open tracking through the tracking-pixel endpoint.
- The public Go DTOs in `pkg/api/newsletter.go`.

## Routes

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/livez` | no | Liveness probe. |
| `GET` | `/readyz` | no | Readiness probe. |
| `POST` | `/projects/{project_uid}/newsletters` | yes | Create a draft. |
| `GET` | `/projects/{project_uid}/newsletters` | yes | Unified list of drafts and sent newsletters. Supports `status` and `page_token` query params. |
| `GET` | `/projects/{project_uid}/newsletters/{newsletter_uid}` | yes | Get one newsletter and return its ETag. |
| `PUT` | `/projects/{project_uid}/newsletters/{newsletter_uid}` | yes | Update a draft. Requires `If-Match`. |
| `DELETE` | `/projects/{project_uid}/newsletters/{newsletter_uid}` | yes | Delete a draft. |
| `POST` | `/projects/{project_uid}/newsletters/{newsletter_uid}/send` | yes | Accept the send: resolve recipients, transition the draft to `sending`, return `202`; the fan-out and the `sent` transition complete in a detached background job. Requires `If-Match`. |
| `POST` | `/projects/{project_uid}/newsletters/recipient-count` | yes | Resolve committees and return unique recipient count. |
| `POST` | `/projects/{project_uid}/newsletters/recipients` | yes | Resolve committees and return unique recipient preview. |
| `POST` | `/projects/{project_uid}/newsletters/test-send` | yes | Dispatch a single test email (no persistence, no analytics). |
| `GET` | `/projects/{project_uid}/newsletters/{newsletter_uid}/analytics` | yes | Return analytics for one newsletter. |
| `GET` | `/projects/{project_uid}/newsletter-opens/{newsletter_uid}` | no | Tracking pixel. Records open by recipient hash and returns a GIF. |
| `GET` | `/newsletters/unsubscribe` | no | One-click unsubscribe via HMAC-signed `t` token. Returns HTML. A direct-service HEAD is a no-op so link previews don't unsubscribe; the gateway ruleset allows only `GET`, so HEAD is blocked at the gateway. |
| `GET` | `/projects/{project_uid}/newsletter-opt-outs` | yes | List all unsubscribes for the project — `id`, `email`, and `unsubscribed_at`, ordered by `unsubscribed_at` descending. No pagination (opt-out volumes are small). |
| `DELETE` | `/projects/{project_uid}/newsletter-opt-outs/{opt_out_id}` | yes | Delete an opt-out entry. Returns `204 No Content` on success, `400` for a malformed `opt_out_id` UUID, `404` for unknown `opt_out_id` or project mismatch. |

Auth routes expect a Heimdall-issued JWT. `REQUIRE_USER_AUTH=false` is only for local development; startup refuses auth-disabled mode outside local/dev `LFX_ENVIRONMENT` values.

## DTOs

Public DTOs live in `pkg/api/newsletter.go`. Field names are snake_case to match the LFX V2 attribute-naming convention.

Core state:

| Field | Description |
| --- | --- |
| `id` | Newsletter UUID. |
| `project_uid` | Owning project UID (also in the URL path). |
| `subject` | Subject line. |
| `body_html` | Newsletter HTML body. |
| `ed_reply_email` | Reply-to address. |
| `committee_uids` | Committees used for recipient resolution. |
| `status` | `draft`, `sending`, or `sent`. |
| `sent_at` | Set when status becomes `sent`. |
| `total_recipients` | Recipient count snapshot taken at send-acceptance time. |
| `group_id` | `lfx-v2-email-service` correlation ID, minted by this service when a send is accepted. Null on drafts. |
| `created_by` | Authenticated principal or local fallback. |
| `version` | Optimistic-locking version. |

`POST …/send` returns `SendNewsletterResponse`: the newsletter plus `group_id`, `total_recipients`, `sent`, `failed`, and per-recipient `failures`. The send is asynchronous: acceptance returns `202` with the newsletter in `status=sending` and `sent=0`; clients observe the outcome by re-fetching the newsletter (branch on `newsletter.status`, not the HTTP status code). The zero-recipient edge case settles synchronously and returns `200` with `status=sent`.

## Optimistic Locking

`POST /projects/{project_uid}/newsletters`, `GET …/newsletters/{newsletter_uid}`, and `PUT …/newsletters/{newsletter_uid}` return a strong ETag formatted as the current integer version.

`PUT …/newsletters/{newsletter_uid}` and `POST …/newsletters/{newsletter_uid}/send` require `If-Match`. Missing or malformed `If-Match` returns `400 invalid_request`. A stale version returns `412 version_mismatch`.

## State Transitions

- New newsletters are created with `status=draft`.
- Drafts can be updated and deleted.
- `POST …/newsletters/{newsletter_uid}/send` accepts the send synchronously and completes it asynchronously:
  - **Synchronous (inside the request):** validates the draft, resolves recipients (excluding project-scoped unsubscribes), renders the email envelope, mints a `group_id`, and atomically transitions `draft → sending` — persisting `group_id` and `total_recipients` and incrementing `version`. This single optimistically-locked transition is the duplicate-send guard across replicas: a concurrent or repeated send observes the row is no longer a draft and gets `409 send_in_progress`. The endpoint then returns `202`.
  - **Asynchronous (detached background job):** fans out per-recipient sends to email-service, then — when at least one recipient was delivered to — sets `status=sent`, `sent_at`, and increments `version`. A fully-failed fan-out reverts the row to `draft` (clearing `group_id` and `total_recipients`) so the operator can retry. The job is detached from the HTTP request context, so client disconnects and proxy timeouts cannot cancel a partially-dispatched send or orphan the status; its runtime is bounded by `SEND_JOB_TIMEOUT` (default 30m).
  - **Zero-recipient edge case:** if recipient resolution yields an empty set — for example every resolved committee member is filtered out by a project-scoped unsubscribe — the send settles synchronously: the draft is marked `status=sent` with `total_recipients=0` (and `sent=0`, `failed=0`), `group_id` is persisted, and the endpoint returns `200`. No email is dispatched, and the newsletter cannot be sent again.
- Newsletters in `sending` cannot be updated, deleted, or sent again (`409 send_in_progress`). This also closes the race where an autosave landing mid-fan-out bumped the version and stranded a delivered newsletter in `draft`.
- Sent newsletters cannot be updated, deleted, or sent again (`409 already_sent`).
- **Crash recovery:** a pod dying mid-fan-out leaves `status=sending`. A sweep (startup + every 5 minutes) marks `sending` rows older than `STUCK_SEND_TTL` (default 45m, always ≥ `SEND_JOB_TIMEOUT` + 5m) as `sent` — after a crash an unknown number of emails already went out, so re-arming Send would guarantee duplicates, while settling to `sent` at worst under-reports a remainder that analytics (via `group_id`) makes visible. The rare crash-before-first-dispatch case can be repaired manually: `UPDATE newsletters SET status='draft', group_id=NULL, total_recipients=0, version=version+1 WHERE id='…' AND status='sending';` before the TTL elapses.

The list endpoint's `status=sent` filter also matches `sending` rows so an in-flight send stays visible on the Sent tab; `status=sending` is accepted as an explicit filter.

The database enforces `status IN ('draft','sending','sent')`, `status='sent' => group_id IS NOT NULL`, and UUID format on `group_id`.

## Recipient And Send APIs

Recipient resolution and the email-service fan-out are documented in `docs/recipient-resolution.md`.

| Endpoint | Behavior |
| --- | --- |
| `…/newsletters/recipient-count` | Returns unique recipient count after resolving committee members. |
| `…/newsletters/recipients` | Returns unique recipient emails and first names. |
| `…/newsletters/test-send` | Validates fields and dispatches a single test email to `to_email` — no persistence, no analytics, no compliance footer. Returns `{ "ok": true }`. |

The fan-out is gated by `SEND_FANOUT_ENABLED` (default true). When disabled, sends validate and transition state without dispatching email.

## Analytics, Open Tracking, And Unsubscribe

`…/newsletter-opens/{newsletter_uid}?r=<recipient_hash>` is intentionally unauthenticated because email clients do not carry user sessions. The hash is a lowercase hex SHA-256 token. Malformed hashes are ignored and the endpoint still returns the pixel. Repeat opens from the same recipient within the same hour collapse to one row.

`…/newsletters/{newsletter_uid}/analytics` aggregates, gated on project ownership:

- persisted recipient total (snapshot at send time)
- local open rows, unique open counts by recipient hash, daily open buckets, open rate
- email-service engagement totals fetched by `group_id` over NATS (delivered/failed counts; per-event opens via `opened_at_list` when present), overlaid best-effort — a failed email-service call falls back to local-only analytics
- `failed_recipients`: the lowercased, deduplicated email addresses email-service marked failed (synchronous send errors plus async bounce/complaint events), drawn from the per-recipient status records. Always present (empty array when there are no known failures or the per-recipient status fetch fails). Best-effort: because the scalar `failed` count comes from the engagement rollup while the list comes from the per-recipient records, the two may briefly diverge while the group index propagates. No failure reason is exposed (the per-recipient records carry no error string).

`GET /newsletters/unsubscribe?t=<token>` is intentionally unauthenticated; authorization comes from the HMAC-signed token binding `(project_uid, email)`. Invalid tokens return `400` HTML. Successful opt-outs are idempotent and project-scoped. The endpoint always renders HTML, and a HEAD request handled directly by the service is a no-op so mail-client link previews cannot unsubscribe recipients. Note that the gateway ruleset (`charts/lfx-v2-newsletter-service/templates/ruleset.yaml`) allows only `GET` on this path, so HEAD probes are blocked at the gateway and never reach the handler; the handler's HEAD no-op is a defensive fallback for direct-service traffic that bypasses the gateway.

`GET /projects/{project_uid}/newsletter-opt-outs` lists everyone who has unsubscribed for the project — `{ "opt_outs": [{ "id": "...", "email": "...", "unsubscribed_at": "..." }] }`, ordered by `unsubscribed_at` descending. No name is returned; the unsubscribe token never captures one. No pagination in v1. The `id` field is the UUID PK and can be used with the DELETE endpoint to remove an individual opt-out.

## Error Mapping

| Condition | HTTP | Error code |
| --- | --- | --- |
| Missing or invalid auth | 401 | `unauthorized` |
| Missing record | 404 | `not_found` |
| Stale `If-Match` | 412 | `version_mismatch` |
| Draft already sent | 409 | `already_sent` |
| Send fan-out still in flight | 409 | `send_in_progress` |
| Invalid request | 400 | `invalid_request` |
| Upstream conflict (typed `pkgerrors.Conflict`) | 409 | `conflict` |
| Upstream dependency unavailable | 503 | `service_unavailable` |
| Unexpected server error | 500 | `internal_error` |

Domain sentinels match first; typed `pkgerrors.*` wrappers from the NATS upstream clients (committee, project, email-dispatcher) match by `errors.As`. 5xx responses intentionally use a generic client message. Details are logged server-side.

## Change Checklist

- Update `pkg/api/newsletter.go`.
- Update `internal/handler/http.go` and route handlers.
- Update `internal/service/` and `internal/repository/` when behavior or persistence changes.
- Update `internal/schema/schema.sql` for database changes.
- Update this document.
- Update `docs/recipient-resolution.md` if recipient resolution or email fan-out behavior changes.
- Add or update tests.
- Run `make test` and `make check`.
