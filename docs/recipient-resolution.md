<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Recipient Resolution

This document owns how the newsletter service finds a newsletter's recipients, and how it hands them to the email provider.

Update it in the same PR as any change to committee member lookup, the audience snapshot, recipient deduplication, unsubscribe exclusion, or the send fan-out.

## Overview

The service reads committee members from the LFX V2 query service over HTTPS. It does this in a background job, one job per newsletter. The job writes an audience snapshot to the database. The send, the recipient count and the recipient preview read that snapshot. For one release they also accept clients that have not adopted snapshots, and read committees live for them (see Clients that have not adopted snapshots below).

The design was agreed at architecture office hours on 29 October 2026. The record is in `lfx-architecture-scratch`, folder `2026-10-Newsletter-Committee-Audience-Query-Service`.

The reason for the design is size. The old NATS call returned every member of a committee in one reply, and NATS limits a reply to about 1 MiB. That limit is reached between 1,500 and 1,750 members, and a test committee of 17,064 members already failed on dev. The query service returns members in pages of up to 1,000, so size is no longer limited by one reply. Reading a large committee still takes many requests, so the read runs in a background job and not inside a request from the gateway.

## Audience Snapshot

Each newsletter has at most one audience. It has its own status, which is separate from the newsletter status. The newsletter `status` column and its values are not changed by this feature.

| Audience status | Meaning |
| --- | --- |
| `processing` | A job is reading the committees. Only one job runs per newsletter. |
| `ready` | The snapshot is complete and matches the committee list stored with it. |
| `failed` | The job failed, or the job did not finish before `AUDIENCE_STALE_TTL`. The caller can start it again. |

The snapshot is stored in two tables.

- `newsletter_audiences` has one row per newsletter. It holds the status, the sorted committee UID list the snapshot was built from, the recipient count, the number of members skipped because they had no email, an error message for a failed audience, and timestamps.
- `newsletter_audience_members` has one row per recipient, with the email (trimmed and lowercased), the first name and the last name.

Both tables are deleted with the newsletter. Once a newsletter is sent or scheduled, its `newsletter_audience_members` rows are deleted, so member emails and names are kept only while the newsletter is a draft or sending. The send deletes them inline, and the recovery sweep (every 5 minutes) deletes any it missed, e.g. for a scheduled send settled by the sweep. A resolution that finishes after the newsletter left draft writes no members. The `newsletter_audiences` row and its counts stay. Per-recipient analytics then read display names from the committees live. Newsletters sent before this feature have no snapshot, and no snapshot is created for them.

### Rules

- A `ready` audience always matches the committee list it was built from. The send, the recipient count and the recipient preview compare that list with the newsletter's current committee list. If the two differ, the audience is treated as stale and must be resolved again.
- Only one resolution runs per newsletter. `BeginResolve` is a single SQL statement that claims the row unless it is already `processing`, so this holds across pods. A second request while one is running gets the running audience back.
- A failed read fails the whole audience. A `ready` audience never holds part of a committee.
- A send never fans out from an audience that is `processing`, `failed` or stale. A `ready` audience whose members were already deleted counts as stale, so a newsletter that reverted to draft is never sent to nobody.
- `POST .../recipients` with a `newsletter_uid` returns `409 already_sent` once the newsletter is no longer a draft, because its members are gone.
- A committee is checked against the newsletter's own project on every read, snapshot or live, because the service reads as its own M2M identity and can see every project. No member may be indexed under another project, including a child project: that fails with "a selected committee belongs to another project". A committee with members must also have at least one member indexed under the project. A member's project is copied from its committee, so one such member proves the committee's project, and members with no project in the index are accepted alongside it. A committee where no member has a project fails with "a selected committee could not be matched to this project".
- Only the committees saved on the draft can be resolved. A request with any other list is a `400`, so a caller cannot make the service read and store an arbitrary committee.
- A committee that returns no members fails the audience with "a selected committee returned no members". The query service answers an unknown committee, or one this service cannot see, with an empty page, so an empty result is far more likely a missing access grant than a real empty committee.
- A `ready` audience is returned unchanged until the committee list changes. `POST .../audience` with `"refresh": true` rebuilds it from the committees' current membership.
- Only a draft can be resolved. A newsletter that is sending, scheduled or sent keeps the audience it was sent to.
- Unsubscribes are applied twice. The job drops addresses that have unsubscribed when it builds the snapshot. The send applies the current unsubscribe list again, so an address that unsubscribed after the snapshot is not sent to. The count shown to the user comes from the snapshot, so `total_recipients` on the sent newsletter can be lower than that count.

## Flow

1. The caller sends `POST /projects/{project_uid}/newsletters/{newsletter_uid}/audience` with the committee UIDs. `withAuth` validates the Heimdall JWT. The bearer token is not attached to the context and is never forwarded.
2. `AudienceService.Resolve` validates the project, validates the committee list (trimmed, deduplicated and sorted), checks that the newsletter belongs to the project and is a draft, and calls `BeginResolve`. If an audience is already `ready` for the same committees, it is returned without a new job.
3. When the claim succeeds, the job starts on a context that does not depend on the request. It is bounded by `AUDIENCE_JOB_TIMEOUT`. The response is `202` with the `processing` audience.
4. The job reads every committee at the same time (`errgroup.WithContext`, so the first failure cancels the others). Each committee is read page by page from the query service.
5. The job normalizes the members (see Recipient Normalization), replaces the stored members in one transaction, and marks the audience `ready` with the count. On any error it marks the audience `failed` with a short message. The full error is only in the log.
6. The caller polls `GET .../audience` until the status is `ready` or `failed`. It returns `404` if the audience was never resolved.
7. `POST .../recipient-count` and `POST .../recipients` read the snapshot. They return `409` with `audience_status` when it is not ready. `POST .../send` returns the same `409` and does not send.

A pod that shuts down cancels its running jobs, and each one marks its audience `failed` so the user can resolve it again straight away. A pod can also stop without a clean shutdown. For that case the recovery sweep, which runs every 5 minutes, marks any `processing` audience that has not changed for `AUDIENCE_STALE_TTL` as `failed`. A running job is protected from the sweep by the TTL margin: `AUDIENCE_STALE_TTL` must be at least `AUDIENCE_JOB_TIMEOUT` plus 5 minutes. The service raises a smaller value to that minimum.

### The 409 response

`audience_status` has one of these values.

| Value | Meaning |
| --- | --- |
| `none` | No audience has been resolved for the newsletter. |
| `processing` | A job is running. |
| `failed` | The last resolution failed. |
| `stale` | A `ready` audience exists, but the committee list has changed since it was built. |

In every case the client starts a resolution with `POST .../audience`, and then waits for `ready`.

### Clients that have not adopted snapshots

The snapshot routes and the old request shapes work side by side for one release, so the service can deploy before the front end.

- `POST .../recipient-count` and `POST .../recipients` without a `newsletter_uid` read the committees live, less the unsubscribe list, as they did before snapshots. An empty committee counts as 0 here, as before. A committee that cannot be matched to the project is refused with `422`, as on the snapshot path, because these reads take committee UIDs straight from the request body.
- `POST .../send` for a newsletter with no audience at all reads the committees live and sends to them, as before. An empty committee refuses the send with `422`, so the newsletter is not used up. Once a client has started an audience for a newsletter, a `processing`, `failed` or stale audience still stops the send with `409`.

Remove these paths once the front end resolves an audience for every send.

## Query Service Consumption

Owner: `lfx-v2-query-service`. The client is in `internal/infrastructure/queryservice`.

The request for one page is:

```
GET {QUERY_SERVICE_URL}/query/resources?v=1&type=committee_member&tags=committee_uid:<uid>&page_size=<n>&page_token=<token>
Authorization: Bearer <service token>
```

- `v=1` is required. `page_size` is `QUERY_SERVICE_PAGE_SIZE` (default 1000, the largest value the query service accepts).
- The reply is `{"resources": [{"type", "id", "data"}], "page_token": "..."}`. There is no total and no `has_more` field. `page_token` is present only when the page before access filtering was full. A page can be shorter than the page size after access filtering and still not be the last one, so the client keeps going until `page_token` is absent and never stops on a short page.
- The client reads only `data.email`, `data.first_name` and `data.last_name`. All members are read, whatever their status.
- Email is read through the single function `memberEmail`. Email is expected to move to its own indexed entity in November 2026. When it does, that function is the one place that changes.
- Members with no email are skipped and counted. The count is stored as `skipped_no_email` on the audience.
- The paging loop is bounded by `QUERY_SERVICE_MAX_PAGES` (default 100, which is 100,000 members at the default page size). A committee that needs more pages fails with an error. The list is never cut short.
- Each page has its own timeout, `QUERY_SERVICE_TIMEOUT` (default 15s). The query service bounds a whole request at 30s.
- A page is retried up to 3 times, with a wait that starts at 500ms and doubles, for a network error, a `429` or a `5xx`. A `Retry-After` header is used when present, up to 30s. A `401` discards the cached token and retries once. A `404` is reported as "committee not found". A `403` is reported as an error that names the committee but not the token. Other statuses fail without a retry.
- Response bodies are read through a limit of 8 MiB.
- The client refuses plain `http` unless the host is a loopback address, and it refuses redirects. Both rules keep the token from being sent somewhere it should not go.

### Identity

The service calls the query service as itself. `internal/infrastructure/m2m` gets an Auth0 access token with the client credentials grant and a private key JWT client assertion, signed with `M2M_AUTH_PRIVATE_KEY` by the `go-auth0` library. The token is cached and counts as expired 60 seconds before its real expiry. The private key is parsed when the first token is requested, so a malformed key shows up on first use and not at startup.

The identity needs read access to every committee member. In the FGA model this is a direct `auditor` tuple on the root project, or membership of the global project auditors team. Each query is checked against `committee:<uid>#viewer` for the client ID as the principal, and one check covers a whole page. If a valid token returns empty pages, check the principal string in that tuple before looking at the code.

Startup rules:

- `QUERY_SERVICE_URL`, `M2M_AUTH_DOMAIN`, `M2M_AUTH_AUDIENCE`, `M2M_AUTH_CLIENT_ID` and `M2M_AUTH_PRIVATE_KEY` are all required. The service does not start if one is missing, because there is no other way to resolve an audience.
- The startup log records only whether each value is set. It never records the key, the client assertion or the token.

The earlier HTTP client forwarded the inbound user's token, which the query service could not validate, so it was removed. The inbound token is still never forwarded.

## Recipient Normalization

`buildAudienceMembers` in `internal/service/audience_members.go` is a pure function. The job calls it after it has read every committee. It does the following:

- Trims and lowercases each email.
- Drops empty emails and values without `@`, and counts them with the members the query service had no email for.
- Deduplicates by normalized email. The first occurrence wins.
- Drops addresses in `newsletter_unsubscribes` for the project.
- Keeps the trimmed first and last name.

`dropUnsubscribed` applies the current unsubscribe list to the stored members at send time.

Do not log recipient emails, tokens or the Authorization header.

## Email-Service Fan-Out

Owner: `lfx-v2-email-service`

Canonical contract: `lfx-v2-email-service/docs/email-service-contract.md`

Current behavior — this service owns the email dispatch behind an `EmailDispatcher` abstraction selected by `EMAIL_PROVIDER`. The bullets below describe the default `email-service` path (fan-out to `lfx-v2-email-service` over NATS → SES). When `EMAIL_PROVIDER=sendgrid` the same orchestration and envelope apply, but the per-recipient send and the engagement read go direct to SendGrid over HTTPS instead (the From domain must be in `SENDGRID_AUTHENTICATED_DOMAINS`, and Reply-To is enforced against `EMAIL_REPLY_TO_ALLOWED_DOMAINS` in the provider rather than by email-service). See the SendGrid provider (`internal/infrastructure/sendgrid`).

- `SendOrchestrator.SendNewsletter` reads the ready audience snapshot (see Audience Snapshot), applies the current unsubscribe list, mints the `group_id` (`uuid.NewString()`), renders the email chrome (HTML and text), and resolves the From display name from the sender's auth-service profile (`lfx.auth-service.user_metadata.read`), falling back to `<project name> Newsletter` via `lfx.projects-api.get_name`. The compliance footer rendered into every real-send body includes a recipient-independent "My Newsletters" deep link (`LFX_SELF_SERVE_BASE_URL` + `/newsletters/my`, default `https://app.lfx.dev/newsletters/my`) above the unsubscribe small print; test-sends render the same compliance footer, with the unsubscribe link minted directly for the request's `to_email` (a real working opt-out link) and the same "My Newsletters" link. Above the card, the chrome renders the send date (`America/Los_Angeles`, or the `scheduled_at` date for scheduled sends) and a "Read online" link to the Self-Serve reader (`LFX_SELF_SERVE_BASE_URL` + `/newsletters/<project slug>/<newsletter_uid>`). The slug is resolved through `lfx.projects-api.get_slug`; if that lookup fails, the link is left out and the send continues. Share links to X, LinkedIn, Reddit and email follow "Read online" and are left out with it. Test-sends show only the date. This describes a newsletter that has no `body_layout`. A newsletter with a `body_layout` takes the layout path described below.
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

**Scheduled sends (LFXV2-2685) are the identical fan-out with two extra fields.** `POST …/schedule` reuses `SendOrchestrator.SendNewsletter` wholesale: the only differences are a `send_at` (UNIX seconds, derived from `scheduled_at`) and `batch_id` (minted via the provider's `CreateBatchID` before the `draft → sending` transition) added to every per-recipient request in the fan-out, identical across every recipient in one send. This is SendGrid-only — scheduling type-asserts the active `EmailDispatcher` against an optional `port.ScheduledSender` interface, and a provider that doesn't implement it (email-service) fails the schedule request with `503 service_unavailable` rather than silently sending immediately. The audience snapshot, per-recipient unsubscribe links, and the open-tracking pixel are unchanged. Cancelling (`POST …/cancel-schedule`) calls the provider's `CancelScheduledBatch` with the same `batch_id` before reverting the row to `draft`.

## Provider-Routed Analytics

A newsletter's engagement lives in whichever provider dispatched it, so analytics resolves the engagement source per newsletter rather than from a single active provider.

- Every send stamps `newsletters.send_provider` at the `sending` transition (`MarkSending`), set from the active `EMAIL_PROVIDER` (`email-service` or `sendgrid`). The column defaults to `email-service`, and a migration backfills every pre-existing row to `email-service`, since all sends predating SendGrid went via email-service (SES).
- `AnalyticsService` holds one `EngagementReader` per provider and resolves the reader from `send_provider` (`email-service` reads engagement from email-service over NATS; `sendgrid` reads from the local SendGrid engagement store). An empty or unrecognized value falls back to the default provider (`email-service`).
- Both readers are always registered. The SendGrid reader is store-backed and needs no API key, so a deployment serves analytics for SendGrid-sent newsletters even when email-service is the currently-active sender, and vice versa. A missing reader degrades that newsletter to local-only analytics rather than failing the request — except on the per-recipient endpoint, which has no local fallback and returns an error instead (an empty list must always mean "no records").
- Net effect: historical SES newsletters keep their analytics and SendGrid sends surface analytics through the same `/analytics` endpoint and DTO after a per-foundation flip, with no UI change.
- The per-recipient endpoint (`…/analytics/recipients`) reads the same provider-routed source via `EngagementReader.RecipientRecords` (email-service's by-group status reply over NATS, or the SendGrid store's engagement rows joined with their open events). Display names are matched on lowercased email. They come from the newsletter's audience snapshot while it still holds members. A sent newsletter's members are deleted after the send, so for sent newsletters (and those sent before snapshots existed) the names are read live from the query service, for all its committees in parallel under a 10-second limit. Name resolution is best-effort in both paths. A failed lookup degrades to email-only rows.
- **Clicks are SendGrid-only.** The SendGrid store's `EngagementReader` additionally reports click totals, a daily click series, per-recipient click detail, and a top-clicked-links breakdown, all sourced from the `click` event on the same signed event webhook that feeds opens (`sendgrid_click_events`, joined to `sendgrid_recipient_engagement` by `email_id`). The email-service reader's `GroupDetailFromRecords` builder leaves every click field zero/empty — SES has no click-event delivery path into this service — so an `email-service`-provider newsletter always reports zero clicks rather than erroring. The compliance-footer's Unsubscribe and My Newsletters links are rendered with `clicktracking="off"` so opt-out/browse clicks never inflate `click_rate` or `top_links`.

## Change Checklist

- Read the `lfx-v2-query-service` design (`design/query-svc.go`) and `lfx-v2-committee-service/docs/indexer-contract.md` for the `committee_member` resource fields.
- Read `lfx-v2-email-service/docs/email-service-contract.md` for `send_email` payload and engagement analytics behavior.
- Keep `internal/infrastructure/nats/subjects.go` in sync with the owning services' subject constants.
- Update tests around recipient deduplication, invalid email filtering, unsubscribe exclusion, the audience states in the 409 response, query service paging and failures, and fan-out error handling.
