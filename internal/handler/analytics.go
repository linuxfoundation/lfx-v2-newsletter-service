// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"net/http"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// GetAnalytics handles GET /projects/{project_uid}/newsletters/{newsletter_uid}/analytics.
//
// Returns ErrNotFound if the newsletter doesn't exist or belongs to a different
// project than the one supplied. Returns zero engagement metrics (not an error)
// when the newsletter exists but has no opens recorded.
func (h *Handler) GetAnalytics(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	id, err := parseUUID(r.PathValue("newsletter_uid"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	a, err := h.analytics.Get(r.Context(), projectUID, id)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	writeJSON(r.Context(), w, http.StatusOK, toAPIAnalytics(a))
}

// toAPIAnalytics converts the domain Analytics aggregate into the public DTO.
func toAPIAnalytics(a *model.Analytics) publicapi.NewsletterAnalytics {
	daily := make([]publicapi.NewsletterDailyOpens, 0, len(a.DailyOpens))
	for _, d := range a.DailyOpens {
		daily = append(daily, publicapi.NewsletterDailyOpens{
			Date:        d.Date.UTC().Format("2006-01-02"),
			Opens:       d.Opens,
			UniqueOpens: d.UniqueOpens,
		})
	}
	return publicapi.NewsletterAnalytics{
		NewsletterID:    a.NewsletterID.String(),
		Subject:         a.Subject,
		Status:          publicapi.Status(a.Status),
		SentAt:          a.SentAt,
		TotalRecipients: a.TotalRecipients,
		Delivered:       a.Delivered,
		Failed:          a.Failed,
		TotalOpens:      a.TotalOpens,
		UniqueOpens:     a.UniqueOpens,
		OpenRate:        a.OpenRate,
		DailyOpens:      daily,
		LastEventAt:     a.LastEventAt,
	}
}
