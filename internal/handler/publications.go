// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// CreatePublication handles POST /projects/{project_uid}/newsletter-publications.
func (h *Handler) CreatePublication(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	var body publicapi.CreatePublicationRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}

	user := UserFromContext(r.Context())
	if user == "" {
		user = "anonymous"
	}

	pub, err := h.publication.CreatePublication(r.Context(), service.CreatePublicationInput{
		ProjectUID:     projectUID,
		Slug:           body.Slug,
		Name:           body.Name,
		WrapperContent: body.WrapperContent,
		TemplateSetID:  body.TemplateSetID,
		EditorType:     body.EditorType,
		SenderEmail:    body.SenderEmail,
		ViewOnlineBase: body.ViewOnlineBase,
		CreatedBy:      user,
	})
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	w.Header().Set("ETag", formatETag(pub.Version))
	w.Header().Set("Location", fmt.Sprintf("/projects/%s/newsletter-publications/%s", projectUID, pub.ID))
	writeJSON(r.Context(), w, http.StatusCreated, toAPIPublication(pub))
}

// GetPublication handles GET /projects/{project_uid}/newsletter-publications/{publication_uid}.
func (h *Handler) GetPublication(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	id, err := parseUUID(r.PathValue("publication_uid"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	pub, err := h.publication.GetPublication(r.Context(), projectUID, id)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	w.Header().Set("ETag", formatETag(pub.Version))
	writeJSON(r.Context(), w, http.StatusOK, toAPIPublication(pub))
}

// ListPublications handles GET /projects/{project_uid}/newsletter-publications.
func (h *Handler) ListPublications(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")

	q := r.URL.Query()
	pageToken := q.Get("page_token")

	// page_size is optional. Absent leaves the repository default; a present
	// value is validated here rather than silently dropped, because the
	// consumer already sends it and a silently-ignored page size looks like the
	// server disagreeing about how many rows a page holds. The repository
	// clamps to its own maximum.
	pageSize := 0
	if q.Has("page_size") {
		raw := strings.TrimSpace(q.Get("page_size"))
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(r.Context(), w, fmt.Errorf("%w: page_size must be a positive integer", domain.ErrInvalidRequest))
			return
		}
		pageSize = parsed
	}

	page, err := h.publication.ListPublications(r.Context(), projectUID, pageToken, pageSize)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	apiPubs := make([]publicapi.NewsletterPublication, 0, len(page.Publications))
	for _, pub := range page.Publications {
		apiPubs = append(apiPubs, *toAPIPublication(pub))
	}

	writeJSON(r.Context(), w, http.StatusOK, publicapi.PublicationListResponse{
		Publications:  apiPubs,
		NextPageToken: page.NextPageToken,
	})
}

// UpdatePublication handles PUT /projects/{project_uid}/newsletter-publications/{publication_uid} with required If-Match.
func (h *Handler) UpdatePublication(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	id, err := parseUUID(r.PathValue("publication_uid"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	expectedVersion, err := requireIfMatch(r)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	var body publicapi.UpdatePublicationRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}

	pub, err := h.publication.UpdatePublication(r.Context(), projectUID, id, expectedVersion, service.UpdatePublicationInput{
		Name:           body.Name,
		WrapperContent: body.WrapperContent,
		TemplateSetID:  body.TemplateSetID,
		EditorType:     body.EditorType,
		SenderEmail:    body.SenderEmail,
		ViewOnlineBase: body.ViewOnlineBase,
	})
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	w.Header().Set("ETag", formatETag(pub.Version))
	writeJSON(r.Context(), w, http.StatusOK, toAPIPublication(pub))
}

// NOTE: Publication deletion is not implemented (LFXV2-2582). The composite
// foreign key on newsletters(project_uid, publication_id) with ON DELETE RESTRICT
// prevents deletion of publications with linked editions; removing a publication
// requires first unlinking or deleting all its editions, which is deferred to a
// follow-up ticket. The FK constraint makes this safe: any attempt to delete a
// publication with editions will fail at the database layer.

// toAPIPublication converts a domain model into the public API DTO.
func toAPIPublication(pub *model.NewsletterPublication) *publicapi.NewsletterPublication {
	var wrapperContent any
	if len(pub.WrapperContent) > 0 && strings.TrimSpace(string(pub.WrapperContent)) != "" {
		if err := json.Unmarshal(pub.WrapperContent, &wrapperContent); err != nil {
			// If unmarshaling fails, return as nil (shouldn't happen in normal operation)
			wrapperContent = nil
		}
	}

	return &publicapi.NewsletterPublication{
		ID:             pub.ID.String(),
		ProjectUID:     pub.ProjectUID,
		Slug:           pub.Slug,
		Name:           pub.Name,
		IsDefault:      pub.IsDefault,
		WrapperContent: wrapperContent,
		TemplateSetID:  pub.TemplateSetID,
		EditorType:     pub.EditorType,
		SenderEmail:    pub.SenderEmail,
		ViewOnlineBase: pub.ViewOnlineBase,
		CreatedBy:      pub.CreatedBy,
		Version:        pub.Version,
		CreatedAt:      pub.CreatedAt,
		UpdatedAt:      pub.UpdatedAt,
	}
}
