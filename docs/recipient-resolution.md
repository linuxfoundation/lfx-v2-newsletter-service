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

Current behavior — this service owns the email dispatch behind an `EmailDispatcher` abstraction selected by `EMAIL_PROVIDER`. The bullets below describe the default `email-service` path (fan-out to `lfx-v2-email-service` over NATS → SES). When `EMAIL_PROVIDER=sendgrid` the same orchestration and envelope apply, but the per-recipient send and the engagement read go direct to SendGrid over HTTPS instead (the From domain must be in `SENDGRID_AUTHENTICATED_DOMAINS`, and Reply-To is enforced against `EMAIL_REPLY_TO_ALLOWED_DOMAINS` in the provider rather than by email-service). See the SendGrid provider (`internal/infrastructure/sendgrid`).

- `SendOrchestrator.SendNewsletter` mints the `group_id` (`uuid.NewString()`), renders the email chrome (HTML and text), and resolves the From display name from the sender's auth-service profile (`lfx.auth-service.user_metadata.read`), falling back to `<project name> Newsletter` via `lfx.projects-api.get_name`. The compliance footer rendered into every real-send body includes a recipient-independent "My Newsletters" deep link (`LFX_SELF_SERVE_BASE_URL` + `/newsletters/my`, default `https://app.lfx.dev/newsletters/my`) above the unsubscribe small print; test-sends render the same compliance footer, with the unsubscribe link minted directly for the request's `to_email` (a real working opt-out link) and the same "My Newsletters" link. Above the card, the chrome renders the send date (`America/Los_Angeles`, or the `scheduled_at` date for scheduled sends) and a "Read online" link to the Self-Serve reader (`LFX_SELF_SERVE_BASE_URL` + `/newsletters/<project slug>/<newsletter_uid>`). The slug is resolved through `lfx.projects-api.get_slug`; if that lookup fails, the link is left out and the send continues. Test-sends show only the date. This describes a newsletter that has no `body_layout`. A newsletter with a `body_layout` takes the layout path described below.
- Reply-To is resolved the same way, from a sibling auth-service subject: `resolveSenderEmail` looks up the sender's primary email via `lfx.auth-service.user_emails.read` (keyed by the same JWT principal) so replies reach whoever actually sends, not whoever last drafted. Resolution is non-fatal — an empty principal, a resolver error, an unparseable address, or a domain outside `EMAIL_REPLY_TO_ALLOWED_DOMAINS` (default `linuxfoundation.org`, mirroring email-service's `SMTP_ALLOWED_REPLY_TO_DOMAINS`) all fall back to the draft's stored `ed_reply_email` rather than blocking the send. The resolved Reply-To is set as the email envelope `reply_to` on both the layout and the legacy path.
- Per-recipient sends go to `lfx.email-service.send_email` with bounded concurrency (`SEND_CONCURRENCY`, default 5). Each request carries `to`, subject, HTML/text bodies (with the per-recipient unsubscribe link substituted in), `from` (the deployment default `EMAIL_FROM_ADDRESS`, default `newsletter@lfx.linuxfoundation.org`, or a per-project override selected by project slug via `EMAIL_FROM_ADDRESS_OVERRIDES` — e.g. the `agentic-ai-foundation` project sends from `newsletter@lfx.aaif.io`; the slug is resolved through `lfx.projects-api.get_slug` and any override domain must also be in the email-service allowlist), `from_display_name`, `reply_to`, and `group_id`.
- The fan-out runs in a background job detached from the `/send` request (the endpoint returns `202` once the row transitions to `sending`, bounded by `SEND_JOB_TIMEOUT`), so per-recipient failures are logged rather than returned; the newsletter settles to `sent` only when at least one recipient was delivered to. A fully-failed fan-out reverts the row to `draft` for retry. See `newsletter-service-contract.md` § State Transitions.
- `SEND_FANOUT_ENABLED=false` short-circuits dispatch (validate + transition only) for dev/staging shake-out.
- Analytics later aggregates engagement by this `group_id` from the provider that sent the newsletter (email-service via `lfx.email-service.get_email_engagement_analytics`, or the local SendGrid store) — see § Provider-Routed Analytics.

### Layout newsletters

A newsletter with a `body_layout` is re-rendered from that layout every time it is sent. This section describes that path.

- The send path renders `body_layout` again with the settings in effect at send time. It does not dispatch the `body_html` that was saved when the draft was written, because that body may carry an unsubscribe row that no longer matches the deployment. If the render fails, the send is refused and the draft stays in `draft` so it can be retried: `422 unprocessable_entity` when the layout itself no longer renders or its output exceeds the size cap, `500` when the stored layout cannot be parsed.
- The rendered email is the complete document. The send path does not wrap it in the email chrome, so there is one header and one footer.
- The wrapper has its own date and "View Online" link. The send path binds `edition.date` to the same formatted send date the legacy chrome shows (`America/Los_Angeles`, or the `scheduled_at` date for scheduled sends). It binds `edition.view_online_link` to the same "Read online" URL the legacy chrome uses. Both values come from the single per-send project slug lookup. If the slug lookup fails or `LFX_SELF_SERVE_BASE_URL` is empty, the wrapper leaves the View Online link out and the send continues.
- A test send with a `body_layout` renders the layout with today's date, no View Online link, and no unsubscribe row. The wrapper's compliance footer still renders. A test send never mints an unsubscribe token for an arbitrary address.
- The embedded wrappers show the opt-out row (the unsubscribe link plus the manage-subscriptions link) only when an unsubscribe link exists. They have no reply-based fallback text. For this reason a real send of a layout newsletter is refused with `503 service_unavailable` when the unsubscribe service is not configured. Nothing is dispatched and the draft stays in `draft`.
- A draft with no content to send is refused with `400 invalid_request` before the `draft` to `sending` change. This covers a layout with an empty `blocks` array and a legacy draft whose `body_html` is empty.
- The send path substitutes three sentinels for each recipient. `%%UNSUBSCRIBE_URL%%` becomes the recipient's signed unsubscribe link. `%%MANAGE_SUBSCRIPTIONS_URL%%` becomes the "My Newsletters" archive link, or empty when no base URL is set. `%%VIEW_ONLINE_URL%%` becomes empty. The send path substitutes `%%SENDER_NAME%%`, `%%PROJECT_NAME%%` and `%%SEND_DATE%%` once for the whole send. The wrappers copied from the template repositories use the unsubscribe and manage sentinels, and bind the footer's sender line to `edition.sender_name` and `edition.project_name`; render-on-write binds those to the sender and project sentinels, which the send path replaces once per send with the resolved sender display name and the project name.
- Writers cannot put any of these sentinels in block content. The service rejects the request with `400 invalid_request`, at any nesting depth.
- The legacy path substitutes only `%%UNSUBSCRIBE_URL%%`. A literal `%%MANAGE_SUBSCRIPTIONS_URL%%` in a legacy `body_html` is left as written.

**Scheduled sends (LFXV2-2685) are the identical fan-out with two extra fields.** `POST …/schedule` reuses `SendOrchestrator.SendNewsletter` wholesale: the only differences are a `send_at` (UNIX seconds, derived from `scheduled_at`) and `batch_id` (minted via the provider's `CreateBatchID` before the `draft → sending` transition) added to every per-recipient request in the fan-out, identical across every recipient in one send. This is SendGrid-only — scheduling type-asserts the active `EmailDispatcher` against an optional `port.ScheduledSender` interface, and a provider that doesn't implement it (email-service) fails the schedule request with `503 service_unavailable` rather than silently sending immediately. Recipient resolution, per-recipient unsubscribe links, and the open-tracking pixel are unchanged. Cancelling (`POST …/cancel-schedule`) calls the provider's `CancelScheduledBatch` with the same `batch_id` before reverting the row to `draft`.

## Provider-Routed Analytics

A newsletter's engagement lives in whichever provider dispatched it, so analytics resolves the engagement source per newsletter rather than from a single active provider.

- Every send stamps `newsletters.send_provider` at the `sending` transition (`MarkSending`), set from the active `EMAIL_PROVIDER` (`email-service` or `sendgrid`). The column defaults to `email-service`, and a migration backfills every pre-existing row to `email-service`, since all sends predating SendGrid went via email-service (SES).
- `AnalyticsService` holds one `EngagementReader` per provider and resolves the reader from `send_provider` (`email-service` reads engagement from email-service over NATS; `sendgrid` reads from the local SendGrid engagement store). An empty or unrecognized value falls back to the default provider (`email-service`).
- Both readers are always registered. The SendGrid reader is store-backed and needs no API key, so a deployment serves analytics for SendGrid-sent newsletters even when email-service is the currently-active sender, and vice versa. A missing reader degrades that newsletter to local-only analytics rather than failing the request — except on the per-recipient endpoint, which has no local fallback and returns an error instead (an empty list must always mean "no records").
- Net effect: historical SES newsletters keep their analytics and SendGrid sends surface analytics through the same `/analytics` endpoint and DTO after a per-foundation flip, with no UI change.
- The per-recipient endpoint (`…/analytics/recipients`) reads the same provider-routed source via `EngagementReader.RecipientRecords` (email-service's by-group status reply over NATS, or the SendGrid store's engagement rows joined with their open events). Display names are resolved at read time by re-querying the newsletter's committees over `lfx.committee-api.list_members` and matching members on lowercased email — a read-only lookup with no unsubscribe filtering (the engagement rows already reflect who was actually sent to). Name resolution is best-effort: a failed committee lookup degrades to email-only rows.
- **Clicks are SendGrid-only.** The SendGrid store's `EngagementReader` additionally reports click totals, a daily click series, per-recipient click detail, and a top-clicked-links breakdown, all sourced from the `click` event on the same signed event webhook that feeds opens (`sendgrid_click_events`, joined to `sendgrid_recipient_engagement` by `email_id`). The email-service reader's `GroupDetailFromRecords` builder leaves every click field zero/empty — SES has no click-event delivery path into this service — so an `email-service`-provider newsletter always reports zero clicks rather than erroring. The compliance-footer's Unsubscribe and My Newsletters links are rendered with `clicktracking="off"` so opt-out/browse clicks never inflate `click_rate` or `top_links`.

## Change Checklist

- Read `lfx-v2-committee-service` docs for committee-member payload fields.
- Read `lfx-v2-email-service/docs/email-service-contract.md` for `send_email` payload and engagement analytics behavior.
- Keep `internal/infrastructure/nats/subjects.go` in sync with the owning services' subject constants.
- Update tests around recipient deduplication, invalid email filtering, unsubscribe exclusion, upstream failures, and fan-out error handling.
