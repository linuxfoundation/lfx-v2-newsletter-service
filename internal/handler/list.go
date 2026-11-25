// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"context"
	"net/http"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
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

	writeNewsletterPage(r.Context(), h, w, page)
}

// writeNewsletterPage writes one page of newsletters as NewsletterListResponse,
// with the share and page-view counts the unified list carries.
func writeNewsletterPage(ctx context.Context, h *Handler, w http.ResponseWriter, page *port.ListPage) {
	out := publicapi.NewsletterListResponse{
		Newsletters:   make([]publicapi.NewsletterListItem, 0, len(page.Newsletters)),
		NextPageToken: page.NextPageToken,
	}
	counts := h.shareCounts(ctx, page.Newsletters)
	views := h.pageViewCounts(ctx, page.Newsletters)
	for _, n := range page.Newsletters {
		item := toAPIListItem(ctx, n)
		item.ShareCount = countFor(counts, n.ID)
		item.PageViewCount = countFor(views, n.ID)
		out.Newsletters = append(out.Newsletters, item)
	}
	writeJSON(ctx, w, http.StatusOK, out)
}

// toAPIListItem converts a domain Newsletter into the list DTO. Engagement
// totals are derived from the persisted row (total_recipients is set at send
// time); per-newsletter analytics (open rate, unique opens) require a separate
// call to /analytics so the list query stays a single DB round-trip.
func toAPIListItem(ctx context.Context, n *model.Newsletter) publicapi.NewsletterListItem {
	// List rows never carry body_layout (see the NewsletterListItem doc):
	// layouts can be ~1 MiB each and pages return up to 100 rows. Shallow-copy
	// the newsletter and clear body_layout on the copy, preserving the original
	// repository-supplied model. This allows toAPINewsletter to convert the copy
	// without mutating the caller's model. The local is named `item`, not `copy`,
	// so it does not shadow the `copy` builtin.
	item := *n
	item.BodyLayout = nil
	return publicapi.NewsletterListItem{Newsletter: *toAPINewsletter(ctx, &item)}
}
