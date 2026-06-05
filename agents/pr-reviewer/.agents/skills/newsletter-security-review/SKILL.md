---
name: newsletter-security-review
description: >
  Security review for lfx-v2-newsletter-service pull requests. Use when a PR
  touches an HTTP handler, the auth middleware, the service or repository layer,
  the embedded schema, config/env reads, the recipient or open-tracking paths,
  the upstream query-service client, or the Helm chart. Walks this service's
  real threat surface: recipient PII and email handling, the unauthenticated
  open-tracking pixel, JWT and audience verification, bearer-token propagation,
  context/tenant scoping, response-body information leakage, SQL construction,
  request-body bounds, and secrets/config. Built from first principles for this
  repo, modeled on systematic security-review methodology, not a generic linter.
allowed-tools: Read, Glob, Grep
---

# Newsletter Service Security Review

Review the diff for security defects, from first principles, against this
service's actual surface. Method: for each changed area, identify what an
attacker controls, what the code trusts, and where trust crosses a boundary
(network input, an unauthenticated endpoint, a token, a SQL query, a log line,
a response body, a secret). Report only findings you can ground in the code.
Severity follows the reviewer's rubric (`critical`/`high`/`should-fix`/`nit`).

This service handles **member PII** (committee-member emails and names) and
runs an **unauthenticated endpoint**. Those two facts set the stakes: a leak is
a privacy incident, and the open pixel is reachable by anyone.

## Threat surface (check the categories the diff touches)

### 1. Unauthenticated open-tracking pixel

`GET /newsletter-opens/{id}` (`internal/handler/open.go`) has no auth: identity
comes only from the SHA-256 hash in `?r=`. Anyone who learns or guesses a
newsletter UUID can hit it.

- The `r=` hash MUST be validated against `^[a-f0-9]{64}$` before it reaches
  persistence, at the handler, again in
  `service.RecordOpenWithHash`, and enforced by the DB CHECK
  (`newsletter_opens_recipient_hash_format`). A malformed hash must be a silent
  no-op that still returns the GIF. Removing or weakening any of those three
  layers is `critical`.
- The open insert MUST dedupe via the per-recipient-per-hour unique index
  (`uq_opens_newsletter_recipient_hour`) with `ON CONFLICT DO NOTHING`. Without
  it, the unauthenticated endpoint writes a row per request: unbounded table
  growth and inflatable open counts. `critical`/`high`.
- The endpoint must never return 4xx/5xx detail to the email client, and must
  never echo the `id` or hash into a body. It only ever serves the pixel.
- Any new unauthenticated route, or any new write reachable without auth, is a
  `critical`-tier design question: justify it or flag it.

### 2. Recipient PII and email handling

- Raw emails are PII. They are legitimately handled in memory during recipient
  resolution and lowercased/deduped, but they must **not** be persisted in
  `newsletter_opens` (only the SHA-256 hash) and must **not** be logged.
  `slog.*Context` calls that include an email address, a recipient list, an
  `Authorization` header, or a bearer token are a leak: `high`/`critical`
  depending on reach. (Note `test-send` logs `to_email` today; treat any new
  email-in-log as a regression to flag, not a pattern to copy.)
- When email dispatch or rendering is wired (not present today), watch for
  **header injection** (CR/LF in subject, reply-to, or recipient fields) and
  **template/CTA injection** into the HTML body. `bodyHtml` is stored verbatim
  and bounded to 100 KiB but is not sanitized here; if a PR starts rendering or
  sending it, sanitization and header-safe construction become required and
  their absence is `critical`.
- `edReplyEmail` and `toEmail` are validated via `mail.ParseAddress`. A change
  that drops that validation, or accepts these fields into a send path
  unvalidated, is a finding.

### 3. Authentication and JWT verification

`internal/handler/middleware.go`, `cmd/newsletter-api/service/config.go`.

- When `REQUIRE_USER_AUTH=true`, both `JWKS_URL` and `JWT_AUDIENCE` must be
  required at startup. An empty expected audience makes `validate` skip the
  `aud` check, accepting any JWKS-signed token regardless of intended audience.
  A change that lets auth be enabled with an optional audience is `critical`.
- The JWT parse must keep `WithExpirationRequired()` and a constrained
  `WithValidMethods` allow-list (`RS256`/`ES256`/`PS256`). Adding `none`,
  widening to symmetric algorithms, or dropping expiry enforcement is
  `critical`.
- On the auth-required failure path, the client must get a generic
  `"invalid token"` 401; the library error goes to the server log only. Echoing
  the JWT library error to an unauthenticated caller can leak JWKS URLs or key
  IDs: `critical`.
- Dev mode (`REQUIRE_USER_AUTH=false`): a failed validation must NOT attach the
  unvalidated bearer to context. Only the boot-time `auth == nil` path may
  proceed without a token. A change that forwards an unvalidated bearer upstream
  is a finding.

### 4. Bearer-token propagation and upstream calls

`internal/infrastructure/upstream/committee_client.go`.

- The caller's bearer is forwarded to query-service so query-service applies
  access filtering. The token must be attached only when present, only to the
  configured query-service base URL, and never logged. A change that sends the
  bearer to a URL derived from request input (SSRF), or to a new upstream, needs
  scrutiny: `high`/`critical`.
- `committee_uid` is interpolated into the `tags` query value. It is URL-encoded
  via `url.Values.Encode()` today; a change that builds the query string by hand
  (string concatenation into the URL) reopens injection of query params and is a
  finding.
- The pagination cap (`maxQueryPages`) bounds a hostile or buggy upstream that
  always returns a next-page token. Removing the cap is a DoS/availability
  `high`.

### 5. Authorization / tenant (context) scoping

This is the highest-leverage thing to think about on this service, and it is
**not enforced in the data layer**. Drafts are fetched, updated, sent, and
deleted by `id` alone (`repo.Get(ctx, id)`), with no check that the
authenticated principal is entitled to the draft's `(contextType, contextUid)`.
The chart notes OpenFGA is reserved-for-future and "the RuleSet currently
authorizes via service-layer checks." So:

- Treat any change that touches draft access, listing, analytics, or send as an
  authorization-relevant change. Ask: can principal A read or mutate principal
  B's newsletter by guessing its UUID? If the PR widens that exposure (e.g. a
  new endpoint that returns a draft by id without a context check), it is
  `critical` and `needs_human`.
- A PR that **adds** real authorization (FGA tuples, an access-check call, a
  context-ownership guard) is a security-positive change but still
  `needs_human`: authorization changes always get a human.
- Do not assume Heimdall fully scopes this. Heimdall authenticates and may
  coarse-authorize at the edge; per-object ownership within a context is the
  service's concern. Verify against `lfx-platform-architecture` rather than
  asserting.

### 6. Information leakage on error paths

`internal/handler/http.go::writeError`, `health.go`.

- 5xx and 503 responses must return a generic message; the full error is logged
  server-side. Only known 4xx domain errors may forward a verbatim message.
  `/readyz` must not put raw DB ping errors in its body (it is unauthenticated).
  A change that echoes `err.Error()` into a 5xx or unauthenticated body is
  `critical`.
- Watch for new handlers that decode JSON outside `decodeJSON` or write errors
  outside `writeError`, bypassing both the body-size cap and the leak masking.

### 7. Input validation and resource bounds

- `decodeJSON` wraps the body with `http.MaxBytesReader` (1 MiB) and uses
  `DisallowUnknownFields`. Any new request-parsing path that bypasses it is a
  DoS and forward-compat finding.
- Service-layer caps (`maxSubjectLength` 200, `maxBodyHTMLLength` 100k,
  `maxCommitteesPerDraft` 50) bound per-request work and downstream fan-out.
  Loosening them without reason, or adding a list-like input with no cap, is a
  finding.
- List endpoints cap page size (`maxListLimit` 100) and use opaque keyset
  cursors; a change that lets the client drive an unbounded scan is `high`.

### 8. SQL and persistence

`internal/repository/postgres.go`.

- All queries go through bun's parameterized builder (`Where("... = ?", v)`).
  Any `fmt.Sprintf` of input into SQL, raw `ColumnExpr`/`Where` with
  interpolated user input, or hand-built query string is SQL injection:
  `critical`.
- The schema's invariants are security-relevant: `status='sent' => group_id
  NOT NULL`, the `group_id` UUID-format CHECK, the `context_type`/`status`
  enums, and the open-hash CHECK. A migration that drops or weakens any of these
  is `critical` and `needs_human`. `ON DELETE CASCADE` from `newsletter_opens`
  to `newsletters` must survive schema edits.
- Optimistic-locking gates (`WHERE id = ? AND version = ?`) prevent lost
  updates and double-sends. A write that drops the version gate is a
  data-integrity `high`.

### 9. Secrets and config

`cmd/newsletter-api/service/config.go`, the chart.

- All env reads live in `AppConfigFromEnv`. The DB password is composed
  in-process from `PG*` vars precisely so it is not a literal in the pod spec; a
  change that puts the password back into the manifest or a plain env value is a
  finding.
- No secret may be logged or returned in a response. Connection strings,
  `PGPASSWORD`, and any future API keys are secrets.
- Chart changes: an externally exposed route that should be behind Heimdall, a
  weakened `networkPolicy`, a disabled `requireUserAuth` default, or a secret
  moved out of ExternalSecrets/CloudNativePG into plaintext values is
  `critical`/`high` and `needs_human`.

## Reporting

For each finding give: the file and function, what an attacker controls, the
boundary that is crossed, the concrete impact on this service, and the fix.
Prefer a small number of real, well-grounded findings over a long list. If the
diff does not touch a category above, do not invent a finding for it.
