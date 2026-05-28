<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Go HTTP And Postgres Conventions

Use this reference when editing implementation code in `lfx-v2-newsletter-service`.

## Package Boundaries

| Area | Rule |
| --- | --- |
| `cmd/newsletter-api/` | Startup, runtime config, and dependency wiring. |
| `pkg/api/` | Public HTTP DTOs consumed by callers. |
| `internal/handler/` | HTTP routing, auth middleware, JSON decoding, error-to-status mapping. |
| `internal/service/` | Business rules, validation, draft/send orchestration, recipient normalization. |
| `internal/repository/` | Bun/Postgres persistence and cursor encoding. |
| `internal/schema/` | Embedded SQL schema applied at startup. |
| `internal/infrastructure/upstream/` | HTTP clients for sibling services. |

Do not put business rules in handlers or transport concerns in repositories.

## Handler Shape

- Register routes in `internal/handler/http.go`.
- Keep auth-protected routes behind `withAuth`.
- Use `decodeJSON` for JSON bodies so unknown fields and oversized bodies are rejected consistently.
- Map domain errors only at the handler boundary.
- Set and require ETags for optimistic-locking routes.

## Postgres Shape

- Change `internal/schema/schema.sql` first when persistence changes.
- Keep schema SQL idempotent.
- Preserve advisory-lock schema application.
- Use repository methods for all database access from service code.
- Keep page tokens opaque and service-owned.

## Upstream Calls

- Use interfaces from `internal/domain/port`.
- Forward bearer tokens only after this service has validated them.
- Read `lfx-v2-query-service/docs/query-service-contract.md` before changing query-service calls.

## Testing

- Prefer table-driven tests.
- Use fake repositories and fake upstream clients for service tests.
- Handler tests should assert status codes, ETags, and error codes.
- Run `make test` after implementation changes.

## Review Checklist

- Public route or DTO changed? Update `docs/newsletter-service-contract.md`.
- Recipient lookup or email handoff changed? Update `docs/recipient-resolution.md`.
- Chart or database deployment changed? Update `docs/service-helm-chart.md`.
- New Go files have the standard two-line license header.
