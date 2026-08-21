// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"net/http"
	"time"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// PublicView handles GET /projects/{project_uid}/newsletters/{newsletter_uid}/public.
//
// This endpoint is *intentionally unauthenticated* — it backs the "View
// Online" link in the email footer, which a recipient opens from their mail
// client with no session. It only ever serves a newsletter whose body has
// already been dispatched to recipients, as decided by
// model.Newsletter.PubliclyViewable. A draft, and a send still holding a future
// release time, return 404 rather than distinguishing "wrong project" from
// "not sent yet", so the permalink can't be used to probe for unpublished
// content.
//
// The check is PubliclyViewable and not status='sent' because the send
// orchestrator embeds this permalink in the email chrome before the
// per-recipient fan-out, and marks the row sent only after the fan-out
// finishes. A recipient at the front of a large fan-out opens the link while
// the row is still 'sending', and must not get a 404 for content already in
// their inbox.
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
	if !n.PubliclyViewable(time.Now().UTC()) {
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
