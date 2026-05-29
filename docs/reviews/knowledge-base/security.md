<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Security

Empirical security patterns flagged on this repo: leaking internal detail through HTTP response bodies on unauthenticated or error paths, accepting untrusted input on the unauthenticated open-tracking pixel without bounds, and auth misconfiguration that silently weakens token verification. These are the patterns Copilot and the maintainer reviewer (dealako) raised and that were resolved by a code change on PR #3.

**Read when:** any file under `internal/handler/`, `internal/service/newsletter.go`, `internal/handler/middleware.go`, `cmd/newsletter-api/service/config.go`, or `internal/schema/schema.sql` changed — anything that writes an HTTP response body, validates a JWT, reads `REQUIRE_USER_AUTH` / `JWT_AUDIENCE`, or persists data from an unauthenticated endpoint.

---

## `security/5xx-error-body-leak` — Critical

**Pattern:** an HTTP handler echoes `err.Error()` directly into the response body for 5xx (or unauthenticated) responses, leaking DB errors, upstream response bodies, JWKS URLs, or other infrastructure detail to the client.

**Detect:** grep `internal/handler/**` for `err.Error()` written into a response body or JSON `Message` field. Confirm 5xx and 503 paths return a generic message (the repo uses `"internal server error"` for 5xx in `writeError`) and that only known 4xx domain errors forward the verbatim message. Check `/readyz` does not write raw DB ping errors.

**Empirical citation:** PR #3 `internal/handler/http.go:127` — Copilot — "writeError currently returns err.Error() to clients for all failures, including 5xx 'internal_error'. That can leak internal details (DB errors, stack context, upstream responses)." Also PR #3 `internal/handler/health.go:33` — Copilot — "Readyz includes raw database ping errors in the HTTP response body. Since /readyz is unauthenticated, this can leak internal infrastructure details." Resolved in `e9e61b0`: "writeError now masks err.Error() in 5xx response bodies … only forwards the message verbatim for 4xx domain errors."

**Failure message:** 5xx / unauthenticated response body leaks `err.Error()` (DB, upstream, or infra detail) to the client.

**Fix:** return a generic client message for 5xx and 503 (and unauthenticated `/readyz`), log the full error server-side via `slog.*Context`. Only echo the verbatim message for known 4xx domain errors.

---

## `security/jwt-error-leak-to-client` — Critical

**Pattern:** the JWT-validation middleware forwards the JWT library's `err.Error()` to an unauthenticated caller. Parse errors can contain JWKS endpoint URLs, key IDs, or other infrastructure detail.

**Detect:** in `internal/handler/middleware.go`, on the `requireUserAuth` failure path, confirm the client gets a generic `"invalid token"` (401) and the full error is logged server-side at warn level via `slog.WarnContext`. Flag any `writeError`/response that includes the library error string.

**Empirical citation:** PR #3 `internal/handler/middleware.go:128` — dealako (maintainer, blocking) — "`err.Error()` from the JWT library is forwarded verbatim to the unauthenticated caller. JWT parse errors can contain JWKS endpoint URLs, key IDs, or other infrastructure details. Return a generic message and keep the full error in the server log only." Resolved in `959e23d`.

**Failure message:** JWT validation error forwarded verbatim to an unauthenticated client (may leak JWKS URL / key IDs).

**Fix:** log the full JWT error with `slog.WarnContext` and return a generic `&authError{msg: "invalid token", status: http.StatusUnauthorized}`.

---

## `security/unauthenticated-pixel-unbounded-write` — Critical

**Pattern:** the unauthenticated open-tracking pixel (`/newsletter-opens/{id}`) persists an attacker-controllable value (`recipient_hash` from the `r=` query param) or writes a row per request with no dedup, letting any caller bloat `newsletter_opens` or inflate open counts.

**Detect:** confirm `recipient_hash` is validated against `^[a-f0-9]{64}$` (the package-level `recipientHashPattern`) before persistence — at the handler (`internal/handler/open.go`), the service (`internal/service/newsletter.go::RecordOpenWithHash`), and the DB CHECK constraint. Confirm the open insert dedups via the per-recipient-per-hour unique index (`uq_opens_newsletter_recipient_hour`) with `ON CONFLICT DO NOTHING`. Malformed hashes must be a silent no-op that still returns the pixel.

**Empirical citation:** PR #3 `internal/handler/open.go:47` — dealako (blocking) — "`recipient_hash` is taken verbatim from the query string and persisted as text with no validation. An attacker can store arbitrarily long or malformed strings in `newsletter_opens`." And PR #3 `internal/handler/open.go:35` — dealako (blocking) — "every GET produces a DB write … Any caller who learns a newsletter UUID — or guesses one — can inflate `total_opens` and grow `newsletter_opens` without bound." Resolved in `959e23d` with regex validation + partial unique index + `ON CONFLICT DO NOTHING`.

**Failure message:** unauthenticated open pixel persists unvalidated / undeduped data — `recipient_hash` not regex-bounded or open insert lacks per-hour `ON CONFLICT DO NOTHING`.

**Fix:** validate `recipient_hash` against `^[a-f0-9]{64}$` at the handler and service, enforce it with a DB CHECK, and keep the `(newsletter_id, recipient_hash, opened_at_hour)` unique index with `ON CONFLICT DO NOTHING`. Treat invalid hashes as a no-op that still returns the GIF.

---

## `security/request-body-no-size-cap` — Critical

**Pattern:** `decodeJSON` reads `r.Body` with no size limit before decoding. Endpoints accept large fields (`bodyHtml`), so a client can stream a multi-gigabyte body and exhaust memory/CPU before validation fires.

**Detect:** in `internal/handler/http.go::decodeJSON`, confirm `r.Body` is wrapped with `http.MaxBytesReader(nil, r.Body, maxRequestBodyBytes)` (1 MiB) before `json.NewDecoder`. Flag any new JSON-decoding path that bypasses `decodeJSON`.

**Empirical citation:** PR #3 `internal/handler/http.go:169` — dealako (blocking) — "`decodeJSON` reads `r.Body` directly with no size cap. A client can stream a multi-gigabyte body and hold this goroutine indefinitely before any validation fires. Wrap the body with `http.MaxBytesReader`." Resolved in `959e23d` with a 1 MiB cap.

**Failure message:** JSON body decoded without `http.MaxBytesReader` — unbounded request body.

**Fix:** route all JSON decoding through `decodeJSON`, which wraps the body with `http.MaxBytesReader` at the 1 MiB cap before constructing the decoder.

---

## `security/auth-enabled-but-audience-unscoped` — Critical

**Pattern:** when `REQUIRE_USER_AUTH=true` the startup config requires `JWKS_URL` but allows an empty `JWT_AUDIENCE`; an empty expected audience makes `AuthValidator.validate` skip the `aud` check, so any token signed by the JWKS keys is accepted regardless of intended audience.

**Detect:** in `cmd/newsletter-api/service/config.go::AppConfigFromEnv`, confirm `JWT_AUDIENCE` is required (non-empty) whenever `REQUIRE_USER_AUTH=true`. Flag a JWKS-required-but-audience-optional combination.

**Empirical citation:** PR #3 `cmd/newsletter-api/service/config.go:84` — Copilot — "When REQUIRE_USER_AUTH=true, AppConfigFromEnv enforces JWKS_URL but allows JWT_AUDIENCE to be empty. In AuthValidator.validate(), an empty expectedAudience skips the aud check entirely, so any token signed by the JWKS keys is accepted regardless of intended audience." Resolved in `959e23d`: "AppConfigFromEnv now also requires JWT_AUDIENCE when REQUIRE_USER_AUTH=true."

**Failure message:** auth enabled but `JWT_AUDIENCE` not required — audience check is skipped, accepting any JWKS-signed token.

**Fix:** require `JWT_AUDIENCE` in `AppConfigFromEnv` whenever `REQUIRE_USER_AUTH=true`, failing startup with a clear error on the misconfiguration.

---

## `security/dev-mode-forwards-invalid-token` — Important

**Pattern:** in local auth-disabled mode (`REQUIRE_USER_AUTH=false`), a failed JWT validation still stores the unvalidated bearer token on the request context (`upstream.BearerTokenContextKey`), so downstream `CommitteeQueryClient` calls forward an invalid token upstream.

**Detect:** in `internal/handler/middleware.go`, on the dev-mode validation-failure path, confirm the bearer is NOT attached to context. The token should be propagated only when validation succeeds (or when auth is disabled at boot, `h.auth == nil`, where there is nothing to validate).

**Empirical citation:** PR #3 `internal/handler/middleware.go:147` — Copilot — "In dev mode (RequireUserAuth=false), if JWT validation fails you still store the unvalidated bearer token in context … downstream CommitteeQueryClient calls may forward an invalid token and fail unexpectedly." Resolved in `e9e61b0`: "we now skip attaching both the user identity and the bearer to the request context."

**Failure message:** dev-mode JWT failure still attaches the unvalidated bearer to context — invalid token forwarded upstream.

**Fix:** when `requireUserAuth` is false and validation fails, skip attaching the bearer (and identity) to context. Only the `h.auth == nil` boot path may propagate the bearer.
