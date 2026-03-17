# LFX v2 Newsletter Service

The LFX v2 Newsletter Service is a microservice that integrates with CMS providers (Ghost currently supported) to provide newsletter content through the LFX platform API. Built with Go and the Goa framework, it provides JWT-authenticated endpoints for retrieving newsletters filtered by tags or by specific IDs.

## 🚀 Quick Start

### For Local Development

1. **Prerequisites**
   - Go 1.25+ installed
   - Make installed
   - CMS provider API credentials

2. **Clone and Setup**

   ```bash
   cd /Users/lewis/go/src/github.com/linuxfoundation/lfx-v2-newsletter-service
   
   # Install dependencies and generate API code
   make deps
   make apigen
   ```

3. **Configure Environment**

   Copy the example env file and set required secrets:
   ```bash
   cp .env.example .env
   ```

   Required values:
   - `GHOST_API_KEY`
   - `JWKS_URL` (production/staging) **or** `JWT_AUTH_DISABLED_MOCK_LOCAL_PRINCIPAL=local-dev-user` (local-only)

4. **Run the Service**

   ```bash
   # Run with default settings
   make run
   
   # Or run with debug logging
   make debug
   ```

### Local Provisioning with kind (One Command)

Use this flow to provision a local Kubernetes environment and expose the service immediately for local testing.

1. **Additional Prerequisites**
   - Docker running locally
   - kind installed
   - kubectl installed
   - Helm installed
   - `.env` file present with `GHOST_API_KEY`

2. **Provision + Deploy + Local Access**

   ```bash
   make deploy-quick
   ```

   This command will:
   - Create the `newsletter-test` kind cluster if missing
   - Build and load the service image into kind
   - Deploy/upgrade Helm release `newsletter` in namespace `lfx-v2-newsletter-service`
   - Wait for rollout completion
   - Start `kubectl port-forward` to `http://127.0.0.1:18080`

3. **Verify Service Availability**

   ```bash
   curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:18080/health
   ```

   Expected response: `200`

4. **Port-forward Logs (if needed)**

   ```bash
   tail -f /tmp/newsletter-port-forward.log
   ```

5. **Tear Down Local Cluster**

   ```bash
   kind delete cluster --name newsletter-test
   ```

## 🏗️ Architecture

The service follows clean architecture patterns with distinct layers:

- **API Layer**: Goa-generated HTTP handlers and OpenAPI specifications
- **Service Layer**: Business logic and orchestration
- **Domain Layer**: Core business models and interfaces
- **Infrastructure Layer**: CMS provider clients, JWT authentication, and caching

### Key Features

- **Newsletter Retrieval**: Fetch newsletters by tag or ID from configured CMS provider
- **JWT Authentication**: Secure API access via Heimdall integration
- **Pagination Support**: Configurable page size and navigation
- **Response Caching**: Optional caching with configurable TTL
- **OpenTelemetry Tracing**: Distributed tracing support
- **Provider Extensibility**: Architecture supports multiple newsletter providers

## 🧭 Diagram Flow (Exact Sequence)

1. **Membership Source** (DB/external system) provides member records.
2. **Apache Airflow DAG** (scheduled Python job) reads and transforms the membership data.
3. Airflow authenticates with **Ghost Admin API Key** and imports members into **Ghost CMS**.
4. **Auditors** create **Newsletters (posts)** in Ghost and tag them as `news`.
5. **Ghost CMS** sends newsletters to imported members.
6. **LFX v2 Golang Microservice** uses the **Ghost Content API Key** via its internal `Ghost Client` to:
   - Fetch newsletters by tag: `GET /ghost/api/content/posts/?filter=tag:news`
   - Fetch a newsletter by ID: `GET /ghost/api/content/posts/{id}`
7. The microservice exposes authenticated REST endpoints:
   - `GET /newsletters?tag=news&limit=15&page=1&v=1`
   - `GET /newsletters/{id}?v=1`
   - Backward-compatible aliases remain available: `/newsletters/tag` and `/newsletters/id/{id}`.
8. The **LFX Individual Dashboard** makes authenticated calls to these endpoints to list or view a single newsletter.
9. **Active Members** (authenticated users) access the dashboard to read newsletters.

## 📁 Project Structure

```bash
lfx-v2-newsletter-service/
├── cmd/
│   └── newsletter-api/         # Application entry point
│       ├── main.go
│       ├── server.go
│       └── config.go
├── design/                     # Goa API design files
│   ├── newsletter-svc.go       # Service definition
│   ├── newsletter_types.go     # Newsletter type definitions
│   └── types.go                # Common types and errors
├── gen/                        # Generated code (DO NOT EDIT)
│   ├── http/                   # HTTP transport layer
│   └── newsletter_service/     # Service interfaces
├── internal/                   # Private application code
│   ├── domain/                 # Business domain layer
│   │   ├── models/             # Domain models
│   │   ├── errors.go           # Domain-specific errors
│   │   └── repository.go       # Repository interfaces
│   ├── infrastructure/         # Infra adapters
│   │   ├── auth/               # JWT/JWKS authentication
│   │   └── cms/                # CMS abstraction + providers
│   │       ├── core/
│   │       └── providers/
│   │           └── ghost/
│   └── service/                # Service layer
│       └── newsletter_service.go
├── pkg/                        # Shared utilities (OTel, helpers, redaction)
├── scripts/                    # Local helper scripts (e.g. auth smoke)
├── docs/                       # Service docs (API/tracing)
├── charts/                     # Helm chart for Kubernetes
│   └── lfx-v2-newsletter-service/
├── Dockerfile                  # Container image definition
├── Makefile                    # Build automation
├── go.mod                      # Go module definition
└── README.md                   # This file
```

## 🔌 API Endpoints

### Get Newsletters by Tag

```bash
GET /newsletters?tag=news&limit=15&page=1&v=1
Authorization: Bearer <jwt-token>

# Alias
GET /newsletters/tag?tag=news&limit=15&page=1&v=1
```

### Get Newsletter by ID

```bash
GET /newsletters/{id}?v=1
Authorization: Bearer <jwt-token>

# Alias
GET /newsletters/id/{id}?v=1
```

### Public Health Endpoints (No JWT Required)

```bash
GET /livez
GET /readyz

# Backward-compatible aliases
GET /health
GET /ready
```

## 🛠️ Development

### Generate API Code

```bash
make apigen
```

### Run Tests

```bash
make test

# With coverage
make test-coverage

# End-to-end strict JWT auth smoke check
make auth-smoke
```

### Quality Gates

```bash
make lint
make check
make verify
```

### Build

```bash
make build
```

### Docker

```bash
# Build image
make docker-build

# Run container
make docker-run
```

## 📊 Monitoring & Observability

### OpenTelemetry Tracing

Enable tracing by setting environment variables:

```bash
OTEL_TRACES_EXPORTER=otlp
OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317
OTEL_EXPORTER_OTLP_INSECURE=true
```

Related docs:
- [API Usage Guide](./docs/api.md)
- [Tracing Guide](./docs/tracing.md)

### Metrics

The service exposes metrics for:
- CMS provider API request duration
- Request count by status code
- Cache hit/miss ratio
- Error rates

## 🔐 Security

- All endpoints require valid JWT authentication
- JWT auth validates PS256 tokens against Heimdall JWKS and requires the `principal` claim
- CMS provider credentials stored in secrets
- Rate limiting on upstream provider API calls
- HTML content sanitization support

## ✅ Auth Compliance Checklist

Use this checklist for any authentication-related PR.

### Required Behavior

- [ ] Unauthenticated request to protected endpoint fails
- [ ] Valid JWT with correct audience succeeds
- [ ] JWT missing `principal` fails
- [ ] Wrong audience or issuer fails
- [ ] Mock local principal works only in local/dev
- [ ] `/livez` and `/readyz` remain publicly accessible

### Required Implementation

- [ ] Goa design keeps JWT security on business endpoints
- [ ] JWT auth package validates Heimdall JWKS + required `principal`
- [ ] Startup wiring maps env config into auth initialization
- [ ] Goa `JWTAuth` stores validated principal in request context
- [ ] Helm/deployment values define JWT variables with local bypass empty in non-dev

## 📝 License

Copyright The Linux Foundation and each contributor to LFX.
SPDX-License-Identifier: MIT

## 🤝 Contributing

Please refer to the main LFX platform contribution guidelines.

## 📚 Additional Documentation

- [API Usage Guide](./docs/api.md)
- [Tracing Guide](./docs/tracing.md)
- [OpenAPI v3 (generated)](./gen/http/openapi3.yaml)
- [Kubernetes Deployment Blueprint](./kubernetes-deployment-blueprint.md)
- [Makefile Blueprint](./make.md)
- [Linting Blueprint](./linter.md)

## 🆘 Support

For issues or questions, please contact the LFX Platform Team.
