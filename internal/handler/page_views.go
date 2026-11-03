// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
)

// totalOpensListConcurrency bounds the per-row provider reads that decorate
// a list page with total_opens.
const totalOpensListConcurrency = 8

// RecordPageView handles POST /projects/{project_uid}/newsletters/{newsletter_uid}/page-views.
//
// Records that the authenticated caller viewed a sent newsletter's web page.
// Idempotent per user, so it always answers 204 on success. A missing or
// anonymous principal is 400 (the gateway already rejects unauthenticated
// callers with 401); drafts, unknown ids, and other projects' newsletters
// are 404. The request takes no body.
func (h *Handler) RecordPageView(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("newsletter_uid"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	if err := h.pageViews.RecordPageView(r.Context(), service.RecordPageViewInput{
		ProjectUID:   r.PathValue("project_uid"),
		NewsletterID: id,
		User:         UserFromContext(r.Context()),
	}); err != nil {
		writeError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pageViewCounts reads unique page-view counts for the sent newsletters in
// the list. Like share counts they are decorative, so a read failure is logged
// and yields nil (the response omits page_view_count).
func (h *Handler) pageViewCounts(ctx context.Context, newsletters []*model.Newsletter) map[uuid.UUID]int {
	if h.pageViews == nil || len(newsletters) == 0 {
		return nil
	}
	counts, err := h.pageViews.PageViewCounts(ctx, newsletters)
	if err != nil {
		slog.WarnContext(ctx, "failed to read newsletter page-view counts; omitting page_view_count", "error", err)
		return nil
	}
	return counts
}

// totalOpensFor reads total_opens for every sent newsletter in a list page,
// with bounded concurrency, using the same rule as the single-newsletter read
// so a list row and the reader show the same number. Entries are nil when the
// total is unknown (unsent, or the read failed); the slice is index-aligned
// with newsletters.
func (h *Handler) totalOpensFor(ctx context.Context, newsletters []*model.Newsletter) []*int {
	out := make([]*int, len(newsletters))
	if h.analytics == nil {
		return out
	}
	var g errgroup.Group
	g.SetLimit(totalOpensListConcurrency)
	for i, n := range newsletters {
		if n.Status != model.StatusSent {
			continue
		}
		g.Go(func() error {
			out[i] = h.totalOpens(ctx, n)
			return nil
		})
	}
	_ = g.Wait() // totalOpens never fails; it logs and yields nil.
	return out
}
