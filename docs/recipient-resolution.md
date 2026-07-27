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

- `SendOrchestrator.SendNewsletter` mints the `group_id` (`uuid.NewString()`), renders the body (HTML and text), and resolves the From display name from the sender's auth-service profile (`lfx.auth-service.user_metadata.read`), falling back to `<project name> Newsletter` via `lfx.projects-api.get_name`.
- Reply-To is resolved the same way, from a sibling auth-service subject: `resolveSenderEmail` looks up the sender's primary email via `lfx.auth-service.user_emails.read` (keyed by the same JWT principal) so replies reach whoever actually sends, not whoever last drafted. Resolution is non-fatal — an empty principal, a resolver error, an unparseable address, or a domain outside `EMAIL_REPLY_TO_ALLOWED_DOMAINS` (default `linuxfoundation.org`, mirroring email-service's `SMTP_ALLOWED_REPLY_TO_DOMAINS`) all fall back to the draft's stored `ed_reply_email` rather than blocking the send. The resolved Reply-To is bound into the compliance footer's reply row on both the layout and legacy paths, and set as the email envelope `reply_to`.
- **Body render branches on layout vs legacy.** A layout-based newsletter (`body_layout` present) is **re-rendered from its persisted `body_layout` at send time** — using the send-time unsubscribe config, so the compliance footer always reflects the current deployment setting rather than the write-time snapshot — and the resulting complete emitter email (wrapper plus blocks, MJML-compiled) is dispatched **without** re-wrapping it in the `email_chrome` envelope (re-wrapping would nest a complete document inside chrome and double the header/footer). If the re-render fails, the send **always refuses** (returns an error, reverting the row to `draft` for retry) rather than fall back to the persisted `body_html`, because that persisted body may carry a stale compliance footer or sentinel from a config that no longer matches the send-time unsubscribe setting. A legacy (`body_html`-only) newsletter takes the chrome path unchanged, including the compliance footer. The text body is derived from the layout HTML via `StripHTMLForText` on the layout path.
- Per-recipient sends go to `lfx.email-service.send_email` with bounded concurrency (`SEND_CONCURRENCY`, default 5). Each request carries `to`, subject, HTML/text bodies (with per-recipient runtime placeholders substituted in — see below), `from` (the deployment default `EMAIL_FROM_ADDRESS`, default `newsletter@lfx.linuxfoundation.org`, or a per-project override selected by project slug via `EMAIL_FROM_ADDRESS_OVERRIDES` — e.g. the `agentic-ai-foundation` project sends from `newsletter@lfx.aaif.io`; the slug is resolved through `lfx.projects-api.get_slug` and any override domain must also be in the email-service allowlist), `from_display_name`, `reply_to`, and `group_id`.
- **Per-recipient placeholder substitution.** Both paths run the rendered body (HTML and text) through `substitutePlaceholders` before each send. `%%UNSUBSCRIBE_URL%%` resolves to the recipient's signed unsubscribe link (HTML-escaped for its href context). `%%MANAGE_SUBSCRIPTIONS_URL%%` resolves to EMPTY, never the unsubscribe URL — that URL performs a one-click opt-out on GET, so aliasing it under a preferences label would silently unsubscribe recipients; render-on-write also binds the field empty so current layout emails do not carry the row at all, and the substitution is a defensive backstop for bodies rendered earlier. The **View Online** row is likewise omitted at render-on-write. When the unsubscribe service is unconfigured, the opt-out row is not omitted — it renders the reply-based fallback copy ("To unsubscribe, reply with UNSUBSCRIBE.") instead of a link. Send-scoped sentinels (`%%SENDER_NAME%%`, `%%PROJECT_NAME%%`) are substituted once per send, HTML-escaped, for the wrapper's compliance footer. The layout-only sentinels are no-ops on the legacy path.
- The fan-out runs in a background job detached from the `/send` request (the endpoint returns `202` once the row transitions to `sending`, bounded by `SEND_JOB_TIMEOUT`), so per-recipient failures are logged rather than returned; the newsletter settles to `sent` only when at least one recipient was delivered to. A fully-failed fan-out reverts the row to `draft` for retry. See `newsletter-service-contract.md` § State Transitions.
- `SEND_FANOUT_ENABLED=false` short-circuits dispatch (validate + transition only) for dev/staging shake-out.
- Analytics later aggregates email-service engagement by this `group_id` (`lfx.email-service.get_email_engagement_analytics`).

## Change Checklist

- Read `lfx-v2-committee-service` docs for committee-member payload fields.
- Read `lfx-v2-email-service/docs/email-service-contract.md` for `send_email` payload and engagement analytics behavior.
- Keep `internal/infrastructure/nats/subjects.go` in sync with the owning services' subject constants.
- Update tests around recipient deduplication, invalid email filtering, unsubscribe exclusion, upstream failures, and fan-out error handling.
