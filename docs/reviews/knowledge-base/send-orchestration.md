<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Send Orchestration

Patterns for `internal/service/send_orchestrator.go` and the NATS clients it drives: persisting a terminal send state so a client disconnect cannot strand the row, and mirroring a peer service's parsing exactly when this service pre-validates a cross-service contract.

Current send truth these patterns assume (see `docs/newsletter-service-contract.md` § State Transitions): the draft lifecycle is `draft → sending → sent`, this service mints the `group_id`, the per-recipient fan-out runs in a **detached** background job with bounded concurrency, and the row settles to `sent` when at least one recipient was delivered to.

**Read when:** `internal/service/send_orchestrator.go`, `internal/service/unsubscribe.go`, or anything under `internal/infrastructure/nats/` changed.

---

## `send/terminal-write-must-outlive-request-ctx` — Important

**Pattern:** a terminal state transition on the send path is issued with the raw inbound request `ctx`. When the client disconnects or an upstream proxy aborts, that context is cancelled and the write never lands, stranding the row in `sending` until the stuck-send sweep reclaims it.

**Detect:** in `internal/service/send_orchestrator.go`, confirm every terminal persistence call (`MarkSent`, `RevertSending`, and the zero-recipient settle) runs on a context detached from the request — `context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)` — rather than on the request `ctx` itself. Flag a terminal write that passes the request context straight through.

**Empirical citation:** PR #32 (MERGED) `internal/service/send_orchestrator.go` — Copilot `3534090347` — *"If the client disconnects or the upstream proxy aborts right after MarkSending, this MarkSent can be cancelled, leaving the row stuck in 'sending' … Use a non-cancellable context with a bounded timeout."* Resolved in `4309260e`. Verified at HEAD `f13d015`: `send_orchestrator.go:304` (zero-recipient settle), `:351` (terminal transition) and `:320` (fan-out job) all detach, with `persistTimeout = 30s` at `:37`.

**Failure message:** terminal send transition uses the request context — a client disconnect strands the newsletter in `sending` until the stuck-send sweep.

**Fix:** wrap the terminal write in `context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)` so it completes independently of the HTTP request's lifetime.

---

## `crosssvc/local-guard-must-mirror-peer-parsing-exactly` — Important

**Pattern:** this service pre-validates a value that a peer service will parse again (an email address, a domain, an identifier), but implements the check by its own intent rather than by copying the peer's exact parsing. The two derivations disagree on adversarial or unusual input, so a value this service accepts is interpreted differently downstream.

**Detect:** in `internal/service/send_orchestrator.go` and the NATS clients, for any helper that validates or derives part of a value the peer also parses, confirm the implementation matches the peer's documented parsing step for step — not merely its purpose. Address-domain derivation must follow `mail.ParseAddress` then `strings.SplitN(addr.Address, "@", 2)` rather than splitting the raw string at the last `@`. **That required order is stated here on purpose, so this check is self-contained:** you review a detached snapshot of this repo alone, so the peer's own repo is never present and must not be treated as a required read. `lfx-v2-email-service/docs/email-service-contract.md` is the provenance of the rule, not an input to the check — cite this entry, never that path. If a new cross-service pattern's rule cannot be stated fully inside the entry, it is not yet detectable here.

**Empirical citation:** PR #56 (MERGED) `internal/service/send_orchestrator.go` — Copilot `3626330233`, `3626519624` — *"This helper accepts `"a@b"@linuxfoundation.org` by splitting at the last `@`, but email-service parses the address and then uses `strings.SplitN(addr.Address, "@", 2)`, so that service derives a different domain."* Resolved in `9a94f7ca`, then corrected again in `3d38a219` — it took two rounds to converge, which is the lesson: local pre-validation of a cross-service contract must copy the peer's exact parsing, not its intent. Verified at HEAD `f13d015`: `send_orchestrator.go:818-827` matches the corrected form.

**Failure message:** local guard derives the value differently from the peer service that will re-parse it — this service accepts input the peer interprets another way.

**Fix:** copy the peer's parsing sequence exactly, citing its contract doc, and add a case for the input that distinguished the two implementations.
