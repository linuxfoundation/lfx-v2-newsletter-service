// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"fmt"
	"net/http"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// CreatePublication handles POST /projects/{project_uid}/newsletter-publications.
func (h *Handler) CreatePublication(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	var body publicapi.PublicationRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}
	user := UserFromContext(r.Context())
	if user == "" {
		user = "anonymous"
	}
	p, err := h.publications.CreatePublication(r.Context(), projectUID, user, toPublicationInput(body))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	w.Header().Set("ETag", formatETag(p.Version))
	w.Header().Set("Location", fmt.Sprintf("/projects/%s/newsletter-publications/%s", projectUID, p.ID))
	writeJSON(r.Context(), w, http.StatusCreated, toAPIPublication(p))
}

// ListPublications handles GET /projects/{project_uid}/newsletter-publications?page_token=...
func (h *Handler) ListPublications(w http.ResponseWriter, r *http.Request) {
	page, err := h.publications.ListPublications(r.Context(), r.PathValue("project_uid"), r.URL.Query().Get("page_token"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	out := publicapi.PublicationListResponse{
		Publications:  make([]publicapi.Publication, 0, len(page.Publications)),
		NextPageToken: page.NextPageToken,
	}
	for _, p := range page.Publications {
		out.Publications = append(out.Publications, *toAPIPublication(p))
	}
	writeJSON(r.Context(), w, http.StatusOK, out)
}

// GetPublication handles GET /projects/{project_uid}/newsletter-publications/{publication_uid}.
func (h *Handler) GetPublication(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("publication_uid"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	p, err := h.publications.GetPublication(r.Context(), r.PathValue("project_uid"), id)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	w.Header().Set("ETag", formatETag(p.Version))
	writeJSON(r.Context(), w, http.StatusOK, toAPIPublication(p))
}

// UpdatePublication handles PUT /projects/{project_uid}/newsletter-publications/{publication_uid}
// with required If-Match.
func (h *Handler) UpdatePublication(w http.ResponseWriter, r *http.Request) {
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
	var body publicapi.PublicationRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}
	p, err := h.publications.UpdatePublication(r.Context(), r.PathValue("project_uid"), id, expectedVersion, toPublicationInput(body))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	w.Header().Set("ETag", formatETag(p.Version))
	writeJSON(r.Context(), w, http.StatusOK, toAPIPublication(p))
}

// ListEditions handles GET .../newsletter-publications/{publication_uid}/editions?status=...&page_token=...
func (h *Handler) ListEditions(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("publication_uid"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	q := r.URL.Query()
	page, err := h.publications.ListEditions(r.Context(), service.ListEditionsInput{
		ProjectUID:     r.PathValue("project_uid"),
		PublicationUID: id,
		Status:         model.Status(q.Get("status")),
		PageToken:      q.Get("page_token"),
	})
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	writeNewsletterPage(r.Context(), h, w, page)
}

// CreateEdition handles POST .../newsletter-publications/{publication_uid}/editions:
// the create-draft flow with the publication taken from the path. A
// publication_uid in the body must match the path.
func (h *Handler) CreateEdition(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("publication_uid"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	var body publicapi.CreateNewsletterRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}
	if body.PublicationUID != nil {
		bodyID, err := parseUUID(*body.PublicationUID)
		if err != nil {
			writeError(r.Context(), w, err)
			return
		}
		if bodyID != id {
			writeError(r.Context(), w, fmt.Errorf("%w: publication_uid in the body does not match the path", domain.ErrInvalidRequest))
			return
		}
	}
	h.createDraft(w, r, body, &id)
}

func toPublicationInput(body publicapi.PublicationRequest) service.PublicationInput {
	return service.PublicationInput{
		Name:          body.Name,
		TemplateKey:   body.TemplateKey,
		FromName:      body.FromName,
		ReplyToEmail:  body.ReplyToEmail,
		CommitteeUIDs: body.CommitteeUIDs,
	}
}

func toAPIPublication(p *model.Publication) *publicapi.Publication {
	return &publicapi.Publication{
		ID:            p.ID.String(),
		ProjectUID:    p.ProjectUID,
		Name:          p.Name,
		TemplateKey:   p.TemplateKey,
		FromName:      p.FromName,
		ReplyToEmail:  p.ReplyToEmail,
		CommitteeUIDs: p.CommitteeUIDs,
		Implicit:      p.Implicit,
		CreatedBy:     p.CreatedBy,
		Version:       p.Version,
		CreatedAt:     p.CreatedAt,
		UpdatedAt:     p.UpdatedAt,
	}
}
