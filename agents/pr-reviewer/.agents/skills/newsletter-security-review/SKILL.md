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
  repo, applying the diff-aware, high-confidence, low-false-positive methodology
  of Anthropic's claude-code-security-review skill, not a generic linter.
allowed-tools: Read, Glob, Grep
---

# Newsletter Service Security Review

Security review for an `lfx-v2-newsletter-service` diff, run with the discipline of
Anthropic's open `claude-code-security-review` skill and grounded in this service's
real surface. This service handles **member PII** (committee-member emails and
names) and runs an **unauthenticated endpoint** (the open-tracking pixel): a leak
is a privacy incident, and the pixel is reachable by anyone. Those two facts set
the stakes.

## Methodology (adapted from Anthropic's `claude-code-security-review`)

Run a focused, **diff-aware** review, not a whole-repo audit:

1. **Only new risk.** Assess the security implications this PR *introduces*. Do not
   relitigate pre-existing issues the diff does not touch (note them at most as a
   `nit`).
2. **Assume the code is hostile, report only what is real.** Flag only
   **high-confidence, concretely exploitable** findings. If you cannot trace a
   specific path from an attacker-controlled input to a sensitive sink, it is not
   `critical`/`high`.
3. **Three passes.** (a) *Context* — find the guards this repo already relies on
   (the `^[a-f0-9]{64}$` hash check, the `mail.ParseAddress` validators, the
   `MaxBytesReader` body cap, bun's parameterized builder, the JWT audience
   check). (b) *Comparative* — does the change deviate from those established
   patterns? (c) *Assessment* — trace each input to its sink and confirm the guard
   sits on the path the data actually takes, not three functions away.
4. **Confidence-gate every finding (1-10); report only >=7.** Prefer a few real
   findings to a long speculative list. Severity follows the reviewer's rubric
   (`critical`/`high`/`should-fix`/`nit`).
5. **Evidence, not vibes.** Each finding names the file, the function, the
   untrusted source, the boundary crossed, the concrete impact, and the fix.

Canonical categories (Anthropic's taxonomy, mapped to this Go service): injection
(SQL via raw string-building, path, template), authentication & authorization
(JWT/audience bypass, privilege escalation, IDOR / tenant-scope bypass), crypto &
secrets (hardcoded keys, weak randomness, secret logging), unsafe code execution
(deserialization), and data exposure (PII or secret in logs/responses, error
leakage). The repo surface below is where these land.

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
  it, the unauthenticated endpoint writes a row per request and inflates open
  counts. Flag this as a **data-integrity** finding (unbounded *unauthenticated*
  writes plus corrupted analytics), `high`, not as a DOS/load issue (we do not
  raise DOS as such).
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
  B's newsletter? The finding rests on the **missing `(contextType, contextUid)`
  ownership check**, not on guessing the UUID (treat UUIDs as unguessable, per the
  methodology). If the PR widens that exposure (e.g. a new endpoint that returns a
  draft by id without a context check), it is `critical` and `needs_human`.
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

## What not to flag (signal discipline)

Adopt Anthropic's exclusions so the gate stays trustworthy. Do **not** raise:

- **Denial of service, resource exhaustion, or "add rate limiting"** on their own.
  (The dedupe-index and pagination caps above are flagged for *data integrity* and
  unbounded *unauthenticated* writes, not as load problems.)
- **A mere lack of hardening / defense-in-depth** with no concrete vulnerability.
- **Outdated third-party dependencies** (managed separately); a *new* dependency's
  risk belongs to the senior reviewer, not here.
- **Theoretical race/timing issues** with no practical exploit.
- **Test-only files**, and anything in Markdown/docs.
- **Log spoofing** (un-sanitized user input in a log line is not itself a vuln),
  **regex injection/DoS**, and **missing audit logs**.
- **SSRF that only controls a URL path**; SSRF counts only when it controls the
  host or protocol (the bearer-propagation item above is exactly that case).

Precedents to apply: **UUIDs are unguessable** and need no validation (the draft
authorization finding rests on the *missing ownership check*, not on guessing the
id); **environment variables and config are trusted** inputs; **logging URLs and
non-PII is fine** — only secrets or PII (emails, names) in a log or response is a
finding. Raise `should-fix`/`MEDIUM` only when the issue is concrete.

## Reporting

For each finding give: the file and function, what an attacker controls, the
boundary that is crossed, the concrete impact on this service, and the fix.
Prefer a small number of real, well-grounded findings over a long list. If the
diff does not touch a category above, do not invent a finding for it.
