// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

type userContextKey struct{}

// userContextKeyValue is the context key under which the authenticated user identifier
// is stored after JWT validation. Use UserFromContext to read it.
var userContextKeyValue = userContextKey{}

// AuthValidator validates inbound Heimdall-issued JWTs via a JWKS endpoint.
//
// A nil receiver represents "auth disabled" mode for local development; callers
// should construct one explicitly in production via NewAuthValidator.
type AuthValidator struct {
	jwks             keyfunc.Keyfunc
	expectedAudience string
}

// NewAuthValidator wires an AuthValidator that fetches JWKS from jwksURL and
// validates that the token audience contains expectedAudience.
//
// Passing an empty jwksURL returns a nil validator (auth disabled). Callers
// should log a warning when this happens in non-local environments.
func NewAuthValidator(ctx context.Context, jwksURL, expectedAudience string) (*AuthValidator, error) {
	if strings.TrimSpace(jwksURL) == "" {
		return nil, nil
	}
	jwks, err := keyfunc.NewDefaultCtx(ctx, []string{jwksURL})
	if err != nil {
		return nil, err
	}
	return &AuthValidator{
		jwks:             jwks,
		expectedAudience: expectedAudience,
	}, nil
}

// validate parses and verifies the JWT. Returns the user identifier (sub or
// principal claim) on success.
func (a *AuthValidator) validate(tokenStr string) (string, error) {
	if a == nil {
		// Auth disabled: extract no user identity.
		return "", nil
	}
	token, err := jwt.Parse(tokenStr, a.jwks.Keyfunc,
		// PS256 is the default for Heimdall's JWT finalizer.
		jwt.WithValidMethods([]string{"RS256", "ES256", "PS256"}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return "", err
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return "", errors.New("invalid token claims")
	}
	if a.expectedAudience != "" {
		if !audienceMatches(claims["aud"], a.expectedAudience) {
			return "", errors.New("token audience mismatch")
		}
	}
	for _, claim := range []string{"principal", "sub"} {
		if v, ok := claims[claim].(string); ok && v != "" {
			return v, nil
		}
	}
	return "", errors.New("token has no principal or sub claim")
}

// audienceMatches checks whether the JWT aud claim contains expected.
// The claim may be a string or []string per RFC 7519 §4.1.3.
func audienceMatches(claim any, expected string) bool {
	switch v := claim.(type) {
	case string:
		return v == expected
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == expected {
				return true
			}
		}
	case []string:
		for _, s := range v {
			if s == expected {
				return true
			}
		}
	}
	return false
}

// withAuth wraps next with JWT validation. On success the user identity and
// bearer token are placed on the request context.
//
// When the handler's auth validator is nil and RequireUserAuth is false, the
// request proceeds without authentication (local dev mode). A warning was logged
// at startup in that case.
func (h *Handler) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := extractBearer(r)
		if bearer == "" {
			if h.requireUserAuth {
				writeError(r.Context(), w, &authError{msg: "missing bearer token", status: http.StatusUnauthorized})
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		user, err := h.auth.validate(bearer)
		if err != nil {
			if h.requireUserAuth {
				// Log the full JWT library error server-side only; surface a
				// generic message to the client so we don't leak JWKS URLs,
				// key IDs, or other infrastructure details to an
				// unauthenticated caller.
				slog.WarnContext(r.Context(), "token validation failed", "error", err)
				writeError(r.Context(), w, &authError{msg: "invalid token", status: http.StatusUnauthorized})
				return
			}
			// Dev mode: validation failed but auth is non-blocking. Do NOT forward
			// the unvalidated token to upstream services — a downstream call that
			// receives an invalid bearer would fail in confusing ways. Strip the
			// user identity and the bearer.
			slog.WarnContext(r.Context(), "token validation failed; proceeding without auth (dev mode)", "error", err)
			next.ServeHTTP(w, r)
			return
		}

		// Downstream calls from this service go over NATS (no per-request auth
		// context propagated on the wire — same trust model committee-service
		// uses for lfx.projects-api.get_name etc.). We deliberately do NOT
		// attach the bearer to the context: outbound calls don't need it and
		// keeping it off the context prevents future code paths from
		// accidentally forwarding it.
		_ = bearer
		ctx := context.WithValue(r.Context(), userContextKeyValue, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withRequestLog logs every request once it completes, with method, path, status, duration.
func (h *Handler) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		slog.InfoContext(r.Context(), "http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// statusRecorder captures the response status code so the request logger can emit it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the status code before delegating to the wrapped ResponseWriter.
func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// extractBearer pulls the bearer token from the Authorization header.
func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// UserFromContext extracts the authenticated user identifier set by withAuth.
func UserFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(userContextKeyValue).(string); ok {
		return v
	}
	return ""
}

// authError is a transport-level error returned by the auth middleware.
type authError struct {
	msg    string
	status int
}

func (e *authError) Error() string { return e.msg }

// classifyAuthError surfaces an authError with its declared status.
func classifyAuthError(err error) (int, string, bool) {
	var ae *authError
	if errors.As(err, &ae) {
		return ae.status, "unauthorized", true
	}
	return 0, "", false
}
