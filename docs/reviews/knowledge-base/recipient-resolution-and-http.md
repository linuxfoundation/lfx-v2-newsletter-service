<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Recipient Resolution and HTTP

Patterns for the recipient-resolution path (concurrent committee lookups against query-service, normalization, the email-service `groupId` handoff) and the HTTP error-classification surface. These are the patterns the maintainer reviewer and Copilot flagged on PRs #3 and #9.

**Read when:** `internal/service/send_orchestrator.go`, `internal/infrastructure/upstream/**`, `internal/handler/send.go`, `internal/handler/http.go`, or `internal/service/newsletter.go` changed — anything that resolves recipients, calls query-service, maps domain errors to HTTP status, or handles the `groupId` send handoff.

---

## `recipient/errgroup-cancellation-on-failure` — Important

**Pattern:** concurrent committee lookups launch all goroutines up front (`sync.WaitGroup` + result slice) and return on the first error, but the remaining goroutines keep running against the upstream query-service with no cancellation.

**Detect:** in `internal/service/send_orchestrator.go::resolveRecipients`, confirm concurrent committee fetches use `errgroup.WithContext` so the first error cancels the shared `gctx` and aborts in-flight `http.NewRequestWithContext` calls. Flag a hand-rolled `WaitGroup` fan-out with no cancellation.

**Empirical citation:** PR #3 `internal/service/send_orchestrator.go:167` — dealako (blocking) — "If one goroutine returns an error, `resolveRecipients` returns early — but the remaining goroutines continue running against the upstream query service until they finish naturally, with no way to cancel them. Use `golang.org/x/sync/errgroup`." Resolved in `959e23d`: "replaced the `sync.WaitGroup` + manual result slice with `errgroup.WithContext`."

**Failure message:** concurrent committee fetches don't cancel siblings on first error — orphaned upstream calls.

**Fix:** use `errgroup.WithContext(ctx)`; pass `gctx` into each `GetMembers` call so the first error cancels the rest.

---

## `recipient/unbounded-pagination-loop` — Important

**Pattern:** the query-service committee-member client follows `next_page_token` in an unbounded `for` loop. The per-call HTTP timeout does not bound the whole sequence, so an upstream that always returns a non-empty token loops until context cancellation.

**Detect:** in `internal/infrastructure/upstream/committee_client.go`, confirm the pagination loop has a max-pages guard (`maxQueryPages = 100`) that returns an error past the cap. Flag a `next_page_token` loop with no page ceiling.

**Empirical citation:** PR #3 `internal/infrastructure/upstream/committee_client.go:70` — dealako (blocking) — "`GetMembers` follows `next_page_token` in an unbounded loop … a buggy or adversarial upstream that always returns a non-empty token will loop forever. Add a max-pages guard." Resolved in `959e23d` with `maxQueryPages = 100`.

**Failure message:** query-service pagination loop has no max-pages guard — infinite loop on a non-terminating token.

**Fix:** add a `maxQueryPages` ceiling and return an error when exceeded; keep the request `ctx` threaded for client-side cancellation.

---

## `recipient/normalize-before-dedup` — Important

**Pattern:** committee UIDs or recipient emails are deduped/stored without normalizing first (trimming whitespace, lowercasing), so logically-equal values (`"abc"` vs `" abc"`, mixed-case emails) are treated as distinct — causing extra upstream calls and inconsistent persistence.

**Detect:** in `internal/service/newsletter.go` (committee UID handling) and `internal/service/send_orchestrator.go::resolveRecipients`, confirm values are `strings.TrimSpace`d (and emails `strings.ToLower`ed) before dedup/storage. Emails must also drop empty values and values without `@`.

**Empirical citation:** PR #3 `internal/service/newsletter.go:298` — Copilot — "validateCommitteeUIDs trims values only to check emptiness, but the values stored/deduped keep original whitespace. This can lead to logically-duplicate committee UIDs … being treated as distinct, causing extra upstream calls and inconsistent persistence." Resolved in `e9e61b0`: "replaced dedupeStrings with normalizeCommitteeUIDs which trims surrounding whitespace and dedupes on the normalized value before storage."

**Failure message:** values deduped/stored without normalizing first — logical duplicates persisted (extra upstream calls).

**Fix:** trim (and lowercase emails) before deduping and storing; dedup on the normalized value. Drop empty emails and values without `@`.

---

## `recipient/groupid-must-be-validated-uuid` — Important

**Pattern:** the email-service `groupId` accepted on `/newsletters/drafts/{id}/send` is treated as a plain required string and persisted as-is, even though the contract says it is a UUID. Whitespace or malformed values get stored and later break email-service analytics lookups.

**Detect:** in `internal/service/send_orchestrator.go::SendDraft`, confirm the incoming `groupId` is `strings.TrimSpace`d, parsed with `uuid.Parse`, and the normalized `.String()` persisted; invalid values must be rejected with `ErrInvalidRequest` before `MarkSent`. (DB CHECK is defense-in-depth, see `persistence/sent-state-data-integrity-checks`.)

**Empirical citation:** PR #9 `internal/service/send_orchestrator.go:73` — Copilot — "`groupId` is treated as a required string, but the PR contract describes it as a UUID. Consider trimming + validating it with `uuid.Parse` (and persisting the normalized `.String()`), otherwise invalid/whitespace values can be stored and later analytics lookups in email-service will fail." Resolved in `7232a31`: "SendDraft now trims the incoming `groupId`, parses it with `uuid.Parse`, and persists the normalized `.String()`."

**Failure message:** `groupId` not trimmed + `uuid.Parse`-validated before persistence — malformed correlation IDs reach the DB.

**Fix:** trim, `uuid.Parse`, persist the normalized `.String()`; reject invalid/whitespace values with `ErrInvalidRequest` before `MarkSent`.

---

## `recipient/groupid-missing-from-api-mapper` — Important

**Pattern:** a newly persisted field (e.g. `groupId`) is added to the domain model and DB but not to the `toAPINewsletter` mapper, so the `/send` response and every list endpoint that reuses the mapper silently omit it — defeating the purpose of persisting it.

**Detect:** when a field is added to `model.Newsletter` and `pkg/api/newsletter.go`, confirm `toAPINewsletter` (in `internal/handler/drafts.go`) sets it from the domain value. Cross-check that every endpoint reusing the mapper now returns the field.

**Empirical citation:** PR #9 `internal/handler/send.go:52` — Copilot — "`toAPINewsletter` … does not populate the new `groupId` field. As a result, the response (and list endpoints that reuse this mapper) will omit `groupId`, undermining the purpose of persisting it." Resolved in `7232a31`: "added `GroupID: n.GroupID` to `toAPINewsletter`."

**Failure message:** new DTO field added to model/schema but not wired into `toAPINewsletter` — response omits it.

**Fix:** set the new field in `toAPINewsletter` from the domain model so every endpoint reusing the mapper returns it.

---

## `http/upstream-error-not-mapped-to-503` — Important

**Pattern:** `classifyError` maps only domain sentinel errors and `authError`. Upstream query-service failures wrapped as `pkgerrors.ServiceUnavailable` fall through to the default branch and surface as `500 internal_error` instead of the intended `503 service_unavailable`.

**Detect:** in `internal/handler/http.go::classifyError`, confirm there is an `errors.As(err, &svcUnavailable)` case mapping `pkgerrors.ServiceUnavailable` to `503 service_unavailable`. Cross-check `docs/newsletter-service-contract.md` error-mapping table (`Upstream dependency unavailable → 503`).

**Empirical citation:** PR #3 `internal/handler/http.go:152` — Copilot — "Upstream failures are wrapped as pkg/errors.ServiceUnavailable … but they currently fall into the default branch and return 500/internal_error. Consider adding an errors.As() case for pkg/errors.ServiceUnavailable." Resolved in `959e23d`: "classifyError now does `errors.As(err, &svcUnavailable)` … and returns `503 service_unavailable`."

**Failure message:** upstream `ServiceUnavailable` not mapped in `classifyError` — surfaces as 500 instead of the contracted 503.

**Fix:** add an `errors.As(err, &svcUnavailable)` case in `classifyError` returning `503 service_unavailable`, matching the contract error table.
