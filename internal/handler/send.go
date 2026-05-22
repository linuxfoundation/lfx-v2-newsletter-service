// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"net/http"
	"strings"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	publicapi "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/api"
)

// SendDraft handles POST /newsletters/drafts/{id}/send.
//
// The request requires If-Match for optimistic locking. edName is taken from
// the X-User-Name header or falls back to the JWT principal.
func (h *Handler) SendDraft(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r.PathValue("id"))
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}
	expectedVersion, err := requireIfMatch(r)
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	result, err := h.send.SendDraft(r.Context(), service.SendDraftInput{
		DraftID:         id,
		ExpectedVersion: expectedVersion,
		EDName:          resolveEDName(r),
	})
	if err != nil {
		writeError(r.Context(), w, err)
		return
	}

	writeJSON(r.Context(), w, http.StatusOK, toAPISendResult(result))
}

// RecipientCount handles POST /newsletters/recipient-count.
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

// Recipients handles POST /newsletters/recipients.
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
	for _, r := range recipients {
		out.Recipients = append(out.Recipients, publicapi.Recipient{
			Email:     r.Email,
			FirstName: r.FirstName,
		})
	}
	writeJSON(r.Context(), w, http.StatusOK, out)
}

// TestSend handles POST /newsletters/test-send.
func (h *Handler) TestSend(w http.ResponseWriter, r *http.Request) {
	var body publicapi.TestSendRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(r.Context(), w, err)
		return
	}
	if err := h.send.TestSend(r.Context(), service.TestSendInput{
		ContextType:  model.ContextType(body.ContextType),
		ContextUID:   body.ContextUID,
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
// metadata. Prefers the X-User-Name header (set by lfx-v2-ui's proxy when
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

// toAPISendResult converts a domain SendResult into the public DTO.
func toAPISendResult(in *model.SendResult) publicapi.SendResponse {
	out := publicapi.SendResponse{
		TotalRecipients: in.TotalRecipients,
		Sent:            in.Sent,
		Failed:          in.Failed,
		Failures:        make([]publicapi.SendFailure, 0, len(in.Failures)),
	}
	for _, f := range in.Failures {
		out.Failures = append(out.Failures, publicapi.SendFailure{
			Email:  f.Email,
			Reason: f.Reason,
		})
	}
	return out
}
