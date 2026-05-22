// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package handler exposes the HTTP API for the newsletter service. It
// translates between transport (HTTP/JSON) and the service-layer business
// logic, and is the only layer that knows about HTTP status codes.
package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
)

// Handler is the HTTP handler aggregate that owns the service-layer dependencies
// and exposes registered routes via Routes().
type Handler struct {
	newsletter      *service.NewsletterService
	send            *service.SendOrchestrator
	db              *sql.DB
	auth            *AuthValidator
	requireUserAuth bool
}

// Config wires a Handler.
type Config struct {
	Newsletter      *service.NewsletterService
	Send            *service.SendOrchestrator
	DB              *sql.DB
	Auth            *AuthValidator
	RequireUserAuth bool
}

// New wires a Handler with the given dependencies.
func New(cfg Config) *Handler {
	return &Handler{
		newsletter:      cfg.Newsletter,
		send:            cfg.Send,
		db:              cfg.DB,
		auth:            cfg.Auth,
		requireUserAuth: cfg.RequireUserAuth,
	}
}

// Routes returns a fully-wired http.Handler with all newsletter routes registered.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	// Health endpoints — no auth.
	mux.HandleFunc("GET /livez", h.Livez)
	mux.HandleFunc("GET /readyz", h.Readyz)

	// Newsletter endpoints — JWT auth via withAuth().
	mux.Handle("POST /newsletters/drafts", h.withAuth(http.HandlerFunc(h.CreateDraft)))
	mux.Handle("GET /newsletters/drafts", h.withAuth(http.HandlerFunc(h.ListDrafts)))
	mux.Handle("GET /newsletters/drafts/{id}", h.withAuth(http.HandlerFunc(h.GetDraft)))
	mux.Handle("PUT /newsletters/drafts/{id}", h.withAuth(http.HandlerFunc(h.UpdateDraft)))
	mux.Handle("DELETE /newsletters/drafts/{id}", h.withAuth(http.HandlerFunc(h.DeleteDraft)))
	mux.Handle("POST /newsletters/drafts/{id}/send", h.withAuth(http.HandlerFunc(h.SendDraft)))

	mux.Handle("POST /newsletters/recipient-count", h.withAuth(http.HandlerFunc(h.RecipientCount)))
	mux.Handle("POST /newsletters/recipients", h.withAuth(http.HandlerFunc(h.Recipients)))
	mux.Handle("POST /newsletters/test-send", h.withAuth(http.HandlerFunc(h.TestSend)))

	// Unified list + per-newsletter analytics — JWT auth.
	// Note: paths intentionally avoid the /newsletters/{id}/... shape because
	// Go's mux flags it as ambiguous with the existing /newsletters/drafts/{id}
	// pattern (drafts could be treated as {id} or as a literal segment).
	mux.Handle("GET /newsletters", h.withAuth(http.HandlerFunc(h.ListNewsletters)))
	mux.Handle("GET /newsletter-analytics/{id}", h.withAuth(http.HandlerFunc(h.GetAnalytics)))

	// Open tracking pixel — intentionally unauthenticated; requested by the
	// recipient's email client which has no session. Identity comes from the
	// hash in the query string.
	mux.HandleFunc("GET /newsletter-opens/{id}", h.OpenPixel)

	// Outermost middleware first: request ID so it appears on every log line,
	// then request log so it captures status + duration.
	return withRequestID(h.withRequestLog(mux))
}

// writeJSON serializes v as JSON and writes the given status. Errors are logged
// but not propagated; once headers are written there is no recovery.
func writeJSON(ctx context.Context, w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.ErrorContext(ctx, "failed to encode response", "error", err)
	}
}

// errorPayload is the JSON shape returned for error responses.
type errorPayload struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// writeError maps a domain error to an HTTP status code + JSON body.
//
// For 5xx responses we deliberately do not echo err.Error() to the client —
// internal errors (DB failures, upstream responses) can carry infrastructure
// details. The full error is still logged server-side at the warn level.
func writeError(ctx context.Context, w http.ResponseWriter, err error) {
	status, code := classifyError(err)
	slog.WarnContext(ctx, "request failed",
		"status", status,
		"code", code,
		"error", err.Error(),
	)
	message := err.Error()
	if status >= http.StatusInternalServerError {
		message = "internal server error"
	}
	writeJSON(ctx, w, status, errorPayload{
		Error:   code,
		Message: message,
	})
}

// classifyError maps an error to an HTTP status code and short error code.
func classifyError(err error) (int, string) {
	if status, code, ok := classifyAuthError(err); ok {
		return status, code
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, domain.ErrVersionMismatch):
		return http.StatusPreconditionFailed, "version_mismatch"
	case errors.Is(err, domain.ErrAlreadySent):
		return http.StatusConflict, "already_sent"
	case errors.Is(err, domain.ErrInvalidRequest):
		return http.StatusBadRequest, "invalid_request"
	default:
		return http.StatusInternalServerError, "internal_error"
	}
}

// decodeJSON decodes the request body into dst, returning a domain.ErrInvalidRequest
// if the body is missing or malformed.
func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return domain.ErrInvalidRequest
	}
	defer func() { _ = r.Body.Close() }()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.Join(domain.ErrInvalidRequest, err)
	}
	return nil
}
