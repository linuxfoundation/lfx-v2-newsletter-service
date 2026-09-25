// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"net/http"

	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// ListOptOuts handles GET /projects/{project_uid}/newsletter-opt-outs.
//
// Returns the full list of email addresses that have unsubscribed from
// newsletters for this project, ordered by unsubscribed_at DESC. Opt-out
// volumes are small, so v1 intentionally has no pagination.
func (h *Handler) ListOptOuts(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")

	unsubscribes, err := h.unsub.ListOptOuts(r.Context(), projectUID)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	out := publicapi.OptOutListResponse{
		OptOuts: make([]publicapi.OptOut, 0, len(unsubscribes)),
	}
	for _, u := range unsubscribes {
		out.OptOuts = append(out.OptOuts, publicapi.OptOut{
			ID:             u.ID.String(),
			Email:          u.Email,
			UnsubscribedAt: u.CreatedAt,
		})
	}
	writeJSON(r.Context(), w, http.StatusOK, out)
}

// DeleteOptOut handles DELETE /projects/{project_uid}/newsletter-opt-outs/{opt_out_id}.
//
// Deletes a single opt-out entry. Returns 204 No Content on success.
// Returns 404 for unknown id or project mismatch, 400 for a malformed UUID.
func (h *Handler) DeleteOptOut(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	id, err := parseUUID(r.PathValue("opt_out_id"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	if err := h.unsub.DeleteOptOut(r.Context(), projectUID, id); err != nil {
		writeError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
