// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// ShareNewsletter handles POST /projects/{project_uid}/newsletters/{newsletter_uid}/shares.
//
// Records that the authenticated caller shared a sent newsletter to a
// channel. Idempotent per (user, channel), so it always answers 204 on
// success. Unknown channels and missing or anonymous principals are 400
// (the gateway already rejects unauthenticated callers with 401); drafts,
// unknown ids, and other projects' newsletters are 404.
func (h *Handler) ShareNewsletter(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	id, err := parseUUID(r.PathValue("newsletter_uid"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	var body publicapi.ShareNewsletterRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}

	if err := h.shares.RecordShare(r.Context(), service.RecordShareInput{
		ProjectUID:   projectUID,
		NewsletterID: id,
		User:         UserFromContext(r.Context()),
		Channel:      model.ShareChannel(body.Channel),
	}); err != nil {
		writeError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// shareCounts reads share counts for the sent newsletters in the list. Share
// counts are decorative, so a read failure is logged and yields nil (the
// response omits share_count) instead of failing the whole request.
func (h *Handler) shareCounts(ctx context.Context, newsletters []*model.Newsletter) map[uuid.UUID]int {
	if h.shares == nil || len(newsletters) == 0 {
		return nil
	}
	counts, err := h.shares.ShareCounts(ctx, newsletters)
	if err != nil {
		slog.WarnContext(ctx, "failed to read newsletter share counts; omitting share_count", "error", err)
		return nil
	}
	return counts
}

// totalOpens reads a sent newsletter's raw open total for the single-newsletter
// read. Like share counts it is decorative: nil (omitted) for unsent
// newsletters or when the local read fails, never an error response.
func (h *Handler) totalOpens(ctx context.Context, n *model.Newsletter) *int {
	if h.analytics == nil {
		return nil
	}
	total, ok, err := h.analytics.TotalOpens(ctx, n)
	if err != nil {
		slog.WarnContext(ctx, "failed to read newsletter total opens; omitting total_opens", "newsletter_id", n.ID, "error", err)
		return nil
	}
	if !ok {
		return nil
	}
	return &total
}

// shareCountFor returns a pointer to the newsletter's share count, or nil
// when no count was read for it (unsent, or the read failed).
func shareCountFor(counts map[uuid.UUID]int, id uuid.UUID) *int {
	count, ok := counts[id]
	if !ok {
		return nil
	}
	return &count
}

// applyShareStats sets the analytics DTO's share fields. A read failure is
// logged and leaves them omitted rather than failing the analytics response.
func (h *Handler) applyShareStats(ctx context.Context, newsletterID uuid.UUID, dto *publicapi.NewsletterAnalytics) {
	if h.shares == nil {
		return
	}
	stats, err := h.shares.ShareStats(ctx, newsletterID)
	if err != nil {
		slog.WarnContext(ctx, "failed to read newsletter share stats; omitting shares", "newsletter_id", newsletterID, "error", err)
		return
	}
	byChannel := make(map[string]int, len(model.ShareChannels))
	for _, channel := range model.ShareChannels {
		byChannel[string(channel)] = stats.ByChannel[channel]
	}
	total := stats.Total
	dto.TotalShares = &total
	dto.SharesByChannel = byChannel
}
