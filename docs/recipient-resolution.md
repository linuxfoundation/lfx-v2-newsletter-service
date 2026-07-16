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
- Per-recipient sends go to `lfx.email-service.send_email` with bounded concurrency (`SEND_CONCURRENCY`, default 5). Each request carries `to`, subject, HTML/text bodies (with the per-recipient unsubscribe link substituted in), `from` (the deployment default `EMAIL_FROM_ADDRESS`, default `newsletter@lfx.linuxfoundation.org`, or a per-project override selected by project slug via `EMAIL_FROM_ADDRESS_OVERRIDES` — e.g. the `agentic-ai-foundation` project sends from `newsletter@lfx.aaif.io`; the slug is resolved through `lfx.projects-api.get_slug` and any override domain must also be in the email-service allowlist), `from_display_name`, `reply_to`, and `group_id`.
- The fan-out runs in a background job detached from the `/send` request (the endpoint returns `202` once the row transitions to `sending`, bounded by `SEND_JOB_TIMEOUT`), so per-recipient failures are logged rather than returned; the newsletter settles to `sent` only when at least one recipient was delivered to. A fully-failed fan-out reverts the row to `draft` for retry. See `newsletter-service-contract.md` § State Transitions.
- A failed per-recipient dispatch is retried up to 2 additional attempts with a short doubling backoff (500ms, then 1s), inside the same worker so `SEND_CONCURRENCY` still bounds total in-flight requests. Retry-safety boundary (deliberate): only failures that prove the email never went out are retried — an error reply that the email-service contract documents as a pre-acceptance rejection, or a NATS no-responders condition. Ambiguous failures are **not** retried: request timeouts, cancelled contexts, email-service's post-acceptance `email delivery failed` reply, and unrecognized error strings. Email-service has no idempotency key, so retrying a request it may already have accepted risks double-sending; the miss is surfaced by failure accounting instead. A recipient counts as failed only after its final attempt, recording the last error; each retry is logged with the redacted recipient and attempt number.
- Known trade-off of that boundary: it is drawn on retry-*safety*, not retry-*usefulness*. Of the retry-safe failures only no-responders is transient — the pre-acceptance rejections are deterministic and re-fail on every attempt. This is deliberate (a transient-vs-deterministic taxonomy would have to be guessed at, since email-service does not expose one, and guessing is how a retry gate starts double-sending), but it has one operator-visible consequence: the four envelope-level rejections (`invalid from address`, `from address domain not allowed`, `invalid reply_to address`, `reply_to address domain not allowed`) apply identically to every recipient, since `from`/`reply_to` are per-send, not per-recipient. So a misconfigured send — most likely an `EMAIL_FROM_ADDRESS_OVERRIDES` domain missing from the email-service allowlist — takes roughly 3× longer to fail than it otherwise would, and on a large enough list can burn `SEND_JOB_TIMEOUT` rather than failing fast. The outcome is the same either way (zero deliveries, row reverted to `draft`, re-sendable once the config is fixed); only the time-to-failure differs. If email-service ever distinguishes transient from permanent rejections, narrow the allowlist to the transient set.
- Systemic-outage fail-fast: once any recipient exhausts its attempts with a final NATS **no-responders** failure, nothing is subscribed to `lfx.email-service.send_email`, so the fan-out declares an outage — **but only skips recipients while zero recipients have been delivered to**. Skipped recipients are recorded as failed with `skipped: email-service unavailable` (distinct from a dispatch error: we never tried). Dispatches already in flight finish normally, and the outage is scoped to the one fan-out — the next send resolves fresh.
- The `sent == 0` condition on the skip is the load-bearing part, not a refinement. Skipping is only safe while the fan-out can still converge to *total* failure, because that reverts the row to `draft` and leaves a cleanly re-sendable newsletter. Once any recipient has been delivered to, the row will be marked `sent` regardless (see `newsletter-service-contract.md` § State Transitions), so skipping from that point would strand every remaining recipient on a newsletter permanently recorded as sent, with no re-send path — the unrecoverable outcome the fail-fast exists to avoid. No-responders is also only a point-in-time fact: a redeploy ends, and queued recipients may well succeed. So once delivery has started, dispatching always beats skipping.
- Deliberate design: no circuit breaker or half-open probing (a one-shot batch fan-out, and no-responders is a server-side fact rather than a statistical signal, so probing adds machinery without information); and the trip condition — a single post-retry exhaustion — is intentionally not configurable, because with the `sent == 0` gate the worst a false trip can do is push a send that has delivered nothing toward the revert-to-draft state it was already heading for. Note the trip condition means the recipient spent every attempt *and* its final failure was no-responders; it does not mean every attempt was no-responders, which is deliberately weak evidence that only the `sent == 0` gate makes safe.
- `SEND_FANOUT_ENABLED=false` short-circuits dispatch (validate + transition only) for dev/staging shake-out.
- Analytics later aggregates email-service engagement by this `group_id` (`lfx.email-service.get_email_engagement_analytics`).

## Change Checklist

- Read `lfx-v2-committee-service` docs for committee-member payload fields.
- Read `lfx-v2-email-service/docs/email-service-contract.md` for `send_email` payload and engagement analytics behavior.
- Keep `internal/infrastructure/nats/subjects.go` in sync with the owning services' subject constants.
- Update tests around recipient deduplication, invalid email filtering, unsubscribe exclusion, upstream failures, and fan-out error handling.
