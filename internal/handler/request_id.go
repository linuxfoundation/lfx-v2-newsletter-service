// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/infrastructure/observability"
)

// RequestIDHeader is the canonical inbound/outbound header carrying the
// per-request correlation ID.
const RequestIDHeader = "X-Request-ID"

type requestIDContextKey struct{}

var requestIDContextKeyValue = requestIDContextKey{}

// RequestIDFromContext returns the request ID attached by withRequestID, or
// the empty string if none is present.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDContextKeyValue).(string); ok {
		return v
	}
	return ""
}

// withRequestID honors an inbound X-Request-ID header or generates a new
// UUID, echoes it on the response, stores it on the request context, and
// threads it into every slog.*Context call for the lifetime of the request.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set(RequestIDHeader, id)

		ctx := context.WithValue(r.Context(), requestIDContextKeyValue, id)
		ctx = observability.AppendCtx(ctx, slog.String("request_id", id))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
