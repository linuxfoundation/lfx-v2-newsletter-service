// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"net/http"
	"time"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// SendNewsletter handles POST /projects/{project_uid}/newsletters/{newsletter_uid}/send.
//
// The service validates the draft, resolves recipients, transitions the row to
// status=sending (the duplicate-send guard), and runs the per-recipient
// fan-out in a background job detached from this request. The response is
// therefore 202 Accepted with the sending-state newsletter (sent=0); clients
// observe completion by re-fetching the newsletter, whose status settles to
// 'sent' when at least one recipient succeeds or any result is ambiguous
// (or reverts to 'draft' when zero recipients could be delivered to).
// A settled status='sent' does NOT guarantee all recipients were accepted —
// only that the send was initiated. The zero-recipient edge case settles
// synchronously and returns 200 with status='sent', preserving the historical
// contract. Consult the Failures list and provider engagement metrics for
// per-recipient delivery outcomes.
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
		Principal:       UserFromContext(r.Context()),
	})
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	status := http.StatusOK
	if result.Newsletter.Status == model.StatusSending {
		status = http.StatusAccepted
	}
	w.Header().Set("ETag", formatETag(result.Newsletter.Version))
	writeJSON(r.Context(), w, status, toAPISendResponse(result))
}

// ScheduleNewsletter handles POST /projects/{project_uid}/newsletters/{newsletter_uid}/schedule.
//
// The request body is optional: an override scheduled_at, else the draft's
// own saved value is used (a 400 if neither exists). Behaves like
// SendNewsletter otherwise — 202 Accepted with the sending-state newsletter,
// settling in the background to status='scheduled' when at least one recipient's
// message is accepted by SendGrid for release, or if any scheduling outcome is
// ambiguous. It reverts to 'draft' only when zero recipients could be scheduled
// and no outcome was ambiguous. A settled status='scheduled' does NOT guarantee
// all recipients were accepted — only that the schedule was armed. Consult the
// Failures list for per-recipient scheduling outcomes.
func (h *Handler) ScheduleNewsletter(w http.ResponseWriter, r *http.Request) {
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

	var body publicapi.ScheduleNewsletterRequest
	if err := decodeJSONOptional(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}

	result, err := h.send.SendNewsletter(r.Context(), service.SendNewsletterInput{
		ProjectUID:      projectUID,
		NewsletterID:    id,
		ExpectedVersion: expectedVersion,
		Principal:       UserFromContext(r.Context()),
		Schedule:        true,
		ScheduledAt:     body.ScheduledAt,
	})
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	status := http.StatusOK
	if result.Newsletter.Status == model.StatusSending {
		status = http.StatusAccepted
	}
	w.Header().Set("ETag", formatETag(result.Newsletter.Version))
	writeJSON(r.Context(), w, status, toAPIScheduleResponse(result))
}

// CancelScheduleNewsletter handles POST
// /projects/{project_uid}/newsletters/{newsletter_uid}/cancel-schedule.
func (h *Handler) CancelScheduleNewsletter(w http.ResponseWriter, r *http.Request) {
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

	reverted, err := h.send.CancelScheduled(r.Context(), service.CancelScheduledInput{
		ProjectUID:      projectUID,
		NewsletterID:    id,
		ExpectedVersion: expectedVersion,
	})
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	w.Header().Set("ETag", formatETag(reverted.Version))
	writeJSON(r.Context(), w, http.StatusOK, publicapi.CancelScheduleResponse{Newsletter: *toAPINewsletter(reverted)})
}

// RecipientCount handles POST /projects/{project_uid}/newsletters/recipient-count.
func (h *Handler) RecipientCount(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	var body publicapi.RecipientCountRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}
	count, err := h.send.RecipientCount(r.Context(), projectUID, body.CommitteeUIDs)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	writeJSON(r.Context(), w, http.StatusOK, publicapi.RecipientCountResponse{Count: count})
}

// Recipients handles POST /projects/{project_uid}/newsletters/recipients.
func (h *Handler) Recipients(w http.ResponseWriter, r *http.Request) {
	projectUID := r.PathValue("project_uid")
	var body publicapi.RecipientsRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}
	recipients, err := h.send.Recipients(r.Context(), projectUID, body.CommitteeUIDs)
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
		Principal:    UserFromContext(r.Context()),
	}); err != nil {
		writeError(r.Context(), w, err)
		return
	}
	writeJSON(r.Context(), w, http.StatusOK, publicapi.TestSendResponse{OK: true})
}

// toAPIScheduleResponse converts a service SendResult into the schedule
// response DTO. ScheduledAt is read off the settled newsletter so the
// response always reflects the value actually persisted (the request's
// override, or the draft's own saved value when the request body was empty).
func toAPIScheduleResponse(result *service.SendResult) publicapi.ScheduleNewsletterResponse {
	failures := make([]publicapi.SendFailure, 0, len(result.Failures))
	for _, f := range result.Failures {
		failures = append(failures, publicapi.SendFailure{Email: f.Email, Error: f.Error})
	}
	var scheduledAt time.Time
	if result.Newsletter.ScheduledAt != nil {
		scheduledAt = *result.Newsletter.ScheduledAt
	}
	return publicapi.ScheduleNewsletterResponse{
		Newsletter:      *toAPINewsletter(result.Newsletter),
		GroupID:         result.GroupID,
		ScheduledAt:     scheduledAt,
		TotalRecipients: result.TotalRecipients,
		Sent:            result.Sent,
		Failed:          result.Failed,
		Failures:        failures,
	}
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
