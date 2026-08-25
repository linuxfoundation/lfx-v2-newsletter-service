<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Newsletter Service Contract

This document is the authoritative contract for the HTTP API and persisted newsletter behavior owned by `lfx-v2-newsletter-service`.

Update this document in the same PR as any change to `pkg/api/newsletter.go`, route registration, status codes, ETag behavior, state transitions, or analytics/open-tracking/unsubscribe behavior.

## Ownership

`lfx-v2-newsletter-service` owns:

- Project-scoped newsletter draft persistence in Postgres.
- Draft-to-sent state transition.
- Email dispatch: the send orchestrator mints the `group_id`, renders email chrome, and fans out per-recipient sends through the active provider (selected by `EMAIL_PROVIDER`): `lfx-v2-email-service` over NATS by default, or SendGrid directly when selected.
- Recipient count and recipient preview through committee-service member lookup over NATS.
- Per-recipient HMAC-signed, project-scoped unsubscribe opt-outs.
- Newsletter list and analytics reads (local opens overlaid with the sending provider's engagement totals, routed by `send_provider`).
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
| `POST` | `/projects/{project_uid}/newsletters/{newsletter_uid}/send` | yes | Accept the send: resolve recipients, transition the draft to `sending`, return `202`; the fan-out and the `sent` transition complete in a detached background job. Requires `If-Match`. Behaviorally unchanged by scheduling: a draft carrying a saved `scheduled_at` still sends immediately, ignoring it. |
| `POST` | `/projects/{project_uid}/newsletters/{newsletter_uid}/schedule` | yes | Arm a scheduled send via the active provider's native scheduling (SendGrid only — LFXV2-2685). Optional body `{ "scheduled_at": "..." }` overrides the draft's saved value; when neither exists, `400`. Otherwise behaves like `send`: `202` with the draft in `sending`, settling in the background to `scheduled` once at least one recipient's message is accepted by the provider for release at `scheduled_at`, or if any scheduling outcome is ambiguous. Requires `If-Match`. `503` when the active provider doesn't support scheduling (`EMAIL_PROVIDER=email-service`). |
| `POST` | `/projects/{project_uid}/newsletters/{newsletter_uid}/cancel-schedule` | yes | Cancel an armed schedule and revert the newsletter to `draft`, retaining `scheduled_at` as the author's saved intent while clearing `group_id`/`batch_id`. Rejected within the configured cancel buffer of `scheduled_at` (`409 cancel_window_closed`) since provider cancellation is best-effort near release. Requires `If-Match`. |
| `POST` | `/projects/{project_uid}/newsletters/recipient-count` | yes | Resolve committees and return unique recipient count. |
| `POST` | `/projects/{project_uid}/newsletters/recipients` | yes | Resolve committees and return unique recipient preview. |
| `POST` | `/projects/{project_uid}/newsletters/test-send` | yes | Dispatch a single test email (no persistence, no analytics). |
| `POST` | `/projects/{project_uid}/newsletters/render-preview` | yes | Render a `NewsletterLayout` to email-safe HTML for the editor's live preview. Stateless — nothing is persisted. Returns `422 unprocessable_entity` when the layout cannot be rendered. |
| `GET` | `/projects/{project_uid}/newsletters/templates` | yes | List the embedded editor template sets (`{"templates": [{"key", "label"}]}`). The catalog is compiled into the binary and identical for every project. |
| `GET` | `/projects/{project_uid}/newsletters/templates/{template_key}/manifest` | yes | Return the editor manifest for one template set: the palette of block types (`block_type`, `label`, `category`, `schema`, optional `is_container`, comment-stripped `template`) plus the page-chrome `wrapper` and `wrapper_key`. Unknown keys return `404 not_found`. Mirrors `NewsletterTemplateManifest` in `@lfx-one/shared`. |
| `GET` | `/projects/{project_uid}/newsletters/{newsletter_uid}/analytics` | yes | Return analytics for one newsletter. |
| `GET` | `/projects/{project_uid}/newsletters/{newsletter_uid}/analytics/recipients` | yes | Per-recipient engagement: who the newsletter went to, delivery outcome, and every recorded open timestamp, with a best-effort display name from the newsletter's committees. |
| `GET` | `/committees/{committee_uid}/newsletters` | yes | Member-facing: sent newsletters whose audience includes the committee, ordered `sent_at` descending. Supports `page_token`. The gateway gates on `committee:{committee_uid}` `member` OR `auditor` (openfga_or_check), checked per request against OpenFGA — auditor folds in committee writers and project oversight roles so stronger roles never see less than members. Member tuples are maintained by committee-service via fga-sync, so revocation after a membership removal is eventual — normally near-immediate, but tuple removal is asynchronous/best-effort (see committee-service's `docs/fga-contract.md`); this endpoint adds no caching on top of tuple state. Returns `CommitteeNewsletterListResponse` — a reduced DTO without `body_html`, `ed_reply_email`, `group_id`, `created_by`, or `committee_uids` (the full audience list would let a single-committee member enumerate the newsletter's other committees). Clients fetch the rendered body via the project-scoped get, which is reachable for members because the platform model's project `viewer` relation includes all authenticated users (`[user:*]`). |
| `GET` | `/projects/{project_uid}/newsletter-opens/{newsletter_uid}` | no | Tracking pixel. Records open by recipient hash and returns a GIF. |
| `GET` | `/newsletters/unsubscribe` | no | Verifies the HMAC-signed `t` token and renders an HTML confirmation page with a `POST` form back to this same path. Read-only: it never records an opt-out, even on a repeated fetch (a mail client's link-preview scanner may fetch the URL more than once). A direct-service HEAD is a no-op for the same reason; the gateway ruleset allows `GET` and `POST` but not HEAD, so HEAD is blocked at the gateway. |
| `POST` | `/newsletters/unsubscribe` | no | Verifies the same HMAC-signed `t` token and records the opt-out. Reached either from the GET confirmation page's form, or directly from a mail client's RFC 8058 one-click unsubscribe action. Returns HTML. |
| `POST` | `/newsletters/sendgrid/events` | no | SendGrid signed event webhook (delivered / open / click / bounce / dropped / spamreport / blocked). Authenticity is the ECDSA signature over `timestamp + body` plus a 10-minute freshness window against replay; no user session. Registered whenever `SENDGRID_WEBHOOK_PUBLIC_KEY` is set — independent of `EMAIL_PROVIDER`, so events for historical and scheduled SendGrid sends keep flowing after the outbound provider is flipped back to `email-service`; do not remove or block this route on a provider flip. Returns `204`; `400` on a malformed body, `401` on a bad signature or stale timestamp. The chart routes this path (`httproute.yaml` + an anonymous `ruleset.yaml` rule) whenever `app.send.sendgrid.webhookPublicKeySecretRef.name` is set — independent of the outbound provider, so ingestion survives a flip back to `email-service`. |
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
| `body_html` | Newsletter HTML body. For layout-based newsletters this is derived from `body_layout` on write (see below); for legacy newsletters it is the authored HTML. |
| `body_layout` | Optional `NewsletterLayout` — the editor's structured layout. Returned **only by the editable single-resource reads** (get / create / update) and **only** for layout-based newsletters (`omitempty` for legacy / html-only rows). The **list** and **send** responses always omit it (`NewsletterListItem` and the send response DTO clear `body_layout` on the returned copy regardless of type — the persisted newsletter's stored layout is untouched, since it is still needed for later reads and send-time re-rendering), so those consumers must never rely on receiving it — fetch the single resource when the structured layout is needed. |
| `ed_reply_email` | Reply-to address stored on the draft. Fallback only — at send time the orchestrator resolves the sender's own primary email via `lfx.auth-service.user_emails.read` and uses it as Reply-To instead, so replies reach whoever sends rather than whoever last drafted. This field is used only when that resolution fails or the sender's domain isn't in the Reply-To allowlist. |
| `committee_uids` | Committees used for recipient resolution. |
| `status` | `draft`, `sending`, `scheduled`, or `sent`. |
| `sent_at` | Set when status becomes `sent`. |
| `scheduled_at` | Optional, settable on create/update. Two meanings depending on `status`: while `draft`, it is the author's saved intent — saving it does **not** by itself contact the send provider or send anything. Once `status=scheduled`, it is the committed release time armed at the provider. Null when no schedule has ever been set. `PUT` is full-replace: an omitted value clears a previously-saved schedule. |
| `total_recipients` | Recipient count snapshot taken at send-acceptance time. |
| `group_id` | `lfx-v2-email-service` correlation ID, minted by this service when a send is accepted. Null on drafts. |
| `created_by` | Authenticated principal or local fallback. |
| `version` | Optimistic-locking version. |

`POST …/send` returns `SendNewsletterResponse`: the newsletter plus `group_id`, `total_recipients`, `sent`, `failed`, and per-recipient `failures`. The send is asynchronous: acceptance returns `202` with the newsletter in `status=sending` and `sent=0`; clients observe the outcome by re-fetching the newsletter (branch on `newsletter.status`, not the HTTP status code). The zero-recipient edge case settles synchronously and returns `200` with `status=sent`.

### Declarative Layout

`CreateNewsletterRequest` and `UpdateNewsletterRequest` accept an optional `body_layout` (`NewsletterLayout`) alongside `subject`, `body_html`, `ed_reply_email`, and `committee_uids`. When `body_layout` is supplied the service renders it to `body_html` via the declarative emitter and persists both; any `body_html` in the request is ignored.

On update, `body_layout` is tri-state: **absent** preserves a layout newsletter's stored layout and derived `body_html` (the request's `body_html` is ignored for layout rows; html-only rows take `body_html` from the request as before); an **explicit `"body_layout": null`** clears the stored layout and converts the newsletter to html-only, taking `body_html` from the request; an **object** replaces the layout and re-derives `body_html`. On create, absent and `null` are equivalent (html-only).

`NewsletterLayout` is the structured newsletter body:

| Field | Description |
| --- | --- |
| `wrapper_key` | Selects the wrapper template the top-level blocks render inside. |
| `template_key` | Optional (`omitempty`). Selects the embedded block library the layout is rendered with. The block composer is an AAIF-only pilot and there is one embedded library, `aaif-user-community`, which is also the **block superset** (every block type any library offers). Empty falls back to that sole library wholesale (its wrapper plus superset blocks), so a layout saved without an explicit key still resolves every block. The fallback therefore renders AAIF-branded chrome; a keyless layout from a non-AAIF project would inherit AAIF branding, which is acceptable while the pilot is AAIF-only (the composer always sends `template_key="aaif-user-community"`). Naming a library the binary does not embed is a `422`. |
| `blocks` | Ordered top-level `LayoutBlock`s rendered in the wrapper's body slot. |

`LayoutBlock` is a recursive content node:

| Field | Description |
| --- | --- |
| `block_type` | Selects the declarative block template. |
| `content` | Optional map of field bindings the template consumes (`omitempty`). |
| `blocks` | Optional nested child `LayoutBlock`s (`omitempty`). |

`POST …/render-preview` takes a `RenderPreviewRequest` and returns a `RenderPreviewResponse`:

| DTO | Field | Description |
| --- | --- | --- |
| `RenderPreviewRequest` | `body_layout` | The `NewsletterLayout` to render. |
| `RenderPreviewRequest` | `wrapper_content` | Optional map of runtime values the wrapper template binds against (e.g. `edition.date`); may be omitted (`omitempty`). |
| `RenderPreviewResponse` | `body_html` | The rendered, email-safe HTML produced by the declarative emitter. |

`GET …/newsletters/templates` returns `{"templates": [{"key", "label"}]}`. `GET …/templates/{template_key}/manifest` returns the editor manifest for one embedded template set — `wrapper_key`, `blocks` (each with `block_type`, `label`, `category`, `schema`, optional `is_container`, and a comment-stripped `template` body), and the page-chrome `wrapper`. The shape is produced by `internal/service/render/declarative.Manifest` and mirrors `NewsletterTemplateManifest` in `@lfx-one/shared`, which the block composer consumes; the embedded per-key template sets are the single source of truth for both the editor palette and rendering.

`TestSendRequest` (the `…/test-send` body) carries an optional `body_layout` (`NewsletterLayout`, `omitempty`) — the **sole layout trigger** for a test send. When present the service recompiles the layout server-side (declarative emitter) and dispatches that HTML, with only the unsubscribe opt-out row **suppressed** — the wrapper's `if=`-guarded opt-out row is dropped, avoiding a dangling unsubscribe link in a test that mints no real signed token, while the sender/reply/delivery attribution in the compliance footer still renders. When `body_layout` is omitted, `body_html` is treated as simple editor HTML and wrapped in email chrome (the legacy path).

The `is_layout` boolean is **deprecated and ignored**. It is still accepted (the decoder rejects unknown fields, so removing it would break clients that still send it), but it has no effect: a precompiled `is_layout` + `body_html` request is no longer dispatched verbatim, because binding its per-recipient unsubscribe sentinel to empty left a dangling `<a href="">Unsubscribe</a>`. Send `body_layout` for a layout test send.

`POST …/schedule` returns `ScheduleNewsletterResponse`: the same shape as `SendNewsletterResponse` plus `scheduled_at` (the value actually armed — the request's override, or the draft's own saved value). `POST …/cancel-schedule` returns `CancelScheduleResponse`: just the reverted newsletter.

**Save-time vs. arm-time validation.** `scheduled_at` on create/update is validated leniently — only that it is in the future. The full arm-time window (minimum lead, and a hard cap at the send provider's horizon — currently 72 hours for SendGrid Mail Send) is validated only when `POST …/schedule` is called, independently of when the value was saved. An author can save a schedule five days out on a draft; arming it fails with `400 invalid_request` until it falls inside the window.

## Optimistic Locking

`POST /projects/{project_uid}/newsletters`, `GET …/newsletters/{newsletter_uid}`, and `PUT …/newsletters/{newsletter_uid}` return a strong ETag formatted as the current integer version.

`PUT …/newsletters/{newsletter_uid}`, `POST …/newsletters/{newsletter_uid}/send`, `POST …/schedule`, and `POST …/cancel-schedule` require `If-Match`. Missing or malformed `If-Match` returns `400 invalid_request`. A stale version returns `412 version_mismatch`.

## State Transitions

- New newsletters are created with `status=draft`.
- Drafts can be updated and deleted.
- On create and update, layout-based newsletters (request includes `body_layout`) derive the persisted `body_html` from `body_layout` via the declarative emitter; legacy newsletters persist the authored `body_html` as-is.
- `POST …/newsletters/{newsletter_uid}/send` accepts the send synchronously and completes it asynchronously:
  - **Synchronous (inside the request):** validates the draft, resolves recipients (excluding project-scoped unsubscribes), renders the email envelope, mints a `group_id`, and atomically transitions `draft → sending` — persisting `group_id` and `total_recipients` and incrementing `version`. This single optimistically-locked transition is the duplicate-send guard across replicas: a concurrent or repeated send observes the row is no longer a draft and gets `409 send_in_progress`. The endpoint then returns `202`.
  - **Asynchronous (detached background job):** fans out per-recipient sends through the active provider, then — when at least one recipient was delivered to OR any result is ambiguous (provider may have accepted it) — sets `status=sent`, `sent_at`, and increments `version`. A settled status=sent does NOT guarantee all recipients were accepted — only that the send was initiated. A fully-failed fan-out (zero definitive rejections) reverts the row to `draft` (clearing `group_id` and `total_recipients`) so the operator can retry. The job is detached from the HTTP request context, so client disconnects and proxy timeouts cannot cancel a partially-dispatched send or orphan the status; its runtime is bounded by `SEND_JOB_TIMEOUT` (default 30m). Consult the Failures list and provider engagement metrics for per-recipient delivery outcomes.
  - **Zero-recipient edge case:** if recipient resolution yields an empty set — for example every resolved committee member is filtered out by a project-scoped unsubscribe — the send settles synchronously: the draft is marked `status=sent` with `total_recipients=0` (and `sent=0`, `failed=0`), `group_id` is persisted, and the endpoint returns `200`. No email is dispatched, and the newsletter cannot be sent again. This applies identically to a scheduled request against a zero-recipient audience — it settles straight to `sent`, never `scheduled`.
- `POST …/newsletters/{newsletter_uid}/schedule` reuses the same `send` machinery (`draft → sending`, same duplicate-send guard, same `202`), with two differences: it mints a provider `batch_id` and validates the arm-time window before transitioning, and it requires the active provider to support native scheduling (SendGrid) — `503 service_unavailable` otherwise. The background job settles a scheduled fan-out to `status=scheduled` instead of `sent` once at least one recipient's message has been accepted by the provider for release at `scheduled_at` (or reverts to `draft` if zero recipients could be scheduled). A settled status=scheduled does NOT guarantee all recipients were accepted — only that the schedule was armed. `POST …/send` on a draft carrying a saved `scheduled_at` is unaffected — it sends immediately and never consults the saved value. Consult the Failures list for per-recipient scheduling outcomes.
- **`scheduled`** means at least one recipient's message has been accepted by the provider and is held for release at `scheduled_at`, or the scheduling outcome was ambiguous and the batch was armed at the provider. The provider — not this service — owns the actual delivery timing. A periodic sweep (the same one that recovers stuck `sending` rows) flips `scheduled` rows to `sent` once `scheduled_at` has passed — this is reconciliation of our own display state only.
  - `POST …/newsletters/{newsletter_uid}/cancel-schedule` cancels the provider batch (best-effort near release) and reverts `scheduled → draft`, clearing `group_id`/`batch_id`/`total_recipients` but **retaining** `scheduled_at` — the author's picked time survives the cancel so re-scheduling doesn't require re-entering it. Rejected inside the configured cancel buffer of `scheduled_at` with `409 cancel_window_closed`. A provider-side cancel failure leaves the row truthfully still `scheduled` rather than reporting a cancellation that didn't take effect upstream.
  - `scheduled` newsletters cannot be updated, deleted, or (re-)sent/scheduled (`409 scheduled`) — cancel first.
  - A pod crash mid-fan-out for a scheduled send leaves `status=sending` with `scheduled_at` set; the stuck-sending sweep is schedule-aware and settles that case to `scheduled` (not `sent`), since the fan-out — not the release — is what crashed.
- Newsletters in `sending` cannot be updated, deleted, or sent again (`409 send_in_progress`). This also closes the race where an autosave landing mid-fan-out bumped the version and stranded a delivered newsletter in `draft`.
- Sent newsletters cannot be updated, deleted, or sent again (`409 already_sent`).
- **Crash recovery:** a pod dying mid-fan-out leaves `status=sending`. A sweep (startup + every 5 minutes) marks non-scheduled `sending` rows older than `STUCK_SEND_TTL` (default 45m, always ≥ `SEND_JOB_TIMEOUT` + 5m) as `sent` — after a crash an unknown number of emails already went out, so re-arming Send would guarantee duplicates, while settling to `sent` at worst under-reports a remainder that analytics (via `group_id`) makes visible. The same sweep also flips due `scheduled` rows to `sent` and stuck scheduled-fan-out rows to `scheduled` (see above). 
  - **Unarmed sends** (immediate sends, `batch_id IS NULL`): The rare crash-before-first-dispatch case can be repaired manually before the TTL elapses: `UPDATE newsletters SET status='draft', group_id=NULL, total_recipients=0, version=version+1 WHERE id='…' AND status='sending' AND batch_id IS NULL;` This repair is safe only when `batch_id IS NULL`; it resets the row to draft so the operator can retry.
  - **Armed scheduled sends** (`batch_id IS NOT NULL`): If a pod crashes mid-fan-out while a scheduled send has an armed SendGrid batch, **the operator must cancel the batch first** via `POST .../cancel-schedule` before attempting any SQL repair. Repairing without cancelling leaves the batch queued at SendGrid and risks duplicate delivery at the scheduled time. The `cancel-schedule` endpoint is idempotent; use it to ensure the batch is cancelled, then the sweep will settle the row correctly to `scheduled` or the operator can manually reset to `draft` if needed.

The list endpoint's `status=sent` filter also matches `sending` rows so an in-flight send stays visible on the Sent tab; `status=sending` and `status=scheduled` are each accepted as explicit filters, and `scheduled` is excluded from both the `draft` and `sent` filters.

The database enforces `status IN ('draft','sending','scheduled','sent')`, `status IN ('sent','scheduled') => group_id IS NOT NULL`, `status='scheduled' => (scheduled_at IS NOT NULL AND batch_id IS NOT NULL)`, and UUID format on `group_id`. A `draft` row may carry a `scheduled_at` with no `batch_id` — that is the saved-intent case, not yet armed.

## Recipient And Send APIs

Recipient resolution and the provider fan-out are documented in `docs/recipient-resolution.md`.

| Endpoint | Behavior |
| --- | --- |
| `…/newsletters/recipient-count` | Returns unique recipient count after resolving committee members. |
| `…/newsletters/recipients` | Returns unique recipient emails and first names. |
| `…/newsletters/test-send` | Validates fields and dispatches a single test email to `to_email` — no persistence, no analytics. The body renders the same compliance footer as a real send: sender attribution, optional reply-to line, the "My Newsletters" link, and a working unsubscribe link minted for `to_email` (clicking it records a real project-scoped opt-out for that address). Returns `{ "ok": true }`. |

The fan-out is gated by `SEND_FANOUT_ENABLED` (default true). When disabled, sends validate and transition state without dispatching email.

Real sends and test-sends render a compliance footer containing sender attribution, an optional reply-to line, a "My Newsletters" deep link (`<LFX_SELF_SERVE_BASE_URL>/newsletters/my`, default `https://app.lfx.dev/newsletters/my`) so recipients can browse past newsletters in Self-Serve, and the per-recipient unsubscribe small print. The "My Newsletters" line sits above the unsubscribe small print in both the HTML and plain-text bodies. An unset or empty `LFX_SELF_SERVE_BASE_URL` falls back to the production default — no supported configuration omits the line from sends (the renderer omits it only for callers that pass no URL).

## Analytics, Open Tracking, And Unsubscribe

`…/newsletter-opens/{newsletter_uid}?r=<recipient_hash>` is intentionally unauthenticated because email clients do not carry user sessions. The hash is a lowercase hex SHA-256 token. Malformed hashes are ignored and the endpoint still returns the pixel. Repeat opens from the same recipient within the same hour collapse to one row.

`…/newsletters/{newsletter_uid}/analytics` aggregates, gated on project ownership:

- persisted recipient total (snapshot at send time)
- local open rows, unique open counts by recipient hash, daily open buckets, open rate
- provider engagement totals fetched by `group_id` from the sending provider's `EngagementReader` (email-service over NATS, or the local SendGrid store the event webhook populates), overlaid best-effort — a failed engagement read falls back to local-only analytics
- `failed_recipients`: the lowercased, deduplicated email addresses the sending provider marked failed (synchronous send errors plus async bounce/complaint events), drawn from the per-recipient status records. Always present (empty array when there are no known failures or the per-recipient status fetch fails). Best-effort: because the scalar `failed` count comes from the engagement rollup while the list comes from the per-recipient records, the two may briefly diverge while the group index propagates. No failure reason is exposed (the per-recipient records carry no error string).
- `total_clicks`, `unique_clicks`, `click_rate` (`unique_clicks / <the same denominator as open_rate>`), `click_to_open_rate` (capped at `1.0`; uncapped formula is `unique_clicks / unique_opens`, where unique_clicks can exceed unique_opens if a recipient clicks without opening — e.g., open pixel blocked. `0` when there are no opens), `daily_clicks` (`[{ "date", "clicks", "unique_clicks" }]`), and `top_links` (`[{ "url", "clicks", "unique_clicks" }]`, ordered by `clicks` descending, capped at 20 URLs). **SendGrid-only**: a newsletter with `send_provider = "email-service"` (SES) always reports `total_clicks: 0`, `click_rate: 0`, `click_to_open_rate: 0`, `daily_clicks: []`, `top_links: []` — SES has no click-event delivery path into this service. Click series/list fields are always present (empty array, never null) on both providers, matching `daily_opens` / `failed_recipients`.

The engagement source is routed per newsletter by `send_provider`, so the response shape is identical whether the newsletter was sent via email-service (SES, engagement read over NATS) or SendGrid (engagement read from the local store the SendGrid event webhook populates) — except that click fields above only ever carry real data on the SendGrid path. See `docs/recipient-resolution.md` § Provider-Routed Analytics.

Click tracking excludes the compliance-footer links: the "Unsubscribe" and "My Newsletters" anchors are rendered with `clicktracking="off"`, so opting out or browsing past newsletters never counts toward `click_rate`, `daily_clicks`, or `top_links` — only clicks on the author's own content are tracked.

### Per-Recipient Engagement

`…/newsletters/{newsletter_uid}/analytics/recipients` returns `NewsletterRecipientEngagementResponse`, gated on project ownership like the aggregate endpoint. Unlike the aggregate analytics rule, the gateway rule requires the `auditor` relation, fail-closed (no `allow_all` fallback; unreachable when `openfga.enabled=false`) — the platform model's project `viewer` includes the public wildcard `[user:*]`, and this response exposes recipient identities plus per-recipient engagement history, so it takes the same posture as the opt-out list. Response fields:

- `total_recipients`: the newsletter's send-time audience snapshot.
- `complete`: `false` when the provider returned fewer per-recipient records than `total_recipients`. Both providers' per-recipient stores are best-effort — email-service silently omits missing/malformed records and its group index briefly lags a fresh send; the SendGrid store records no row for ambiguous send outcomes — so clients must treat an incomplete list as partial data, not as proof the absent recipients were never sent to.
- `recipients`: one row per recipient the sending provider recorded, with:
  - `email`: the recipient address the provider recorded for the send.
  - `name`: the recipient's full name, resolved best-effort at read time by re-querying the newsletter's committees (`lfx.committee-api.list_members`) and matching on lowercased email. Omitted when the member no longer appears in the committees, has no name on file, or the committee lookup fails — clients display `name` when present and fall back to `email`.
  - `delivered` / `delivered_at`, `failed` / `failed_at`: delivery outcome; timestamps omitted when unknown.
  - `opened`, `open_count`, `last_opened_at`, and `opened_at_list` — open timestamps, ascending. Per recipient, `opened_at_list` is capped at the 500 most recent recorded opens (enforced in the query) to bound response size. `open_count` is the provider's total recorded open count and may exceed the capped list length. `opened_at_list` is always present (empty array when the recipient never opened).
  - `clicked`, `click_count`, `last_clicked_at`, and `clicked_at_list` — click timestamps, ascending, same shape and 500-entry cap as the open fields above. **SendGrid-only**: on an email-service (SES) newsletter these are always `false` / `0` / omitted / `[]`.

Semantics and caveats:

- Records come from the provider that dispatched the newsletter, routed by `send_provider` (email-service per-recipient status over NATS, or the local SendGrid store). Local pixel opens are NOT included — they are keyed by recipient hash, which cannot be mapped back to an email address.
- Drafts, and sent rows without a `group_id`, return `200` with an empty `recipients` array (`complete: true` — nothing was sent). A provider engagement-read failure is an error response (`503`/`500`), never an empty list — an empty list always means "no records".
- `recipients` is sorted by lowercased email ascending and returned in full; no pagination (the audience is committee-bounded).
- Open counts inherit the usual tracking caveats: image blocking undercounts, mail-client prefetching (e.g. Apple Mail privacy protection) can overcount.

`/newsletters/unsubscribe?t=<token>` is intentionally unauthenticated; authorization comes from the HMAC-signed token binding `(project_uid, email)`. It is split into two stages so an automated link-checker or preview scanner following the `GET` link cannot itself trigger an unsubscribe:

- `GET` verifies the token and renders an HTML confirmation page — it never mutates state, on the first fetch or any repeat. Invalid tokens return `400` HTML. A HEAD request handled directly by the service is also a no-op, for the same reason. Note that the gateway ruleset (`charts/lfx-v2-newsletter-service/templates/ruleset.yaml`) allows `GET` and `POST` on this path but not HEAD, so HEAD probes are blocked at the gateway and never reach the handler; the handler's HEAD no-op is a defensive fallback for direct-service traffic that bypasses the gateway.
- `POST` re-verifies the same token and records the opt-out. It renders an HTML success page. Successful opt-outs are idempotent and project-scoped. This is the stage reached by the confirmation page's form submit, and also the stage an RFC 8058-compliant mail client hits directly for one-click unsubscribe (see below) — bypassing the confirmation page entirely.

Every outbound send (`send_orchestrator.go`, both the real fan-out and test-send) sets the RFC 8058 one-click unsubscribe headers whenever the `UnsubscribeService` is enabled (an HMAC secret and base URL are configured): `list_unsubscribe_url` is set to that recipient's own unsubscribe link (the same link substituted into the body), and `list_unsubscribe_post` is set to `true`. The **SendGrid** provider emits these as `List-Unsubscribe` / `List-Unsubscribe-Post` headers (via SendGrid's custom-headers field) today, so a compliant mail client can unsubscribe with a single POST and no confirmation page. The `lfx-v2-email-service` path forwards the same two fields over NATS, but the pinned `lfx-v2-email-service@v0.1.5` does not yet expose them and ignores the extra fields — so on `EMAIL_PROVIDER=email-service` the one-click header is not emitted until email-service adds support and the pin is bumped (the two-stage unsubscribe landing page still works on every provider). See the `sendEmailWire` comment in `email_dispatcher.go`. When the `UnsubscribeService` is disabled, neither field is set and no one-click header is emitted — the legacy footer copy is the only opt-out path in that configuration.

`GET /projects/{project_uid}/newsletter-opt-outs` lists everyone who has unsubscribed for the project — `{ "opt_outs": [{ "id": "...", "email": "...", "unsubscribed_at": "..." }] }`, ordered by `unsubscribed_at` descending. No name is returned; the unsubscribe token never captures one. No pagination in v1. The `id` field is the UUID PK and can be used with the DELETE endpoint to remove an individual opt-out.

## Error Mapping

| Condition | HTTP | Error code |
| --- | --- | --- |
| Missing or invalid auth | 401 | `unauthorized` |
| Missing record | 404 | `not_found` |
| Stale `If-Match` | 412 | `version_mismatch` |
| Draft already sent | 409 | `already_sent` |
| Send fan-out still in flight | 409 | `send_in_progress` |
| Newsletter is scheduled (edit/delete/send/schedule attempted) | 409 | `scheduled` |
| Cancel-schedule requested inside the cancel buffer | 409 | `cancel_window_closed` |
| Invalid request | 400 | `invalid_request` |
| A layout cannot be rendered — unknown `block_type`, an unknown/unembedded `template_key`, malformed markup, MJML compile failure (on `render-preview`, or render-on-write during create/update) | 422 | `unprocessable_entity` |
| Upstream conflict (typed `pkgerrors.Conflict`) | 409 | `conflict` |
| Upstream dependency unavailable | 503 | `service_unavailable` |
| Unexpected server error | 500 | `internal_error` |

Domain sentinels match first; typed `pkgerrors.*` wrappers from the NATS upstream clients (committee, project, email-dispatcher) match by `errors.As`. 5xx responses intentionally use a generic client message. Details are logged server-side.

`422 unprocessable_entity` is a client/markup error — the request was well-formed but its layout could not be rendered. This includes a `template_key` that names a block library the binary does not embed. The same status applies whether the layout fails on `render-preview` or on render-on-write during create/update, so an editor that previews then saves the same bad layout sees a consistent code. It is distinct from a failure to load the *default* render library, which is a deployment defect (the templates ship with the binary) and surfaces as `500 internal_error`.

## Change Checklist

- Update `pkg/api/newsletter.go`.
- Update `internal/handler/http.go` and route handlers.
- Update `internal/service/` and `internal/repository/` when behavior or persistence changes.
- Update `internal/schema/schema.sql` for database changes.
- Update this document.
- Update `docs/recipient-resolution.md` if recipient resolution or email fan-out behavior changes.
- Add or update tests.
- Run `make test` and `make check`.
