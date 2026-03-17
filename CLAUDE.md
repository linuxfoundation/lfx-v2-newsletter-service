# CLAUDE.md

This file provides guidance to coding agents when working with code in this repository.

## Architecture Overview

The LFX v2 Newsletter Service is a stateless Go microservice built with Goa v3.
It provides JWT-protected newsletter retrieval from configurable CMS providers (Ghost is the currently implemented provider).

The service follows a clean architecture pattern with:

- **API Layer**: Goa-generated HTTP handlers and OpenAPI specifications
- **Service Layer**: Request orchestration and domain-level operations
- **Domain Layer**: Core models, repository interfaces, and typed errors
- **Infrastructure Layer**: CMS provider adapters and JWT authentication components

## Key Features

- **CMS-agnostic provider model**: provider selection via `CMS_PROVIDER`
- **Newsletter retrieval by tag and by ID**
- **JWT Authentication**: Heimdall-compatible validation via JWKS
- **Public health/readiness endpoints**: `/livez`, `/readyz` (plus aliases)
- **Generated API contracts**: Goa design in `design/`, generated artifacts in `gen/`
- **One-command local Kubernetes provisioning**: `make deploy-quick` (build, deploy, port-forward)

## Key Architectural Components

**API Layer (Goa-generated)**
- Design specifications in `design/`
- Generated transport/client code in `gen/`
- HTTP server wiring in `cmd/newsletter-api/server.go`

**Domain Layer (`internal/domain/`)**
- Domain errors and repository interfaces
- Core newsletter models in `internal/domain/models/`

**Service Layer (`internal/service/`)**
- `NewsletterService` orchestrates domain operations over repository interfaces

**Infrastructure Layer (`internal/infrastructure/`)**
- `auth/`: JWT authenticator and principal parsing
- `cms/core/`: shared CMS integration infrastructure
- `cms/providers/ghost/`: Ghost provider client + transformations

**Application Bootstrap (`cmd/newsletter-api/`)**
- Configuration loading (`config.go`)
- Provider factory (`provider_factory.go`)
- Goa service adapter (`goa_service.go`)
- Main server startup/shutdown (`main.go`)

## Development Commands

### Core Development Workflow

- `make all` - Full local pipeline (clean, deps, apigen, fmt, lint, test, build)
- `make deps` - Install dependencies and Goa tooling
- `make apigen` - Generate API code from Goa design files
- `make build` - Build service binary
- `make run` - Run service locally
- `make debug` - Run with debug logging

### Testing

- `make test` - Run tests
- `make test-verbose` - Run tests with verbose output
- `make test-coverage` - Generate `coverage.out` and `coverage.html`
- `make auth-smoke` - Run strict JWT auth smoke script (`scripts/auth_smoke.sh`)

### Code Quality

- `make fmt` - Format source
- `make lint` - Run `golangci-lint` (falls back to `go vet` for local toolchain mismatch)
- `make license-check` - Validate SPDX/copyright headers
- `make check` - Non-mutating quality gate
- `make verify` - Ensure generated code in `gen/` is up to date

### Docker & Deployment

- `make docker-build` - Build Docker image
- `make docker-run` - Run Docker container
- `make deploy-quick` - Local kind deploy + rollout + auto port-forward to `127.0.0.1:18080`
- `make helm-install` - Install/upgrade Helm release
- `make helm-install-local` - Install/upgrade with local values override
- `make helm-templates` - Render Helm templates
- `make helm-templates-local` - Render Helm templates with local values override
- `make helm-uninstall` - Uninstall Helm release

## Development Guidelines

### Code Generation

- Run `make apigen` after modifying files under `design/`
- Do not manually edit generated files under `gen/`
- Run `make verify` before opening a PR

### Testing Strategy

- Add unit tests for service and provider behavior (`*_test.go`)
- Keep external dependencies mocked in tests where practical
- Use targeted tests first, then broader runs

### Error Handling

- Use domain/service error mapping consistently to HTTP responses
- Preserve status semantics (`400`, `401`, `404`, `429`, `500`, `503`)
- Keep logging structured and context-aware

### Authentication & Authorization

- Protected endpoints require bearer JWT
- JWT validation uses JWKS, audience, issuer, and required principal claim
- Local-only bypass is controlled by `JWT_AUTH_DISABLED_MOCK_LOCAL_PRINCIPAL`

### Dependencies

- Go version: `1.25.0` (see `go.mod`)
- Framework: Goa `v3.16.2`
- JWT libs: `github.com/golang-jwt/jwt/v5`, `github.com/MicahParks/keyfunc/v3`

## Environment Variables

### Core Service Configuration

- `PORT` (default `8080`)
- `LOG_LEVEL` (default `info`)

### CMS Configuration

- `CMS_PROVIDER` (default `ghost`)
- `CMS_TIMEOUT` (default `30s`)

### Ghost Provider Configuration

- `GHOST_API_BASE_URL`
- `GHOST_API_KEY`
- `GHOST_API_TIMEOUT`
- `GHOST_CACHE_TTL`
- `GHOST_MAX_RESULTS_PER_PAGE`

### Authentication Configuration

- `JWKS_URL`
- `JWT_AUDIENCE`
- `JWT_ISSUER`
- `JWT_CLOCK_SKEW`
- `JWT_AUTH_DISABLED_MOCK_LOCAL_PRINCIPAL` (local/dev only)

### OpenTelemetry Configuration

- `OTEL_SERVICE_NAME`
- `OTEL_SERVICE_VERSION`
- `OTEL_EXPORTER_OTLP_ENDPOINT`
- `OTEL_EXPORTER_OTLP_INSECURE`
- `OTEL_TRACES_EXPORTER`
- `OTEL_TRACES_SAMPLE_RATIO`

Reference: `.env.example` for recommended defaults and local values.

## Integration Flow(s)

### Request Flow (Protected Endpoints)

1. Client sends request with bearer token
2. Server validates JWT and extracts principal
3. Goa service adapter validates request payload/query parameters
4. Service layer calls CMS repository interface
5. Configured provider adapter (currently Ghost) performs upstream API request
6. Response is transformed into API schema and returned

### Local Provisioning Flow

1. Run `make deploy-quick`
2. kind cluster is created if missing
3. Image is built, loaded, and Helm release is installed/upgraded
4. Deployment rollout is awaited
5. Port-forward is started automatically for local access on `http://127.0.0.1:18080`

## Project Structure Notes

### Current Focus Areas

- CMS provider abstraction under `internal/infrastructure/cms/`
- JWT auth infrastructure under `internal/infrastructure/auth/`
- Goa-driven contract-first API with generated transport and OpenAPI artifacts

### Generated vs Editable Code

- Editable source: `cmd/`, `internal/`, `design/`, `pkg/`, `charts/`, `scripts/`
- Generated source: `gen/` (regenerate via `make apigen`)

## API Endpoints

### Health Checks (Public)

- `GET /livez`
- `GET /readyz`
- Backward-compatible aliases: `GET /health`, `GET /ready`

### Core Operations (JWT Required)

- `GET /newsletters?tag=<tag>&limit=<limit>&page=<page>&include_fields=<csv>&v=1`
- `GET /newsletters/{id}?include_fields=<csv>&v=1`

### Alias Routes (Backward-compatible)

- `GET /newsletters/tag?...`
- `GET /newsletters/id/{id}?...`
