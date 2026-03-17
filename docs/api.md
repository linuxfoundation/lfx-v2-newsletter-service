# Newsletter Service API Guide

This guide covers local provisioning, authentication, and request examples for the current newsletter API.

## Local Provisioning (kind + Make)

### Prerequisites

- Docker
- kind
- kubectl
- Helm
- Make
- A populated `.env` file (start from `.env.example`)

### One-command local deploy

```bash
make deploy-quick
```

`make deploy-quick` will:

- create kind cluster `newsletter-test` (if needed)
- build and load the image into kind
- deploy Helm release `newsletter` in namespace `lfx-v2-newsletter-service`
- wait for rollout
- start local port-forward to `http://127.0.0.1:18080`

### Verify local access

```bash
curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:18080/health
```

Expected: `200`

## Base URL

- Local (port-forward): `http://127.0.0.1:18080`
- Direct local process (`make run`): `http://127.0.0.1:8080`

## Authentication

- Protected business endpoints require `Authorization: Bearer <token>`.
- Health endpoints are public.
- In local/dev mode, if `JWT_AUTH_DISABLED_MOCK_LOCAL_PRINCIPAL` is set, any non-empty bearer token can be used for protected endpoint testing.

## Endpoints

### 1) Get newsletters by tag

Primary route:

```http
GET /newsletters?tag=<tag>&limit=<1-100>&page=<page>=1&include_fields=<csv>&v=1
```

Alias route:

```http
GET /newsletters/tag?tag=<tag>&limit=<1-100>&page=<page>=1&include_fields=<csv>&v=1
```

Example:

```bash
curl -s -H 'Authorization: Bearer test-token' \
  'http://127.0.0.1:18080/newsletters?tag=Weekly%20ED%20Newsletter&limit=15&page=1&v=1'
```

### 2) Get newsletter by ID

Primary route:

```http
GET /newsletters/{id}?include_fields=<csv>&v=1
```

Alias route:

```http
GET /newsletters/id/{id}?include_fields=<csv>&v=1
```

Example:

```bash
curl -s -H 'Authorization: Bearer test-token' \
  'http://127.0.0.1:18080/newsletters/69b02ad450c7e50001107a23?v=1'
```

### 3) Health endpoints (public)

Primary:

- `GET /livez`
- `GET /readyz`

Backward-compatible aliases:

- `GET /health`
- `GET /ready`

## Response Notes

- Successful list response includes `newsletters` and `meta`.
- Successful single response includes `newsletter`.
- `Cache-Control` may be returned based on provider caching behavior.

## Common Error Statuses

- `400` bad request (invalid query/params)
- `401` unauthorized (missing/invalid bearer token)
- `404` not found
- `429` too many requests
- `500` internal server error
- `503` service unavailable

## OpenAPI Source

Generated OpenAPI is available at:

- `gen/http/openapi.yaml`
- `gen/http/openapi3.yaml`

