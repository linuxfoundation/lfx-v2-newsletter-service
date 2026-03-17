# OpenTelemetry Tracing

This document describes how distributed tracing is implemented in the LFX v2 Newsletter Service using OpenTelemetry.

For endpoint usage, local provisioning, and request examples, see the [API Usage Guide](./api.md).

## Overview

The Newsletter Service uses OpenTelemetry to provide distributed tracing capabilities, allowing you to track requests as they flow through the system and into external CMS provider dependencies.

## Configuration

Tracing is configured through environment variables:

```bash
# Service identification
OTEL_SERVICE_NAME=lfx-v2-newsletter-service

# OTLP Exporter configuration
OTEL_EXPORTER_OTLP_ENDPOINT=jaeger-collector:4317
OTEL_EXPORTER_OTLP_INSECURE=true

# Trace settings
OTEL_TRACES_EXPORTER=otlp
OTEL_TRACES_SAMPLE_RATIO=1.0
```

### Environment Variables

- `OTEL_SERVICE_NAME`: Name of the service in traces
- `OTEL_EXPORTER_OTLP_ENDPOINT`: OTLP collector endpoint (e.g., Jaeger, Tempo)
- `OTEL_EXPORTER_OTLP_INSECURE`: Use insecure connection (set to `false` for production)
- `OTEL_TRACES_EXPORTER`: Trace exporter type (`otlp`, `jaeger`, `zipkin`, or `none`)
- `OTEL_TRACES_SAMPLE_RATIO`: Sampling rate (0.0 to 1.0, where 1.0 = 100%)

## Implementation

### HTTP Client Instrumentation

The CMS provider client is instrumented using `otelhttp.Transport`:

```go
import (
    "go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

client := &http.Client{
    Timeout: timeout,
    Transport: otelhttp.NewTransport(http.DefaultTransport),
}
```

This automatically creates spans for all HTTP requests, capturing:
- Request method and URL
- Response status code
- Request duration
- Error information

### Manual Span Creation

For additional context, spans are manually created in each provider client adapter:

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
)

func (c *Client) GetByTag(ctx context.Context, tag string, limit int) ([]*models.Newsletter, *models.PaginationMeta, error) {
  tracer := otel.Tracer("cms-provider-client")
  ctx, span := tracer.Start(ctx, "provider.GetByTag")
    defer span.End()
    
    span.SetAttributes(
    attribute.String("provider.tag", tag),
    attribute.Int("provider.limit", limit),
    )
    
    // Make API call...
}
```

### Span Attributes

The following attributes are set on spans:

**Provider Client Operations:**
- `provider.tag`: Newsletter tag filter
- `provider.limit`: Maximum results per page
- `provider.newsletter_id`: Specific newsletter ID
- `http.method`: HTTP method (GET, POST, etc.)
- `http.url`: Full request URL
- `http.status_code`: HTTP response status

**Error Information:**
- `error`: Boolean indicating if an error occurred
- `error.type`: Error type/class
- `error.message`: Error description

## Trace Propagation

Context propagation happens automatically through the standard `context.Context` parameter:

```go
func (s *NewsletterService) GetNewslettersByTag(ctx context.Context, tag string, limit int) ([]*models.Newsletter, *models.PaginationMeta, error) {
    // Context is automatically propagated to repository
    return s.repo.GetByTag(ctx, tag, limit)
}
```

The `otelhttp.Transport` extracts and injects trace context headers (W3C Trace Context) automatically.

## Viewing Traces

### Local Development with Jaeger

1. Start Jaeger using Docker:

```bash
docker run -d --name jaeger \
  -p 16686:16686 \
  -p 4317:4317 \
  jaegertracing/all-in-one:latest
```

2. Set environment variables:

```bash
export OTEL_SERVICE_NAME=lfx-v2-newsletter-service
export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317
export OTEL_EXPORTER_OTLP_INSECURE=true
export OTEL_TRACES_EXPORTER=otlp
export OTEL_TRACES_SAMPLE_RATIO=1.0
```

3. Run the service and access Jaeger UI at http://localhost:16686

### Kubernetes Deployment

In Kubernetes, configure the OTLP endpoint to point to your trace collector:

```yaml
env:
  - name: OTEL_SERVICE_NAME
    value: "lfx-v2-newsletter-service"
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "jaeger-collector.observability.svc.cluster.local:4317"
  - name: OTEL_EXPORTER_OTLP_INSECURE
    value: "true"
  - name: OTEL_TRACES_EXPORTER
    value: "otlp"
  - name: OTEL_TRACES_SAMPLE_RATIO
    value: "1.0"
```

## Trace Examples

### Successful Request Trace

```
newsletter-service: GET /newsletters?tag=announcements
  ├─ provider.GetByTag (3ms)
  │   └─ HTTP GET https://cms.example.org/content/posts (45ms)
  │       ├─ DNS lookup (2ms)
  │       ├─ TCP connection (5ms)
  │       ├─ TLS handshake (15ms)
  │       └─ Server processing (23ms)
  └─ Response: 200 OK (50ms total)
```

### Error Trace

```
newsletter-service: GET /newsletters/invalid-id
  ├─ provider.GetByID (2ms)
  │   └─ HTTP GET https://cms.example.org/content/posts/invalid-id (30ms)
  │       └─ Response: 404 Not Found
  │           ├─ error: true
  │           ├─ error.type: ErrNewsletterNotFound
  │           └─ error.message: Newsletter not found
  └─ Response: 404 Not Found (35ms total)
```

## Sampling Strategies

### Production Sampling

For high-traffic production environments, use probabilistic sampling:

```bash
# Sample 10% of traces
OTEL_TRACES_SAMPLE_RATIO=0.1
```

### Development Sampling

For development and testing, capture all traces:

```bash
# Sample 100% of traces
OTEL_TRACES_SAMPLE_RATIO=1.0
```

### Adaptive Sampling

Consider implementing adaptive sampling based on:
- Error responses (always sample errors)
- Slow requests (always sample requests > threshold)
- Random sampling for normal requests

## Best Practices

1. **Always propagate context**: Pass `context.Context` through all layers
2. **Set meaningful attributes**: Add business-relevant data to spans
3. **Handle errors properly**: Record errors on spans using `span.RecordError(err)`
4. **Use semantic conventions**: Follow OpenTelemetry semantic conventions for attributes
5. **Avoid sensitive data**: Don't include API keys, tokens, or PII in span attributes
6. **End spans properly**: Use `defer span.End()` to ensure spans are closed
7. **Name spans clearly**: Use descriptive operation names (e.g., `provider.GetByTag`)

## Troubleshooting

### No traces appearing

1. Check OTEL environment variables are set
2. Verify collector endpoint is reachable
3. Confirm sampling ratio is > 0
4. Check logs for OTEL initialization errors

### Missing spans

1. Ensure context is propagated through all function calls
2. Verify HTTP client uses `otelhttp.Transport`
3. Check that spans are properly closed with `defer span.End()`

### High cardinality issues

Avoid adding unbounded values to span attributes:
- ❌ Bad: `span.SetAttributes(attribute.String("newsletter.content", content))`
- ✅ Good: `span.SetAttributes(attribute.Int("newsletter.content_length", len(content)))`

## Further Reading

- [OpenTelemetry Go Documentation](https://opentelemetry.io/docs/instrumentation/go/)
- [OpenTelemetry Semantic Conventions](https://opentelemetry.io/docs/reference/specification/trace/semantic_conventions/)
- [W3C Trace Context](https://www.w3.org/TR/trace-context/)
