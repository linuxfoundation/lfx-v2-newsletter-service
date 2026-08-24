// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"encoding/json"
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

// CreateNewsletter handles POST /projects/{project_uid}/newsletters.
func (h *Handler) CreateNewsletter(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	var body publicapi.CreateNewsletterRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}

	user := UserFromContext(r.Context())
	if user == "" {
		user = "anonymous"
	}

	publicationID, err := parseRequiredPublicationID(body.PublicationID)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	draft, err := h.newsletter.CreateDraft(r.Context(), service.CreateDraftInput{
		ProjectUID:    projectUID,
		Subject:       body.Subject,
		BodyHTML:      body.BodyHTML,
		EDReplyEmail:  body.EDReplyEmail,
		CommitteeUIDs: body.CommitteeUIDs,
		CreatedBy:     user,
		ScheduledAt:   body.ScheduledAt,
		PublicationID: publicationID,
	})
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	w.Header().Set("ETag", formatETag(draft.Version))
	w.Header().Set("Location", fmt.Sprintf("/projects/%s/newsletters/%s", projectUID, draft.ID))
	writeJSON(r.Context(), w, http.StatusCreated, toAPINewsletter(draft))
}

// GetNewsletter handles GET /projects/{project_uid}/newsletters/{newsletter_uid}.
func (h *Handler) GetNewsletter(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	id, err := parseUUID(r.PathValue("newsletter_uid"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	n, err := h.newsletter.GetNewsletter(r.Context(), projectUID, id)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	w.Header().Set("ETag", formatETag(n.Version))
	writeJSON(r.Context(), w, http.StatusOK, toAPINewsletter(n))
}

// UpdateNewsletter handles PUT /projects/{project_uid}/newsletters/{newsletter_uid} with required If-Match.
func (h *Handler) UpdateNewsletter(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	id, err := parseUUID(r.PathValue("newsletter_uid"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	expectedVersion, err := requireIfMatch(r)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	var body publicapi.UpdateNewsletterRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}

	publicationID, publicationIDSet, err := parseUpdatePublicationID(body.PublicationID)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	updated, err := h.newsletter.UpdateDraft(r.Context(), projectUID, service.UpdateDraftInput{
		ID:               id,
		ExpectedVersion:  expectedVersion,
		Subject:          body.Subject,
		BodyHTML:         body.BodyHTML,
		EDReplyEmail:     body.EDReplyEmail,
		CommitteeUIDs:    body.CommitteeUIDs,
		ScheduledAt:      body.ScheduledAt,
		PublicationID:    publicationID,
		PublicationIDSet: publicationIDSet,
	})
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	w.Header().Set("ETag", formatETag(updated.Version))
	writeJSON(r.Context(), w, http.StatusOK, toAPINewsletter(updated))
}

// DeleteNewsletter handles DELETE /projects/{project_uid}/newsletters/{newsletter_uid}.
func (h *Handler) DeleteNewsletter(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	id, err := parseUUID(r.PathValue("newsletter_uid"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	if err := h.newsletter.DeleteDraft(r.Context(), projectUID, id); err != nil {
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

// parseOptionalPublicationID parses a publication_id used as a LIST FILTER
// (the ?publication_id= query parameter), where absent legitimately means "do
// not filter". It is not used for create or update, where the field identifies
// the edition's owning publication and is required.
func parseOptionalPublicationID(raw *string) (*uuid.UUID, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	id, err := uuid.Parse(strings.TrimSpace(*raw))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid publication_id: %v", domain.ErrInvalidRequest, err)
	}
	return &id, nil
}

// parseRequiredPublicationID converts the create request's publication_id into
// a *uuid.UUID. An edition is always composed inside a publication, so an
// absent, null, or empty value is a client error rather than an implicit
// "attach to the project default" — a project is not given a default
// publication automatically; publications are created explicitly.
func parseRequiredPublicationID(raw *string) (*uuid.UUID, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, fmt.Errorf("%w: publication_id is required: create the publication first, then compose the edition inside it", domain.ErrInvalidRequest)
	}
	id, err := uuid.Parse(strings.TrimSpace(*raw))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid publication_id: %v", domain.ErrInvalidRequest, err)
	}
	return &id, nil
}

// parseUpdatePublicationID decodes the update request's publication_id, which
// is a raw message so the three JSON states stay distinguishable. set is false
// only when the caller omitted the field, which tells the service to preserve
// the edition's current publication. An explicit null or empty string is
// rejected: an edition cannot be unfiled once it belongs to a publication.
//
// A plain *string would collapse "absent" and "null" into nil, so a client
// PUTting without the key would silently unlink the edition.
func parseUpdatePublicationID(raw *json.RawMessage) (*uuid.UUID, bool, error) {
	if raw == nil {
		return nil, false, nil
	}
	if strings.TrimSpace(string(*raw)) == "null" {
		return nil, true, fmt.Errorf("%w: publication_id cannot be null: an edition must belong to a publication", domain.ErrInvalidRequest)
	}
	var s string
	if err := json.Unmarshal(*raw, &s); err != nil {
		return nil, true, fmt.Errorf("%w: invalid publication_id: %v", domain.ErrInvalidRequest, err)
	}
	if strings.TrimSpace(s) == "" {
		return nil, true, fmt.Errorf("%w: publication_id cannot be empty: an edition must belong to a publication", domain.ErrInvalidRequest)
	}
	id, err := uuid.Parse(strings.TrimSpace(s))
	if err != nil {
		return nil, true, fmt.Errorf("%w: invalid publication_id: %v", domain.ErrInvalidRequest, err)
	}
	return &id, true, nil
}

// toAPINewsletter converts a domain model into the public API DTO.
func toAPINewsletter(n *model.Newsletter) *publicapi.Newsletter {
	var publicationID *string
	if n.PublicationID != nil {
		pubIDStr := n.PublicationID.String()
		publicationID = &pubIDStr
	}
	return &publicapi.Newsletter{
		ID:              n.ID.String(),
		ProjectUID:      n.ProjectUID,
		Subject:         n.Subject,
		BodyHTML:        n.BodyHTML,
		EDReplyEmail:    n.EDReplyEmail,
		CommitteeUIDs:   n.CommitteeUIDs,
		Status:          publicapi.Status(n.Status),
		SentAt:          n.SentAt,
		GroupID:         n.GroupID,
		ScheduledAt:     n.ScheduledAt,
		PublicationID:   publicationID,
		TotalRecipients: n.TotalRecipients,
		CreatedBy:       n.CreatedBy,
		Version:         n.Version,
		CreatedAt:       n.CreatedAt,
		UpdatedAt:       n.UpdatedAt,
	}
}
