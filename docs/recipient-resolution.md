<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Recipient Resolution

This document owns the newsletter service's current recipient-resolution behavior and its handoff to the email service.

Update it in the same PR as any change to committee member lookup, NATS subjects or payloads, recipient deduplication, unsubscribe exclusion, or the email-service fan-out.

## Current Flow

1. Authenticated caller invokes a project-scoped newsletter endpoint that needs recipients.
2. `withAuth` validates the Heimdall-issued JWT and stores the validated principal on the request context. The bearer token itself is never attached to context or forwarded — outbound calls don't need it.
3. `SendOrchestrator.resolveRecipients` asks `CommitteeClient.ListMembers` for each committee UID concurrently (`errgroup.WithContext`, so the first failure cancels in-flight siblings).
4. Each lookup is a NATS request to `lfx.committee-api.list_members` (committee UID as raw bytes; JSON member array back).
5. Recipient records are merged, lowercased, deduplicated by email, filtered against the project's unsubscribe list, and returned to the caller or fanned out.

> History: an earlier revision called query-service over HTTP (`GET /query/resources`) and forwarded the inbound bearer token. That path was retired — Heimdall mints a JWT the query-service can't validate as OIDC, so FGA filtered the response empty in production. `internal/infrastructure/upstream/` remains only as an empty placeholder package.

## Committee-Service Consumption

Owner: `lfx-v2-committee-service`

Subject constants live in `internal/infrastructure/nats/subjects.go`; the client in `internal/infrastructure/nats/committee_client.go`.

| Subject | Request | Response |
| --- | --- | --- |
| `lfx.committee-api.list_members` | committee UID (raw bytes) | JSON array of committee member records (`email`, `first_name`, …) |

No token travels on the wire; trust is enforced upstream of NATS (the same model committee-service uses for project metadata). Committee-member schema is owned by `lfx-v2-committee-service` — verify payload fields there before changing response parsing.

## Recipient Normalization

`SendOrchestrator.resolveRecipients`:

- Resolves each committee concurrently with an `errgroup`; the first failure cancels in-flight lookups.
- Lowercases and trims email addresses.
- Drops empty emails and values without `@`.
- Deduplicates by normalized email.
- Excludes addresses in `newsletter_unsubscribes` for the project (and logs the excluded count).
- Preserves trimmed first names when available.

Do not log raw recipient lists or bearer tokens.

## Email-Service Fan-Out

Owner: `lfx-v2-email-service`

Canonical contract: `lfx-v2-email-service/docs/email-service-contract.md`

Current behavior — this service owns the email dispatch:

- `SendOrchestrator.SendNewsletter` mints the `group_id` (`uuid.NewString()`), renders the email chrome (HTML and text), and resolves the From display name from the sender's auth-service profile (`lfx.auth-service.user_metadata.read`), falling back to `<project name> Newsletter` via `lfx.projects-api.get_name`.
- Per-recipient sends go to `lfx.email-service.send_email` with bounded concurrency (`SEND_CONCURRENCY`, default 5). Each request carries `to`, subject, HTML/text bodies (with the per-recipient unsubscribe link substituted in), `from` (`EMAIL_FROM_ADDRESS`, default `newsletter@lfx.linuxfoundation.org` — domain must be in the email-service allowlist), `from_display_name`, `reply_to`, and `group_id`.
- Per-recipient failures are collected and surfaced in the `/send` response; the draft is marked sent only when at least one recipient was delivered to. A fully-failed fan-out leaves the row a draft for retry.
- `SEND_FANOUT_ENABLED=false` short-circuits dispatch (validate + transition only) for dev/staging shake-out.
- Analytics later aggregates email-service engagement by this `group_id` (`lfx.email-service.get_email_engagement_analytics`).

## Change Checklist

- Read `lfx-v2-committee-service` docs for committee-member payload fields.
- Read `lfx-v2-email-service/docs/email-service-contract.md` for `send_email` payload and engagement analytics behavior.
- Keep `internal/infrastructure/nats/subjects.go` in sync with the owning services' subject constants.
- Update tests around recipient deduplication, invalid email filtering, unsubscribe exclusion, upstream failures, and fan-out error handling.
