// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"errors"
	"html"
	"log/slog"
	"net/http"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
)

// Unsubscribe handles GET /newsletters/unsubscribe?t=<token>.
//
// This endpoint is *intentionally unauthenticated* — it is requested by a
// newsletter recipient clicking the footer link in their mail client, which
// has no session. Authorization comes from the HMAC-signed token: only
// someone who received the email (or this service) can produce a valid
// token for a given (project_uid, email) pair.
//
// Always returns text/html so the browser renders a confirmation rather
// than offering a JSON download.
func (h *Handler) Unsubscribe(w http.ResponseWriter, r *http.Request) {
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
			writeUnsubscribeHTML(w, http.StatusBadRequest, "Invalid link", "This unsubscribe link is invalid or has expired.")
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
