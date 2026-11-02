// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"net/http"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// ResolveAudience handles POST /projects/{project_uid}/newsletters/{newsletter_uid}/audience.
//
// It starts a background job that reads the committee members and stores them
// as the newsletter's audience snapshot. The response is 202 Accepted with the
// 'processing' audience, and the same response is returned if a job is already
// running. When a snapshot is already ready for the same committee list, it is
// returned with 200 and nothing is resolved again. Clients poll GET .../audience
// until the status is 'ready' or 'failed'.
func (h *Handler) ResolveAudience(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	id, err := parseUUID(r.PathValue("newsletter_uid"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	var body publicapi.ResolveAudienceRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}
	audience, err := h.audience.Resolve(r.Context(), projectUID, id, body.CommitteeUIDs)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	status := http.StatusAccepted
	if audience.Status == model.AudienceReady {
		status = http.StatusOK
	}
	writeJSON(r.Context(), w, status, toAPIAudience(audience))
}

// GetAudience handles GET /projects/{project_uid}/newsletters/{newsletter_uid}/audience.
// It returns 404 when the audience was never resolved.
func (h *Handler) GetAudience(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	id, err := parseUUID(r.PathValue("newsletter_uid"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	audience, err := h.audience.Get(r.Context(), projectUID, id)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	writeJSON(r.Context(), w, http.StatusOK, toAPIAudience(audience))
}

// toAPIAudience converts an audience into the public API DTO.
func toAPIAudience(a *model.Audience) publicapi.AudienceResponse {
	uids := a.CommitteeUIDs
	if uids == nil {
		uids = []string{}
	}
	return publicapi.AudienceResponse{
		Status:         string(a.Status),
		RecipientCount: a.RecipientCount,
		SkippedNoEmail: a.SkippedNoEmail,
		CommitteeUIDs:  uids,
		RequestedAt:    a.RequestedAt,
		ResolvedAt:     a.ResolvedAt,
		Error:          a.Error,
	}
}
