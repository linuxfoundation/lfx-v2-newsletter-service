// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"net/http"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// PublicView handles GET /projects/{project_uid}/newsletters/{newsletter_uid}/public.
//
// This endpoint is *intentionally unauthenticated* — it backs the "View
// Online" link in the email footer, which a recipient opens from their mail
// client with no session. It only ever serves a newsletter once it has
// reached status='sent': a draft or in-flight send returns 404 rather than
// distinguishing "wrong project" from "not sent yet", so the permalink can't
// be used to probe for unpublished content.
func (h *Handler) PublicView(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projectUID := r.PathValue("project_uid")
	id, err := parseUUID(r.PathValue("newsletter_uid"))
	if err != nil {
		writeError(ctx, w, err)
		return
	}

	n, err := h.newsletter.GetNewsletter(ctx, projectUID, id)
	if err != nil {
		writeError(ctx, w, err)
		return
	}
	if n.Status != model.StatusSent {
		writeError(ctx, w, domain.ErrNotFound)
		return
	}

	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(ctx, w, http.StatusOK, publicapi.PublicNewsletterView{
		Subject:     n.Subject,
		BodyHTML:    n.BodyHTML,
		ProjectName: h.projectDisplayName(ctx, projectUID),
		SentAt:      n.SentAt,
	})
}
