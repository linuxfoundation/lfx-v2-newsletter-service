// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"net/http"
	"strings"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// SendNewsletter handles POST /projects/{project_uid}/newsletters/{newsletter_uid}/send.
//
// Service mints group_id, resolves recipients, fans out emails to email-service
// via NATS, and persists the status transition. Per-recipient failures are
// returned in the response so the UI can surface them; the newsletter is still
// marked sent if any recipients succeeded.
func (h *Handler) SendNewsletter(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	id, err := parseUUID(r.PathValue("newsletter_uid"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	expectedVersion, err := requireIfMatch(r)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	result, err := h.send.SendNewsletter(r.Context(), service.SendNewsletterInput{
		ProjectUID:      projectUID,
		NewsletterID:    id,
		ExpectedVersion: expectedVersion,
		EDName:          resolveEDName(r),
	})
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	w.Header().Set("ETag", formatETag(result.Newsletter.Version))
	writeJSON(r.Context(), w, http.StatusOK, toAPISendResponse(result))
}

// RecipientCount handles POST /projects/{project_uid}/newsletters/recipient-count.
func (h *Handler) RecipientCount(w http.ResponseWriter, r *http.Request) {
	var body publicapi.RecipientCountRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}
	count, err := h.send.RecipientCount(r.Context(), body.CommitteeUIDs)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	writeJSON(r.Context(), w, http.StatusOK, publicapi.RecipientCountResponse{Count: count})
}

// Recipients handles POST /projects/{project_uid}/newsletters/recipients.
func (h *Handler) Recipients(w http.ResponseWriter, r *http.Request) {
	var body publicapi.RecipientsRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}
	recipients, err := h.send.Recipients(r.Context(), body.CommitteeUIDs)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	out := publicapi.RecipientsResponse{Recipients: make([]publicapi.Recipient, 0, len(recipients))}
	for _, recipient := range recipients {
		out.Recipients = append(out.Recipients, publicapi.Recipient{
			Email:     recipient.Email,
			FirstName: recipient.FirstName,
		})
	}
	writeJSON(r.Context(), w, http.StatusOK, out)
}

// TestSend handles POST /projects/{project_uid}/newsletters/test-send.
func (h *Handler) TestSend(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	var body publicapi.TestSendRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}
	if err := h.send.TestSend(r.Context(), service.TestSendInput{
		ProjectUID:   projectUID,
		Subject:      body.Subject,
		BodyHTML:     body.BodyHTML,
		ToEmail:      body.ToEmail,
		EDReplyEmail: body.EDReplyEmail,
		EDName:       resolveEDName(r),
	}); err != nil {
		writeError(r.Context(), w, err)
		return
	}
	writeJSON(r.Context(), w, http.StatusOK, publicapi.TestSendResponse{OK: true})
}

// resolveEDName resolves the executive director display name from request
// metadata. Prefers the X-User-Name header (set by an upstream proxy when
// available) and falls back to the JWT principal.
func resolveEDName(r *http.Request) string {
	if name := strings.TrimSpace(r.Header.Get("X-User-Name")); name != "" {
		return name
	}
	if user := UserFromContext(r.Context()); user != "" {
		return user
	}
	return "Executive Director"
}

// toAPISendResponse converts a service SendResult into the public API DTO.
func toAPISendResponse(result *service.SendResult) publicapi.SendNewsletterResponse {
	failures := make([]publicapi.SendFailure, 0, len(result.Failures))
	for _, f := range result.Failures {
		failures = append(failures, publicapi.SendFailure{Email: f.Email, Error: f.Error})
	}
	return publicapi.SendNewsletterResponse{
		Newsletter:      *toAPINewsletter(result.Newsletter),
		GroupID:         result.GroupID,
		TotalRecipients: result.TotalRecipients,
		Sent:            result.Sent,
		Failed:          result.Failed,
		Failures:        failures,
	}
}
