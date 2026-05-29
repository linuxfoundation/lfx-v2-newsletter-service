// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"net/http"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// ListNewsletters handles GET /projects/{project_uid}/newsletters?status=...&page_token=...
//
// Returns drafts and sent newsletters paginated by updated_at DESC. When the
// status query param is omitted, both are returned interleaved.
func (h *Handler) ListNewsletters(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	q := r.URL.Query()
	status := model.Status(q.Get("status"))
	pageToken := q.Get("page_token")

	page, err := h.newsletter.ListNewsletters(r.Context(), service.ListNewslettersInput{
		ProjectUID: projectUID,
		Status:     status,
		PageToken:  pageToken,
	})
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	out := publicapi.NewsletterListResponse{
		Newsletters:   make([]publicapi.NewsletterListItem, 0, len(page.Newsletters)),
		NextPageToken: page.NextPageToken,
	}
	for _, n := range page.Newsletters {
		out.Newsletters = append(out.Newsletters, toAPIListItem(n))
	}
	writeJSON(r.Context(), w, http.StatusOK, out)
}

// toAPIListItem converts a domain Newsletter into the list DTO. Engagement
// totals are derived from the persisted row (total_recipients is set at send
// time); per-newsletter analytics (open rate, unique opens) require a separate
// call to /analytics so the list query stays a single DB round-trip.
func toAPIListItem(n *model.Newsletter) publicapi.NewsletterListItem {
	return publicapi.NewsletterListItem{Newsletter: *toAPINewsletter(n)}
}
