// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"io"
	"net/http"
	"strconv"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// UploadImage handles POST /projects/{project_uid}/newsletters/images.
// Accepts raw binary image bytes with Content-Type header specifying the format.
func (h *Handler) UploadImage(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	user := UserFromContext(r.Context())
	if user == "" {
		user = "anonymous"
	}

	// Cap the request body to the configured image max bytes.
	// This is the first defense before the pipeline runs.
	r.Body = http.MaxBytesReader(nil, r.Body, h.imageMaxBytes)
	defer func() { _ = r.Body.Close() }()

	// Read the request body.
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(r.Context(), w, domain.ErrInvalidRequest)
		return
	}

	// Get the declared content type from the Content-Type header.
	declaredContentType := r.Header.Get("Content-Type")
	if declaredContentType == "" {
		writeError(r.Context(), w, domain.ErrInvalidRequest)
		return
	}

	// Upload the image via the service.
	img, err := h.images.UploadImage(r.Context(), projectUID, user, data, declaredContentType)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	// Build the public URL. For now, always use the service's own download route;
	// CDN URL support would be added at the S3 client layer.
	publicURL := h.publicBaseURL + "/projects/" + projectUID + "/newsletters/images/" + img.Hash

	// Return 201 Created with the image metadata.
	writeJSON(r.Context(), w, http.StatusCreated, publicapi.UploadImageResponse{
		Hash:        img.Hash,
		ContentType: img.ContentType,
		Width:       img.Width,
		Height:      img.Height,
		ByteSize:    img.ByteSize,
		PublicURL:   publicURL,
	})
}

// DownloadImage handles GET /projects/{project_uid}/newsletters/images/{hash}.
// This route is intentionally unauthenticated so email clients (no session) can
// load images embedded in newsletters. Returns the image bytes with appropriate
// headers for caching.
func (h *Handler) DownloadImage(w http.ResponseWriter, r *http.Request) {
	_ = r.PathValue("project_uid") // Extracted for consistency but not used; authorization is implicit in the image's persistence
	hash := r.PathValue("hash")

	// Retrieve the image bytes.
	data, contentType, err := h.images.GetImage(r.Context(), hash)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	// Content-addressed storage: cache forever since content never changes.
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		// Error writing to client; already logged by the HTTP server.
	}
}
