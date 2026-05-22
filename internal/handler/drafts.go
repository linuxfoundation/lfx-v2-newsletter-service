// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// CreateDraft handles POST /newsletters/drafts.
func (h *Handler) CreateDraft(w http.ResponseWriter, r *http.Request) {
	var body publicapi.CreateDraftRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}

	user := UserFromContext(r.Context())
	if user == "" {
		user = "anonymous"
	}

	draft, err := h.newsletter.CreateDraft(r.Context(), service.CreateDraftInput{
		ContextType:   model.ContextType(body.ContextType),
		ContextUID:    body.ContextUID,
		Subject:       body.Subject,
		BodyHTML:      body.BodyHTML,
		EDReplyEmail:  body.EDReplyEmail,
		CommitteeUIDs: body.CommitteeUIDs,
		CreatedBy:     user,
	})
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	w.Header().Set("ETag", formatETag(draft.Version))
	w.Header().Set("Location", "/newsletters/drafts/"+draft.ID.String())
	writeJSON(r.Context(), w, http.StatusCreated, toAPINewsletter(draft))
}

// GetDraft handles GET /newsletters/drafts/{id}.
func (h *Handler) GetDraft(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	draft, err := h.newsletter.GetDraft(r.Context(), id)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	w.Header().Set("ETag", formatETag(draft.Version))
	writeJSON(r.Context(), w, http.StatusOK, toAPINewsletter(draft))
}

// ListDrafts handles GET /newsletters/drafts?contextType=...&contextUid=....
func (h *Handler) ListDrafts(w http.ResponseWriter, r *http.Request) {
	contextType := model.ContextType(r.URL.Query().Get("contextType"))
	contextUID := r.URL.Query().Get("contextUid")

	drafts, err := h.newsletter.ListDrafts(r.Context(), contextType, contextUID)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	resp := publicapi.ListDraftsResponse{Drafts: make([]publicapi.Newsletter, 0, len(drafts))}
	for _, d := range drafts {
		resp.Drafts = append(resp.Drafts, *toAPINewsletter(d))
	}
	writeJSON(r.Context(), w, http.StatusOK, resp)
}

// UpdateDraft handles PUT /newsletters/drafts/{id} with required If-Match.
func (h *Handler) UpdateDraft(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	expectedVersion, err := requireIfMatch(r)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	var body publicapi.UpdateDraftRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}

	updated, err := h.newsletter.UpdateDraft(r.Context(), service.UpdateDraftInput{
		ID:              id,
		ExpectedVersion: expectedVersion,
		Subject:         body.Subject,
		BodyHTML:        body.BodyHTML,
		EDReplyEmail:    body.EDReplyEmail,
		CommitteeUIDs:   body.CommitteeUIDs,
	})
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	w.Header().Set("ETag", formatETag(updated.Version))
	writeJSON(r.Context(), w, http.StatusOK, toAPINewsletter(updated))
}

// DeleteDraft handles DELETE /newsletters/drafts/{id}.
func (h *Handler) DeleteDraft(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	if err := h.newsletter.DeleteDraft(r.Context(), id); err != nil {
		writeError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// formatETag formats a version number as a strong ETag value.
func formatETag(version int64) string {
	return fmt.Sprintf("\"%d\"", version)
}

// requireIfMatch parses the If-Match header into a version integer. Missing or
// malformed headers return domain.ErrInvalidRequest.
func requireIfMatch(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		return 0, fmt.Errorf("%w: If-Match header is required", domain.ErrInvalidRequest)
	}
	// Strip optional quotes and W/ weak prefix.
	raw = strings.TrimPrefix(raw, "W/")
	raw = strings.Trim(raw, "\"")
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: If-Match is not a valid version: %v", domain.ErrInvalidRequest, err)
	}
	return v, nil
}

// parseUUID parses a path parameter into a UUID. Malformed input yields ErrInvalidRequest.
func parseUUID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: invalid uuid: %v", domain.ErrInvalidRequest, err)
	}
	return id, nil
}

// toAPINewsletter converts a domain model into the public API DTO.
func toAPINewsletter(n *model.Newsletter) *publicapi.Newsletter {
	return &publicapi.Newsletter{
		ID:            n.ID.String(),
		ContextType:   publicapi.ContextType(n.ContextType),
		ContextUID:    n.ContextUID,
		Subject:       n.Subject,
		BodyHTML:      n.BodyHTML,
		EDReplyEmail:  n.EDReplyEmail,
		CommitteeUIDs: n.CommitteeUIDs,
		Status:        publicapi.Status(n.Status),
		SentAt:        n.SentAt,
		CreatedBy:     n.CreatedBy,
		Version:       n.Version,
		CreatedAt:     n.CreatedAt,
		UpdatedAt:     n.UpdatedAt,
	}
}
