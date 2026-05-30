// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package auth carries the small set of types that both the inbound HTTP
// middleware and the outbound upstream HTTP clients need to share — most
// notably the context key under which the validated inbound bearer token
// is stored for forwarding to the LFX platform query service.
//
// Splitting this out of internal/handler keeps internal/infrastructure
// from depending on the transport layer.
package auth

import "context"

type bearerContextKey struct{}

// bearerContextKeyValue is the context key under which the raw inbound bearer
// token is stored after JWT validation. The only legitimate consumer is the
// upstream package, which forwards the token to the platform query service.
var bearerContextKeyValue = bearerContextKey{}

// WithBearer returns a copy of ctx carrying the given bearer token. Callers
// (typically the HTTP auth middleware) attach the token here so outbound
// clients can read it via BearerFromContext.
func WithBearer(ctx context.Context, bearer string) context.Context {
	return context.WithValue(ctx, bearerContextKeyValue, bearer)
}

// BearerFromContext returns the raw inbound bearer token. Empty when auth is
// disabled or the request arrived without a token. Do not log the result.
func BearerFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(bearerContextKeyValue).(string); ok {
		return v
	}
	return ""
}
