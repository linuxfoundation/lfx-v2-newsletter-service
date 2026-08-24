// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"errors"
	"html"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
)

// UnsubscribeConfirm handles GET /newsletters/unsubscribe?t=<token>.
//
// This endpoint is *intentionally unauthenticated* — it is requested by a
// newsletter recipient clicking the footer link in their mail client, which
// has no session. Authorization comes from the HMAC-signed token: only
// someone who received the email (or this service) can produce a valid
// token for a given (project_uid, email) pair.
//
// GET never mutates state. It only verifies the token and renders a
// confirmation page with a form that POSTs back to this same path to
// complete the unsubscribe. This two-stage split exists because link
// checkers, security scanners, and mail-client preview engines routinely
// GET links in email bodies; if GET mutated state, every preview would
// silently unsubscribe the recipient.
//
// Always returns text/html so the browser renders a confirmation rather
// than offering a JSON download.
func (h *Handler) UnsubscribeConfirm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Go's net/http ServeMux routes HEAD to a GET-only pattern. Treat it as
	// a no-op for the same reason GET itself never mutates.
	if r.Method == http.MethodHead {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		return
	}

	token := r.URL.Query().Get("t")

	if h.unsub == nil {
		slog.ErrorContext(ctx, "unsubscribe: service not configured")
		writeUnsubscribeHTML(w, http.StatusInternalServerError, "Unsubscribe unavailable", "Unsubscribe is not configured on this server.")
		return
	}

	projectUID, email, err := h.unsub.VerifyToken(token)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidRequest) {
			slog.WarnContext(ctx, "unsubscribe: invalid token", "error", err.Error())
			writeUnsubscribeHTML(w, http.StatusBadRequest, "Invalid link", "This unsubscribe link is invalid.")
			return
		}
		slog.ErrorContext(ctx, "unsubscribe: failed", "error", err.Error())
		writeUnsubscribeHTML(w, http.StatusInternalServerError, "Something went wrong", "We couldn't process your request. Please try again later.")
		return
	}

	displayName := h.projectDisplayName(ctx, projectUID)
	// Query-only relative action: the browser resolves it against the URL that
	// delivered this page, so a confirmation reached at a path-prefixed public
	// URL submits back to that same endpoint rather than the origin root (see
	// NewUnsubscribeService's optional path-prefix behavior).
	actionURL := "?t=" + url.QueryEscape(token)
	body := "Confirm that " + html.EscapeString(email) + " should stop receiving " + html.EscapeString(displayName) + " newsletters." +
		`<form method="POST" action="` + html.EscapeString(actionURL) + `" style="margin-top:24px;">` +
		`<button type="submit" style="font-size:15px;padding:10px 20px;background:#3B82F6;color:#fff;border:none;border-radius:6px;cursor:pointer;">Unsubscribe</button>` +
		`</form>`
	writeUnsubscribeHTML(w, http.StatusOK, "Confirm unsubscribe", body)
}

// UnsubscribeSubmit handles POST /newsletters/unsubscribe?t=<token>.
//
// This endpoint is *intentionally unauthenticated*, for the same reason as
// UnsubscribeConfirm: authorization comes from the HMAC-signed token, not a
// session. It is reached either by the confirmation form UnsubscribeConfirm
// renders, or directly by a mail client performing an RFC 8058 one-click
// unsubscribe POST. This is the only path that mutates unsubscribe state.
func (h *Handler) UnsubscribeSubmit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	token := r.URL.Query().Get("t")

	if h.unsub == nil {
		slog.ErrorContext(ctx, "unsubscribe: service not configured")
		writeUnsubscribeHTML(w, http.StatusInternalServerError, "Unsubscribe unavailable", "Unsubscribe is not configured on this server.")
		return
	}

	projectUID, email, err := h.unsub.Unsubscribe(ctx, token)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidRequest) {
			slog.WarnContext(ctx, "unsubscribe: invalid token", "error", err.Error())
			writeUnsubscribeHTML(w, http.StatusBadRequest, "Invalid link", "This unsubscribe link is invalid.")
			return
		}
		slog.ErrorContext(ctx, "unsubscribe: failed", "error", err.Error())
		writeUnsubscribeHTML(w, http.StatusInternalServerError, "Something went wrong", "We couldn't process your request. Please try again later.")
		return
	}

	displayName := h.projectDisplayName(ctx, projectUID)
	writeUnsubscribeHTML(w, http.StatusOK, "You're unsubscribed",
		html.EscapeString(email)+" will no longer receive "+html.EscapeString(displayName)+" newsletters.")
}

// writeUnsubscribeHTML writes a minimal self-contained confirmation page.
// Both heading and body must already be HTML-safe.
func writeUnsubscribeHTML(w http.ResponseWriter, status int, heading, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` + heading + `</title></head>
<body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;max-width:560px;margin:48px auto;padding:0 16px;color:#1F2937;">
<h1 style="font-size:22px;">` + heading + `</h1>
<p style="font-size:15px;line-height:1.6;color:#4B5563;">` + body + `</p>
<p style="font-size:12px;color:#9CA3AF;margin-top:32px;">Delivered by <strong style="color:#3B82F6;">LFX</strong></p>
</body></html>`))
}
