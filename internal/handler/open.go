// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
)

// trackingPixel is a 1x1 transparent GIF (43 bytes). Inlined to avoid a disk
// read per request.
var trackingPixel = mustDecodeBase64("R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7")

// OpenPixel handles GET /newsletter-opens/{id}?r=<recipient_hash>.
//
// This endpoint is *intentionally unauthenticated* — it is requested by the
// recipient's email client, which doesn't carry a session. Recipients are
// identified by the opaque hash embedded in the URL (SHA-256 of the lowercased
// email), so the endpoint never receives or stores raw email addresses.
//
// On success (or on any failure that isn't a clearly malicious request) we
// respond with the transparent pixel so email clients don't surface a broken
// image. We log warnings for genuinely invalid requests but never propagate
// 4xx/5xx to the email client.
func (h *Handler) OpenPixel(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		slog.WarnContext(r.Context(), "open pixel: invalid id", "error", err.Error())
		writePixel(w)
		return
	}

	// Recipient hash is forwarded verbatim. RecordOpenWithHash treats an empty
	// hash as a silent no-op (still 200s with the pixel), so a tracking URL
	// that lost its `r=` query param doesn't error — it just isn't counted.
	recipientHash := r.URL.Query().Get("r")

	if err := h.newsletter.RecordOpenWithHash(r.Context(), id, recipientHash); err != nil {
		// ErrNotFound means the URL was tampered with or the newsletter was
		// deleted; log warn and still serve the pixel.
		if errors.Is(err, domain.ErrNotFound) {
			slog.WarnContext(r.Context(), "open pixel: newsletter not found", "id", id)
		} else {
			slog.ErrorContext(r.Context(), "open pixel: record failed", "id", id, "error", err.Error())
		}
	}

	writePixel(w)
}

// writePixel writes the 1x1 GIF body with caching headers tuned for email
// clients (no caching so each open is counted distinctly per request).
func writePixel(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Content-Length", "43")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(trackingPixel)
}

func mustDecodeBase64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}
