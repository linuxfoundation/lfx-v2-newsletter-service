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
	"io"
	"log/slog"
	"net/http"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Handler is the HTTP handler aggregate that owns the service-layer dependencies
// and exposes registered routes via Routes().
type Handler struct {
	newsletter      *service.NewsletterService
	send            *service.SendOrchestrator
	analytics       *service.AnalyticsService
	unsub           *service.UnsubscribeService
	project         port.ProjectMetadataClient
	db              *sql.DB
	auth            *AuthValidator
	requireUserAuth bool
	// sendgridWebhook is the SendGrid event-webhook handler, registered on a
	// public route when configured (nil otherwise). It self-authenticates via
	// its ECDSA signature check.
	sendgridWebhook http.Handler
}

// Config wires a Handler.
type Config struct {
	Newsletter      *service.NewsletterService
	Send            *service.SendOrchestrator
	Analytics       *service.AnalyticsService
	Unsubscribe     *service.UnsubscribeService
	Project         port.ProjectMetadataClient
	DB              *sql.DB
	Auth            *AuthValidator
	RequireUserAuth bool
	// SendGridWebhook, when non-nil, is registered at POST
	// /newsletters/sendgrid/events (unauthenticated; signature-verified).
	SendGridWebhook http.Handler
}

// New wires a Handler with the given dependencies.
func New(cfg Config) *Handler {
	return &Handler{
		newsletter:      cfg.Newsletter,
		send:            cfg.Send,
		analytics:       cfg.Analytics,
		unsub:           cfg.Unsubscribe,
		project:         cfg.Project,
		db:              cfg.DB,
		auth:            cfg.Auth,
		requireUserAuth: cfg.RequireUserAuth,
		sendgridWebhook: cfg.SendGridWebhook,
	}
}

// projectDisplayName resolves a human-readable project name for use in
// recipient-facing pages, falling back to a generic label when the lookup
// fails so the page always renders.
func (h *Handler) projectDisplayName(ctx context.Context, projectUID string) string {
	if h.project == nil {
		return "this project's"
	}
	name, err := h.project.Name(ctx, projectUID)
	if err != nil || name == "" {
		return "this project's"
	}
	return name
}

// Routes returns a fully-wired http.Handler with all newsletter routes registered.
//
// All authenticated paths are project-scoped (`/projects/{project_uid}/...`)
// so the Heimdall ruleset can gate by `project:{project_uid}` extracted from
// `Request.URL.Captures.project_uid`. Drafts and sent newsletters share one
// resource; status is a field on the row, not a sub-path.
//
// The open-pixel endpoint also carries `project_uid` for path consistency,
// but Heimdall lets it through anonymously — recipients clicking the tracker
// have no session.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	// Health endpoints — no auth.
	mux.HandleFunc("GET /livez", h.Livez)
	mux.HandleFunc("GET /readyz", h.Readyz)

	// Newsletter CRUD — JWT auth via withAuth().
	mux.Handle("POST /projects/{project_uid}/newsletters", h.withAuth(http.HandlerFunc(h.CreateNewsletter)))
	mux.Handle("GET /projects/{project_uid}/newsletters", h.withAuth(http.HandlerFunc(h.ListNewsletters)))
	mux.Handle("GET /projects/{project_uid}/newsletters/{newsletter_uid}", h.withAuth(http.HandlerFunc(h.GetNewsletter)))
	mux.Handle("PUT /projects/{project_uid}/newsletters/{newsletter_uid}", h.withAuth(http.HandlerFunc(h.UpdateNewsletter)))
	mux.Handle("DELETE /projects/{project_uid}/newsletters/{newsletter_uid}", h.withAuth(http.HandlerFunc(h.DeleteNewsletter)))
	mux.Handle("POST /projects/{project_uid}/newsletters/{newsletter_uid}/send", h.withAuth(http.HandlerFunc(h.SendNewsletter)))
	mux.Handle("POST /projects/{project_uid}/newsletters/{newsletter_uid}/schedule", h.withAuth(http.HandlerFunc(h.ScheduleNewsletter)))
	mux.Handle("POST /projects/{project_uid}/newsletters/{newsletter_uid}/cancel-schedule", h.withAuth(http.HandlerFunc(h.CancelScheduleNewsletter)))

	// Committee-scoped newsletter reads — JWT auth. Member-facing: Heimdall
	// gates on `committee:{committee_uid}#member` (not project writer), so
	// committee members can list the sent newsletters addressed to their
	// committee. See ListCommitteeNewsletters.
	mux.Handle("GET /committees/{committee_uid}/newsletters", h.withAuth(http.HandlerFunc(h.ListCommitteeNewsletters)))

	// Recipient resolution + test send — JWT auth.
	mux.Handle("POST /projects/{project_uid}/newsletters/recipient-count", h.withAuth(http.HandlerFunc(h.RecipientCount)))
	mux.Handle("POST /projects/{project_uid}/newsletters/recipients", h.withAuth(http.HandlerFunc(h.Recipients)))
	mux.Handle("POST /projects/{project_uid}/newsletters/test-send", h.withAuth(http.HandlerFunc(h.TestSend)))

	// Per-newsletter analytics — JWT auth.
	mux.Handle("GET /projects/{project_uid}/newsletters/{newsletter_uid}/analytics", h.withAuth(http.HandlerFunc(h.GetAnalytics)))
	mux.Handle("GET /projects/{project_uid}/newsletters/{newsletter_uid}/analytics/recipients", h.withAuth(http.HandlerFunc(h.GetRecipientEngagement)))

	// Newsletter opt-outs — JWT auth.
	mux.Handle("GET /projects/{project_uid}/newsletter-opt-outs", h.withAuth(http.HandlerFunc(h.ListOptOuts)))
	mux.Handle("DELETE /projects/{project_uid}/newsletter-opt-outs/{opt_out_id}", h.withAuth(http.HandlerFunc(h.DeleteOptOut)))

	// Open tracking pixel — intentionally unauthenticated; requested by the
	// recipient's email client which has no session. Identity comes from the
	// hash in the query string.
	mux.HandleFunc("GET /projects/{project_uid}/newsletter-opens/{newsletter_uid}", h.OpenPixel)

	// One-click unsubscribe — intentionally unauthenticated; requested by a
	// recipient clicking the footer link. Authorization comes from the
	// HMAC-signed token in the query string.
	mux.HandleFunc("GET /newsletters/unsubscribe", h.Unsubscribe)

	// SendGrid event webhook — intentionally unauthenticated; SendGrid POSTs
	// engagement events with no session. Authenticity comes from the ECDSA
	// signature the handler verifies. Registered only when configured.
	if h.sendgridWebhook != nil {
		mux.Handle("POST /newsletters/sendgrid/events", h.sendgridWebhook)
	}

	// Outermost middleware first: otelhttp creates a span per request (health
	// probes excluded), then request ID so it appears on every log line,
	// then request log so it captures status + duration.
	// Go 1.22+ ServeMux sets r.Pattern on matched routes so otelhttp names
	// spans after the matched pattern (e.g. GET /projects/{project_uid}/newsletters).
	return otelhttp.NewHandler(
		withRequestID(h.withRequestLog(mux)),
		"newsletter-service",
		otelhttp.WithFilter(func(r *http.Request) bool {
			p := r.URL.Path
			return p != "/livez" && p != "/readyz"
		}),
	)
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
//
// Two error families are recognized:
//
//   - domain sentinel errors (`domain.Err*`), matched with `errors.Is`. These
//     are returned by the repository and service layers for well-known
//     business-logic outcomes.
//   - typed `pkgerrors.*` wrappers (`Validation`, `NotFound`, `Conflict`,
//     `ServiceUnavailable`), matched with `errors.As`. These are returned by
//     the NATS upstream clients (committee, project, email-dispatcher) since
//     they don't have a domain sentinel to wrap.
//
// Domain matches take precedence over the typed wrappers. Anything else falls
// through to 500.
func classifyError(err error) (int, string) {
	if status, code, ok := classifyAuthError(err); ok {
		return status, code
	}
	var (
		svcUnavailable pkgerrors.ServiceUnavailable
		notFound       pkgerrors.NotFound
		validation     pkgerrors.Validation
		conflict       pkgerrors.Conflict
	)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, domain.ErrVersionMismatch):
		return http.StatusPreconditionFailed, "version_mismatch"
	case errors.Is(err, domain.ErrAlreadySent):
		return http.StatusConflict, "already_sent"
	case errors.Is(err, domain.ErrSendInProgress):
		// Distinct code from already_sent so clients can poll the newsletter
		// until the in-flight send settles instead of giving up.
		return http.StatusConflict, "send_in_progress"
	case errors.Is(err, domain.ErrScheduled):
		return http.StatusConflict, "scheduled"
	case errors.Is(err, domain.ErrCancelWindowClosed):
		return http.StatusConflict, "cancel_window_closed"
	case errors.Is(err, domain.ErrInvalidRequest):
		return http.StatusBadRequest, "invalid_request"
	case errors.As(err, &svcUnavailable):
		return http.StatusServiceUnavailable, "service_unavailable"
	case errors.As(err, &notFound):
		return http.StatusNotFound, "not_found"
	case errors.As(err, &validation):
		return http.StatusBadRequest, "invalid_request"
	case errors.As(err, &conflict):
		return http.StatusConflict, "conflict"
	default:
		return http.StatusInternalServerError, "internal_error"
	}
}

// maxRequestBodyBytes caps inbound JSON bodies. Newsletter body_html is the
// largest legitimate field (capped at 100 KiB in the service layer); 1 MiB
// gives generous headroom while bounding the per-request allocation when a
// client streams a hostile payload.
const maxRequestBodyBytes = 1 << 20

// decodeJSON decodes the request body into dst, returning a domain.ErrInvalidRequest
// if the body is missing, malformed, or exceeds maxRequestBodyBytes.
func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return domain.ErrInvalidRequest
	}
	defer func() { _ = r.Body.Close() }()
	r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.Join(domain.ErrInvalidRequest, err)
	}
	return nil
}

// decodeJSONOptional is decodeJSON, but a missing or empty body leaves dst at
// its zero value instead of erroring. Used by POST .../schedule, whose body
// is an optional override — arming a draft's already-saved schedule needs no
// body at all.
func decodeJSONOptional(r *http.Request, dst any) error {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	defer func() { _ = r.Body.Close() }()
	r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if err == io.EOF {
			return nil
		}
		return errors.Join(domain.ErrInvalidRequest, err)
	}
	return nil
}
