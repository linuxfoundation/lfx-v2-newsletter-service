// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"net/http"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// ListNewsletters handles GET /newsletters?contextType=...&contextUid=...&status=...&pageToken=...
//
// Returns drafts and sent newsletters paginated by updated_at DESC. When the
// status query param is omitted, both are returned interleaved.
func (h *Handler) ListNewsletters(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	contextType := model.ContextType(q.Get("contextType"))
	contextUID := q.Get("contextUid")
	status := model.Status(q.Get("status"))
	pageToken := q.Get("pageToken")

	page, err := h.newsletter.ListNewsletters(r.Context(), service.ListNewslettersInput{
		ContextType: contextType,
		ContextUID:  contextUID,
		Status:      status,
		PageToken:   pageToken,
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

// toAPIListItem converts a domain Newsletter into the list DTO, populating
// engagement fields for sent rows (currently delivered/opens are 0 until
// the email pipeline + open tracking are wired into real campaigns).
func toAPIListItem(n *model.Newsletter) publicapi.NewsletterListItem {
	item := publicapi.NewsletterListItem{Newsletter: *toAPINewsletter(n)}
	if n.Status == model.StatusSent {
		total := n.TotalRecipients
		opens := 0
		rate := 0.0
		item.TotalRecipients = &total
		item.UniqueOpens = &opens
		item.OpenRate = &rate
	}
	return item
}
