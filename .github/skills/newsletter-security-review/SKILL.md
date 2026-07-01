---
name: newsletter-security-review
description: >
  Security review for lfx-v2-newsletter-service pull requests. Use when a PR
  touches a handler, auth, persistence, the dispatch path, recipient data,
  config, or the chart. Applies a diff-aware, high-confidence,
  low-false-positive methodology (adapted from Anthropic's
  claude-code-security-review) to this service's durable threat anchors:
  recipient PII, the deliberately unauthenticated endpoints, JWT
  verification, gateway-delegated authorization, SQL construction, and
  secrets. Discovers the concrete guards
  from the code at review time; this skill carries the method, not an
  inventory.
allowed-tools: Read, Glob, Grep
---

<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Newsletter Service Security Review

This service handles **member PII** (recipient emails and names in transit)
and exposes **deliberately unauthenticated endpoints**: a leak is a privacy
incident, and parts of the surface are reachable by anyone on the internet.
Those facts set the stakes for every security judgment here.

## Methodology

Run a focused, **diff-aware** review, not a whole-repo audit:

1. **Only new risk.** Assess what this PR introduces or weakens. Do not
   relitigate pre-existing issues the diff does not touch (at most a `nit`).
2. **Assume hostile input, report only what is real.** Flag only
   high-confidence, concretely exploitable findings: if you cannot trace a
   path from an attacker-controlled input to a sensitive sink, it is not
   `critical`/`high`.
3. **Three passes.**
   - *Context*: discover, from the code and the repo docs at review time, the
     guards this service relies on around the diff (format checks,
     validators, body caps, parameterized queries, signature verification).
     Never assume a guard exists; find it.
   - *Comparative*: does the change deviate from the guard patterns the
     surrounding code establishes?
   - *Assessment*: trace each input to its sink and confirm a guard sits on
     the path the data actually takes, not three functions away.
4. **Confidence-gate every finding** (1-10, report only >= 7). A few real
   findings beat a speculative list.
5. **Evidence, not vibes.** Each finding names the file and function, what
   the attacker controls, the boundary crossed, the concrete impact, and the
   fix.

## Durable threat anchors

These are the kinds of boundaries that make a diff security-relevant in this
service. They describe the service's shape, not its current line-level
guards; verify the concrete mechanism in the code each time.

- **The unauthenticated surface.** Two routes are reachable by anyone with no
  session: the open-tracking pixel (guarded by an opaque recipient hash) and one-click
  unsubscribe (guarded by an HMAC-signed token, and it writes a project-scoped
  opt-out). Weakening either token check, returning detail to an anonymous caller,
  broadening what an anonymous write can do, or adding any new unauthenticated route
  is the top of the severity scale.
- **Authentication.** JWT verification in the middleware, its toggle, and the
  deliberate decision not to forward the bearer to downstream NATS calls.
  Watch for weakened validation (audience, expiry, algorithms), library
  errors echoed to unauthenticated callers, and any path that starts
  forwarding or logging a bearer.
- **Authorization lives at the gateway.** The service runs no access checks
  of its own; the chart's Heimdall RuleSet is the entire authorization model.
  A handler or route change must keep the route shape the RuleSet gates on;
  a chart change to the rules is an authorization change outright.
- **Recipient PII.** Raw emails move through the service in process and over
  NATS during dispatch. Open tracking persists only an opaque recipient hash, but
  the unsubscribe table persists the lowercased email (project-scoped) — so a
  durable email store already exists, and its exposure, how it is matched, and the
  path that reaches it are in scope. Any new path that logs, returns, or stores an
  email or name beyond that, or weakens a hash, is a leak. The newsletter HTML body
  travels to real inboxes: if a PR changes how it is rendered or what gets
  interpolated into it, injection into the email becomes the question.
- **SQL and schema.** Queries go through a parameterized builder; any
  hand-built SQL string carrying input is injection. The schema's CHECK
  constraints and uniqueness rules are security guards as well as data
  rules; a migration that drops one deserves the same scrutiny as code.
- **Input bounds.** The service bounds request bodies, list sizes, field
  lengths, and fan-out. A new input path that bypasses the established
  decode/validation helpers, or a loosened cap with no reason, is a finding.
- **Secrets and config.** Configuration is read in one documented place;
  secrets (DB credentials) must never appear
  in logs, responses, or plaintext chart values. Chart changes that weaken
  the network policy or expose a route that should sit behind the gateway
  count here too.

## What not to flag

Signal discipline keeps the reviewer trusted. Do not raise:

- Denial of service, resource exhaustion, or "add rate limiting" on their
  own. (Unbounded *unauthenticated* writes are flagged as data integrity,
  not load.)
- Mere lack of hardening or defense-in-depth with no concrete vulnerability.
- Outdated third-party dependencies (managed separately); a *new*
  dependency's risk belongs to the architecture lens.
- Theoretical race or timing issues with no practical exploit.
- Test-only files, Markdown, and docs.
- Log spoofing, regex-DoS, and missing audit logs.
- SSRF that only controls a path; it counts when the attacker controls host
  or protocol.

Precedents: UUIDs are unguessable and need no validation (an authorization
finding rests on a missing check, not on guessing an id); environment
variables and config are trusted inputs; logging URLs and non-PII is fine.

## Reporting

For each finding give the file and function, what the attacker controls, the
boundary crossed, the concrete impact on this service, and the fix. If the
diff does not touch an anchor above, do not invent a finding for it.
