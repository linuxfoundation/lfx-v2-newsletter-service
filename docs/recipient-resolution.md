<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Recipient Resolution

This document owns the newsletter service's current recipient-resolution behavior and its handoff to the email service.

Update it in the same PR as any change to committee lookup, query-service parameters, bearer-token propagation, recipient deduplication, or email-service integration.

## Current Flow

1. Authenticated caller invokes a newsletter endpoint that needs recipients.
2. `withAuth` validates the Heimdall-issued JWT and stores the bearer token on the request context.
3. `SendOrchestrator` asks `CommitteeClient` for each committee UID.
4. `CommitteeQueryClient` calls query-service `GET /query/resources`.
5. Recipient records are merged, lowercased, deduplicated by email, and returned to the caller or used to mark the draft sent.

The service does not currently publish email messages. `SendDraft` persists the `groupId` supplied by the caller after that caller has coordinated the `lfx-v2-email-service` send fan-out.

## Query-Service Consumption

Owner: `lfx-v2-query-service`

Canonical contract: `lfx-v2-query-service/docs/query-service-contract.md`

Committee-member indexed data owner: `lfx-v2-committee-service/docs/indexer-contract.md`

Current query intent:

| Query parameter | Value |
| --- | --- |
| `v` | `1` |
| `type` | `committee_member` |
| `tags` | `committee_uid:<committee_uid>` |
| `page_size` | `100` |
| `page_token` | Opaque token from the prior response, when present. |

Before changing response parsing or pagination, verify the current query-service response envelope in `lfx-v2-query-service/docs/query-service-contract.md` and generated OpenAPI/Goa design. Do not infer it from stale central skill prose.

## Bearer Token Propagation

The service validates inbound JWTs before forwarding a bearer token to query-service.

- If auth is required and the JWT is missing or invalid, the request fails before recipient resolution.
- If local auth-disabled mode is active and token validation fails, the service does not forward the invalid token.
- Upstream query-service remains responsible for access-filtering committee member results.

Do not log bearer tokens, Authorization headers, or raw upstream response bodies.

## Recipient Normalization

`SendOrchestrator.resolveRecipients`:

- Resolves each committee concurrently with an `errgroup`.
- Cancels in-flight lookups if one lookup fails.
- Lowercases and trims email addresses.
- Drops empty emails and values without `@`.
- Deduplicates by normalized email.
- Preserves trimmed first names when available.

This service does not own committee member schema. Committee member index data is owned by `lfx-v2-committee-service`, then queried through `lfx-v2-query-service`.

## Email-Service Handoff

Owner: `lfx-v2-email-service`

Canonical contract: `lfx-v2-email-service/docs/email-service-contract.md`

Current behavior:

- The caller is responsible for sending each rendered email to `lfx.email-service.send_email`.
- The caller supplies or receives an email-service `groupId`.
- The caller passes that `groupId` to `POST /newsletters/drafts/{id}/send`.
- Newsletter service validates that `groupId` is a UUID and persists it on the newsletter row.

Future behavior may move email publication into this service. If that happens, update:

- `docs/newsletter-service-contract.md`
- this document
- `pkg/api/newsletter.go`
- `internal/service/send_orchestrator.go`
- chart values for any new NATS connection
- tests for send fan-out, error handling, and idempotency

## Change Checklist

- Read `lfx-v2-query-service/docs/query-service-contract.md`.
- Read `lfx-v2-committee-service/docs/indexer-contract.md` for committee-member data fields and tags.
- Read `lfx-v2-email-service/docs/email-service-contract.md` for `groupId` and email send behavior.
- Update tests around recipient deduplication, invalid email filtering, upstream failures, and bearer-token propagation.
