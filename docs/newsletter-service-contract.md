<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Newsletter Service Contract

This document is the authoritative contract for the HTTP API and persisted newsletter behavior owned by `lfx-v2-newsletter-service`.

Update this document in the same PR as any change to `pkg/api/newsletter.go`, route registration, status codes, ETag behavior, state transitions, or analytics/open-tracking behavior.

## Ownership

`lfx-v2-newsletter-service` owns:

- Newsletter draft persistence in Postgres.
- Draft-to-sent state transition.
- Recipient count and recipient preview through query-service consumption.
- Newsletter list and analytics reads.
- Local open tracking through `/newsletter-opens/{id}`.
- The public Go DTOs in `pkg/api/newsletter.go`.

The service does not currently dispatch email. Today, the caller supplies the `groupId` created for the `lfx-v2-email-service` send fan-out, and this service persists it when a draft is marked sent.

## Routes

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| `GET` | `/livez` | no | Liveness probe. |
| `GET` | `/readyz` | no | Readiness probe. |
| `POST` | `/newsletters/drafts` | yes | Create a draft. |
| `GET` | `/newsletters/drafts` | yes | List drafts for a context. |
| `GET` | `/newsletters/drafts/{id}` | yes | Get one draft and return its ETag. |
| `PUT` | `/newsletters/drafts/{id}` | yes | Update a draft. Requires `If-Match`. |
| `DELETE` | `/newsletters/drafts/{id}` | yes | Delete a draft. |
| `POST` | `/newsletters/drafts/{id}/send` | yes | Mark a draft sent and persist `groupId`. Requires `If-Match`. |
| `POST` | `/newsletters/recipient-count` | yes | Resolve committees and return unique recipient count. |
| `POST` | `/newsletters/recipients` | yes | Resolve committees and return unique recipient preview. |
| `POST` | `/newsletters/test-send` | yes | Validate test-send input. Does not dispatch email. |
| `GET` | `/newsletters` | yes | List drafts and sent newsletters for a context. |
| `GET` | `/newsletter-analytics/{id}` | yes | Return analytics for one newsletter. |
| `GET` | `/newsletter-opens/{id}` | no | Tracking pixel. Records open by recipient hash and returns a GIF. |

Auth routes expect a Heimdall-issued JWT. `REQUIRE_USER_AUTH=false` is only for local development.

## DTOs

Public DTOs live in `pkg/api/newsletter.go`. Field names intentionally use the JSON casing consumed by Self Serve.

Core state:

| Field | Description |
| --- | --- |
| `id` | Newsletter UUID. |
| `contextType` | `foundation` or `project`. |
| `contextUid` | Owning context UID. |
| `subject` | Subject line. |
| `bodyHtml` | Newsletter HTML body. |
| `edReplyEmail` | Reply-to address. |
| `committeeUids` | Committees used for recipient resolution. |
| `status` | `draft` or `sent`. |
| `sentAt` | Set when status becomes `sent`. |
| `groupId` | `lfx-v2-email-service` correlation ID persisted on send. |
| `createdBy` | Authenticated principal or local fallback. |
| `version` | Optimistic-locking version. |

## Optimistic Locking

`GET /newsletters/drafts/{id}`, `POST /newsletters/drafts`, `PUT /newsletters/drafts/{id}`, and `POST /newsletters/drafts/{id}/send` return a strong ETag formatted as the current integer version.

`PUT /newsletters/drafts/{id}` and `POST /newsletters/drafts/{id}/send` require `If-Match`. Missing or malformed `If-Match` returns `400 invalid_request`. A stale version returns `412 version_mismatch`.

## State Transitions

- New newsletters are created with `status=draft`.
- Drafts can be updated and deleted.
- `POST /newsletters/drafts/{id}/send` validates the draft, resolves recipients, persists `groupId`, sets `status=sent`, sets `sentAt`, stores `totalRecipients`, and increments `version`.
- Sent newsletters cannot be updated, deleted, or sent again.

The database enforces `status='sent' => group_id IS NOT NULL`.

## Recipient APIs

Recipient resolution is documented in `docs/recipient-resolution.md`.

| Endpoint | Behavior |
| --- | --- |
| `/newsletters/recipient-count` | Returns unique recipient count after resolving committee members. |
| `/newsletters/recipients` | Returns unique recipient emails and first names. |
| `/newsletters/test-send` | Validates fields and recipient email, returns `{ "ok": true }`, and does not dispatch email. |

## Analytics And Open Tracking

`/newsletter-opens/{id}?r=<recipient_hash>` is intentionally unauthenticated because email clients do not carry user sessions. The hash is a lowercase hex SHA-256 token. Malformed hashes are ignored and the endpoint still returns the pixel.

`/newsletter-analytics/{id}` aggregates:

- persisted recipient total
- local open rows
- unique open counts by recipient hash
- daily open buckets
- open rate

Delivered and failed counts are currently derived locally as placeholders. Cross-service aggregation from `lfx-v2-email-service` engagement records is a future integration and must update this document when added.

## Error Mapping

| Condition | HTTP | Error code |
| --- | --- | --- |
| Missing or invalid auth | 401 | `unauthorized` |
| Missing record | 404 | `not_found` |
| Stale `If-Match` | 412 | `version_mismatch` |
| Draft already sent | 409 | `already_sent` |
| Invalid request | 400 | `invalid_request` |
| Upstream dependency unavailable | 503 | `service_unavailable` |
| Unexpected server error | 500 | `internal_error` |

5xx responses intentionally use a generic client message. Details are logged server-side.

## Change Checklist

- Update `pkg/api/newsletter.go`.
- Update `internal/handler/http.go` and route handlers.
- Update `internal/service/` and `internal/repository/` when behavior or persistence changes.
- Update `internal/schema/schema.sql` for database changes.
- Update this document.
- Update `docs/recipient-resolution.md` if recipient or email handoff behavior changes.
- Add or update tests.
- Run `make test` and `make check`.
