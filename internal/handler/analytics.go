// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"net/http"
	"time"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
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

// GetRecipientEngagement handles
// GET /projects/{project_uid}/newsletters/{newsletter_uid}/analytics/recipients.
//
// Returns ErrNotFound if the newsletter doesn't exist or belongs to a
// different project. Drafts (and sent rows without a group_id) return an
// empty recipients list; a provider engagement-read failure is an error
// response, never an empty list.
func (h *Handler) GetRecipientEngagement(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	id, err := parseUUID(r.PathValue("newsletter_uid"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	records, err := h.analytics.Recipients(r.Context(), projectUID, id)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	writeJSON(r.Context(), w, http.StatusOK, toAPIRecipientEngagement(id.String(), records))
}

// toAPIRecipientEngagement converts per-recipient engagement records into the
// public DTO, keeping the always-present-array contract for opened_at_list
// and recipients.
func toAPIRecipientEngagement(newsletterID string, records []port.RecipientEngagement) publicapi.NewsletterRecipientEngagementResponse {
	recipients := make([]publicapi.NewsletterRecipientEngagement, 0, len(records))
	for _, re := range records {
		opens := re.Record.OpenedAtList
		if opens == nil {
			opens = []time.Time{}
		}
		recipients = append(recipients, publicapi.NewsletterRecipientEngagement{
			Name:         re.Name,
			Email:        re.Record.To,
			SentAt:       re.Record.SentAt,
			Delivered:    re.Record.Delivered,
			DeliveredAt:  re.Record.DeliveredAt,
			Failed:       re.Record.Failed,
			FailedAt:     re.Record.FailedAt,
			Opened:       re.Record.Opened,
			OpenCount:    re.Record.OpenCount,
			LastOpenedAt: re.Record.LastOpened,
			OpenedAtList: opens,
		})
	}
	return publicapi.NewsletterRecipientEngagementResponse{
		NewsletterID: newsletterID,
		Recipients:   recipients,
	}
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
	failedRecipients := a.FailedRecipients
	if failedRecipients == nil {
		failedRecipients = []string{}
	}
	return publicapi.NewsletterAnalytics{
		NewsletterID:     a.NewsletterID.String(),
		Subject:          a.Subject,
		Status:           publicapi.Status(a.Status),
		SentAt:           a.SentAt,
		TotalRecipients:  a.TotalRecipients,
		Delivered:        a.Delivered,
		Failed:           a.Failed,
		FailedRecipients: failedRecipients,
		TotalOpens:       a.TotalOpens,
		UniqueOpens:      a.UniqueOpens,
		OpenRate:         a.OpenRate,
		DailyOpens:       daily,
		LastEventAt:      a.LastEventAt,
	}
}
