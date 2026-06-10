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
| `POST` | `/projects/{project_uid}/newsletters/{newsletter_uid}/send` | yes | Resolve recipients, fan out per-recipient sends, mark the draft sent. Requires `If-Match`. |
| `POST` | `/projects/{project_uid}/newsletters/recipient-count` | yes | Resolve committees and return unique recipient count. |
| `POST` | `/projects/{project_uid}/newsletters/recipients` | yes | Resolve committees and return unique recipient preview. |
| `POST` | `/projects/{project_uid}/newsletters/test-send` | yes | Dispatch a single test email (no persistence, no analytics). |
| `GET` | `/projects/{project_uid}/newsletters/{newsletter_uid}/analytics` | yes | Return analytics for one newsletter. |
| `GET` | `/projects/{project_uid}/newsletter-opens/{newsletter_uid}` | no | Tracking pixel. Records open by recipient hash and returns a GIF. |
| `GET` | `/newsletters/unsubscribe` | no | One-click unsubscribe via HMAC-signed `t` token. Returns HTML. HEAD is a no-op so link previews don't unsubscribe. |

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
| `status` | `draft` or `sent`. |
| `sent_at` | Set when status becomes `sent`. |
| `total_recipients` | Recipient count snapshot taken at send time. |
| `group_id` | `lfx-v2-email-service` correlation ID, minted by this service on send. Null on drafts. |
| `created_by` | Authenticated principal or local fallback. |
| `version` | Optimistic-locking version. |

`POST …/send` returns `SendNewsletterResponse`: the updated newsletter plus `group_id`, `total_recipients`, `sent`, `failed`, and per-recipient `failures`.

## Optimistic Locking

`POST /projects/{project_uid}/newsletters`, `GET …/newsletters/{newsletter_uid}`, and `PUT …/newsletters/{newsletter_uid}` return a strong ETag formatted as the current integer version.

`PUT …/newsletters/{newsletter_uid}` and `POST …/newsletters/{newsletter_uid}/send` require `If-Match`. Missing or malformed `If-Match` returns `400 invalid_request`. A stale version returns `412 version_mismatch`.

## State Transitions

- New newsletters are created with `status=draft`.
- Drafts can be updated and deleted.
- `POST …/newsletters/{newsletter_uid}/send` validates the draft, resolves recipients (excluding project-scoped unsubscribes), mints a `group_id`, fans out per-recipient sends to email-service, and — only when at least one recipient was delivered to — sets `status=sent`, `sent_at`, `total_recipients`, persists `group_id`, and increments `version`. A fully-failed fan-out leaves the row a draft so the operator can retry.
- Sent newsletters cannot be updated, deleted, or sent again.

The database enforces `status='sent' => group_id IS NOT NULL` and UUID format on `group_id`.

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

`GET /newsletters/unsubscribe?t=<token>` is intentionally unauthenticated; authorization comes from the HMAC-signed token binding `(project_uid, email)`. Invalid tokens return `400` HTML. Successful opt-outs are idempotent and project-scoped. The endpoint always renders HTML, and HEAD requests are a no-op so mail-client link previews cannot unsubscribe recipients.

## Error Mapping

| Condition | HTTP | Error code |
| --- | --- | --- |
| Missing or invalid auth | 401 | `unauthorized` |
| Missing record | 404 | `not_found` |
| Stale `If-Match` | 412 | `version_mismatch` |
| Draft already sent | 409 | `already_sent` |
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
