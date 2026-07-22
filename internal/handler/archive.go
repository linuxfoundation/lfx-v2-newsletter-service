// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// ArchiveNewsletters handles GET /newsletters/archive?committee_uids=<csv>&page_token=...
//
// Returns a page of sent newsletters the authenticated caller has access to,
// filtered to newsletters sent to at least one committee they belong to.
// Authorization is service-layer: membership is verified via committee-service.
// Empty verified committees returns 200 with empty list.
func (h *Handler) ArchiveNewsletters(w http.ResponseWriter, r *http.Request) {
	principal := UserFromContext(r.Context())
	if principal == "" {
		writeError(r.Context(), w, &authError{msg: "missing user principal", status: http.StatusUnauthorized})
		return
	}

	// Parse and normalize committee_uids CSV from query string.
	raw := parseCommitteeUIDs(r.URL.Query().Get("committee_uids"))
	committeeUIDs := normalizeCommitteeUIDs(raw)

	// Check bounds: max 50 committees per request (mirrors draft audience limit).
	const maxCommitteesPerRequest = 50
	if len(committeeUIDs) > maxCommitteesPerRequest {
		writeError(r.Context(), w, fmt.Errorf("%w: at most %d committee_uids allowed", domain.ErrInvalidRequest, maxCommitteesPerRequest))
		return
	}

	pageToken := r.URL.Query().Get("page_token")

	out, err := h.archive.ListArchive(r.Context(), service.ListArchiveInput{
		Principal:     principal,
		CommitteeUIDs: committeeUIDs,
		PageToken:     pageToken,
	})
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	resp := publicapi.ArchiveNewsletterListResponse{
		Newsletters:   make([]publicapi.ArchiveNewsletterListItem, 0, len(out.Newsletters)),
		NextPageToken: out.NextPageToken,
	}
	for _, n := range out.Newsletters {
		sentAt := time.Time{}
		if n.SentAt != nil {
			sentAt = *n.SentAt
		}
		resp.Newsletters = append(resp.Newsletters, publicapi.ArchiveNewsletterListItem{
			ID:            n.ID.String(),
			ProjectUID:    n.ProjectUID,
			Subject:       n.Subject,
			SentAt:        sentAt,
			CommitteeUIDs: n.CommitteeUIDs,
			Status:        publicapi.Status(n.Status),
		})
	}
	writeJSON(r.Context(), w, http.StatusOK, resp)
}

// GetArchiveNewsletter handles GET /newsletters/archive/{newsletter_uid}
//
// Returns the full newsletter (including body_html) if:
// 1. The newsletter exists and status='sent' (prevents draft enumeration)
// 2. The authenticated caller is a member of at least one of its committees
//
// Returns 404 for missing or draft newsletters. Returns 403 for non-members.
func (h *Handler) GetArchiveNewsletter(w http.ResponseWriter, r *http.Request) {
	principal := UserFromContext(r.Context())
	if principal == "" {
		writeError(r.Context(), w, &authError{msg: "missing user principal", status: http.StatusUnauthorized})
		return
	}

	// Parse newsletter_uid from path.
	newsletterUID, err := parseUUID(r.PathValue("newsletter_uid"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	n, err := h.archive.GetArchive(r.Context(), service.GetArchiveInput{
		Principal:    principal,
		NewsletterID: newsletterUID,
	})
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	sentAt := time.Time{}
	if n.SentAt != nil {
		sentAt = *n.SentAt
	}

	resp := publicapi.ArchiveNewsletter{
		ID:            n.ID.String(),
		ProjectUID:    n.ProjectUID,
		Subject:       n.Subject,
		BodyHTML:      n.BodyHTML,
		SentAt:        sentAt,
		CommitteeUIDs: n.CommitteeUIDs,
		Status:        publicapi.Status(n.Status),
	}
	writeJSON(r.Context(), w, http.StatusOK, resp)
}

// normalizeCommitteeUIDs deduplicates and trims committee UIDs, preserving first-seen order.
// Empty values (after trimming) are dropped.
func normalizeCommitteeUIDs(in []string) []string {
	if len(in) == 0 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

// parseCommitteeUIDs parses a comma-separated string of committee UIDs.
// Returns an empty slice if the input is empty or malformed.
func parseCommitteeUIDs(csv string) []string {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return []string{}
	}
	uids := strings.Split(csv, ",")
	result := make([]string, 0, len(uids))
	for _, uid := range uids {
		if uid := strings.TrimSpace(uid); uid != "" {
			result = append(result, uid)
		}
	}
	return result
}
