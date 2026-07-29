// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"net/http"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// ListCommitteeNewsletters handles GET /committees/{committee_uid}/newsletters?page_token=...
//
// Member-facing read path: the Heimdall ruleset authorizes the caller against
// `committee:{committee_uid}#member` before the request reaches this service,
// so reaching the handler means the caller is a current member of the
// committee. Only sent newsletters are returned (enforced in the repository
// query); drafts and in-flight sends stay manager-only.
func (h *Handler) ListCommitteeNewsletters(w http.ResponseWriter, r *http.Request) {
	committeeUID := r.PathValue("committee_uid")
	pageToken := r.URL.Query().Get("page_token")

	page, err := h.newsletter.ListCommitteeNewsletters(r.Context(), service.ListCommitteeNewslettersInput{
		CommitteeUID: committeeUID,
		PageToken:    pageToken,
	})
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	out := publicapi.CommitteeNewsletterListResponse{
		Newsletters:   make([]publicapi.CommitteeNewsletter, 0, len(page.Newsletters)),
		NextPageToken: page.NextPageToken,
	}
	for _, n := range page.Newsletters {
		out.Newsletters = append(out.Newsletters, toAPICommitteeNewsletter(n))
	}
	writeJSON(r.Context(), w, http.StatusOK, out)
}

// toAPICommitteeNewsletter converts a domain Newsletter into the member-facing
// committee list DTO. body_html is intentionally excluded — members fetch the
// rendered body via GET /projects/{project_uid}/newsletters/{newsletter_uid}.
func toAPICommitteeNewsletter(n *model.Newsletter) publicapi.CommitteeNewsletter {
	return publicapi.CommitteeNewsletter{
		ID:            n.ID.String(),
		ProjectUID:    n.ProjectUID,
		Subject:       n.Subject,
		CommitteeUIDs: n.CommitteeUIDs,
		SentAt:        n.SentAt,
	}
}
