# Escalation guidelines — lfx-v2-newsletter-service

**Draft starting point.** These are seeded from the change categories we already
know need a human on this service. Replace or refine them with the team's agreed
guidelines, this file is meant to be edited by maintainers. The escalation judge
(`AGENTS.md` next to this file) applies them to every PR and raises `needs-human`
when a change matches.

A change needs a human if it matches **any** of the following. Each guideline has
a short id so the verdict can cite it.

## Security & authorization

- **A1 — Authentication / JWT verification.** Any change to the auth middleware,
  JWKS handling, audience / expiry / algorithm checks, or what it means for a
  request to be authenticated.
- **A2 — Authorization / tenant scoping.** Any change to who can read or mutate a
  draft, the `(contextType, contextUid)` ownership model, or the introduction or
  removal of an access check or FGA tuple, in either direction (adding
  authorization counts too).
- **A3 — The unauthenticated surface.** Any change to the open-tracking pixel
  endpoint, its recipient-hash validation, or anything that adds a new
  unauthenticated route or unauthenticated write.
- **A4 — Secrets & PII.** Changes to secret / config handling, the DB-password
  composition, or any new path that logs or returns recipient emails or names.

## Data & contracts

- **B1 — Database schema / migrations.** Any change to `schema.sql`, a
  constraint, an index, an enum, or anything touching existing data or the
  idempotent-apply behavior.
- **B2 — Public API contract.** Any change to `pkg/api` request/response shapes,
  JSON field casing, status codes, or ETag / If-Match behavior (Self Serve
  consumes these).
- **B3 — Cross-repo handoffs.** Any change to the query-service request/parse
  contract or the email-service `groupId` handoff.

## Capability & surface

- **C1 — First wiring of a major capability** the service does not have today:
  real email dispatch, NATS publication to email-service, indexer messages, or
  FGA emission. These reshape the security and operational surface.

## Infra & supply chain

- **D1 — Infra / CI / deploy.** Changes under `.github/`, the Helm chart
  (`charts/`), `CODEOWNERS`, or the agents' own config (`agents/`).
- **D2 — Dependencies.** `go.mod` / `go.sum` additions or major version bumps.

## Judgment

- **E1 — Unbounded or unclear blast radius.** A change the reviewer flagged
  `critical` whose fix you cannot confirm is complete and self-contained, or any
  change large or subtle enough that you cannot confidently classify it. When in
  doubt, escalate.

> D1, D2 (and often B1) are also the cases a deterministic protected-file floor
> catches for free. The categories globs cannot express, A2 / A3 / B2 / B3 / C1 /
> E1, are where the escalation judge earns its keep.
