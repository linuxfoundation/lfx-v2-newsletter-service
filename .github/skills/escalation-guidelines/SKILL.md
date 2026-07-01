---
name: escalation-guidelines
description: >-
  Detailed boundaries behind the needs-human decision for lfx-v2-newsletter-service:
  the critical surfaces (auth, the unauthenticated surface, schema, send behavior),
  shared and cross-repo contracts, scale-with-importance, and pipeline/supply-chain.
  Load this whenever judging whether a newsletter-service PR needs a human, as the
  detail behind the `needs-human-escalation` skill.
---

# Escalation guidelines (lfx-v2-newsletter-service)

These detail the boundaries behind the escalation decision: the service's
critical parts, its shared surfaces, and what scale-with-importance means here. A
change escalates to needs-human if it has that character, wherever in the tree it
lives; one that merely sits near such an area without moving it does not. Match
the substance, not the neighborhood.

Three properties of the service shape the critical boundaries. It runs no
authorization of its own: the gateway (Heimdall, from this repo's chart) decides
who may call each route. Two routes are deliberately reachable without
authentication (a recipient's mail client has no session), each guarded by its own
server-verified token. And every cross-service call travels over NATS to contracts
owned by peer services.

## Auth and the gateway

- **Authentication.** JWT verification in the HTTP middleware
  (`internal/handler/`), behind a toggle that can disable it, with the bearer
  deliberately not forwarded downstream. Any change to how a request is
  authenticated, to that toggle or its default, or that forwards the bearer.
- **Authorization.** The service checks no access itself; the chart's Heimdall
  RuleSet maps each route to a viewer or writer relation. Changing that mapping,
  adding a route without one, or adding or removing an in-service check. Merely
  routing a `project_uid` through a handler is not this.
- **The unauthenticated surface.** Two routes are reachable by anyone: the
  open-tracking pixel (guarded by an opaque recipient hash) and one-click unsubscribe
  (guarded by an HMAC-signed token). Both reach the database anonymously — the pixel
  records an open, unsubscribe writes a project-scoped opt-out. Any change to either
  guard or to what the endpoint does, or any new unauthenticated route or write.

## Shared and cross-repo surfaces

A break here lands in a repo this PR cannot show you, so lean on the skills.

- **`pkg/api`.** Imported by other repos; its JSON shapes mirror the Self Serve
  interfaces, and the version / `If-Match` concurrency surface is part of it.
  Changing shapes, casing, status codes, or concurrency semantics. Confirm
  consumers with the `lfx` skill (`skills/lfx/SKILL.md` in `linuxfoundation/lfx-skills`).
- **The schema** (`internal/schema/`). Encodes the invariants every deployed pod
  assumes: the draft-to-sent state machine, sent-requires-a-group-id, token and
  hash formats, cascade deletes, the idempotent lock-serialized apply.
- **NATS contracts.** Peers own the subjects this service calls (committee,
  project, email, auth). Changing a request or reply shape from this side, or
  adding a peer dependency. Resolve ownership with the `lfx` skill (`skills/lfx/SKILL.md` in `linuxfoundation/lfx-skills`).

## Sending behavior

Sending is the highest-blast-radius act, but only the behavior needs a human, not
the look. Presentation (rendered HTML and CSS, layout, copy, styling) is the
reviewer's domain and does not escalate, even though rendering happens in the
orchestrator. Send behavior does escalate: who the orchestrator resolves as
recipients, how sends fan out, dispatch order or idempotency, failure handling,
the integrity of the per-recipient group id, and first wiring
a send-adjacent capability the service lacks today. So does any path that logs,
returns, or stores a recipient email or name beyond what the service already
persists, weakens a hash, or changes how the database credentials are handled.
(Open tracking persists only an opaque recipient hash; the unsubscribe table
persists the lowercased email, project-scoped, so a change to how that email is
stored, exposed, or matched is in scope.)

## Scale and visibility

Some changes need a human for their weight, not a single boundary: a large change
reworking or touching many key workflows at once, or a significant,
high-visibility piece of work a lead should know is landing, even when each part
looks sound. Judge scale with importance, not line count: big but low-risk work
(a mechanical refactor, a sweep of UI, a batch of tests or docs) does not
escalate; a big change moving auth, the send path, the schema, or several core
handlers at once does.

## Pipeline and supply chain

Changes under `.github/`, to the chart (`charts/`, carrying the Heimdall RuleSet
and network policy), to `CODEOWNERS` or the build toolchain, or to the PR review
system's own config (the `.github/skills/` review skills and the
`.github/copilot-instructions.md` routing) change how code reaches production or
gets reviewed. A new
dependency, or a version bump in the auth path or to a pinned LFX module this
service couples to, shifts the supply chain. Routine patch and minor bumps of
uninvolved dependencies do not.
