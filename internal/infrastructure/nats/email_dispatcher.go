// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package nats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	emailapi "github.com/linuxfoundation/lfx-v2-email-service/pkg/api"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// EmailDispatcher implements port.EmailDispatcher over NATS request/reply
// against the lfx-v2-email-service subjects. There is no auth context on the
// wire — NATS network access is the only gate (mirrors the comment in the
// email-service NATS handler).
type EmailDispatcher struct {
	client *Client
}

// NewEmailDispatcher wires an EmailDispatcher backed by the shared NATS client.
func NewEmailDispatcher(client *Client) *EmailDispatcher {
	return &EmailDispatcher{client: client}
}

// SendEmail dispatches one email to lfx-v2-email-service. Email-service mints
// the group_id when missing; we always pass one through so analytics can be
// aggregated reliably.
func (d *EmailDispatcher) SendEmail(ctx context.Context, in port.SendEmailInput) (string, error) {
	envelope := emailapi.SendEmailRequest{
		To:      in.To,
		Subject: in.Subject,
		HTML:    in.HTML,
		Text:    in.Text,
		GroupID: in.GroupID,
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return "", pkgerrors.NewUnexpected("marshal send_email request", err)
	}
	reply, err := d.client.Request(ctx, EmailServiceSendEmailSubject, data)
	if err != nil {
		return "", err
	}
	if len(reply) == 0 {
		// Email-service convention: empty reply means accepted with no
		// per-message identifiers. Treat as success without a tracking handle.
		return "", nil
	}
	var ok emailapi.SendEmailResponse
	if jsonErr := json.Unmarshal(reply, &ok); jsonErr == nil && ok.EmailID != "" {
		return ok.EmailID, nil
	}
	var errResp emailapi.SendEmailErrorResponse
	if jsonErr := json.Unmarshal(reply, &errResp); jsonErr != nil {
		return "", pkgerrors.NewUnexpected("malformed email-service reply", jsonErr)
	}
	if errResp.Error != "" {
		return "", pkgerrors.NewServiceUnavailable("email-service returned error", errors.New(errResp.Error))
	}
	return "", nil
}

// GetEngagement fetches per-group engagement totals from email-service.
//
// Note: email-service does not currently report unique opens — UniqueOpens is
// populated from the local newsletter_opens table by the analytics service.
func (d *EmailDispatcher) GetEngagement(ctx context.Context, groupID string) (*port.EmailEngagement, error) {
	if groupID == "" {
		return nil, pkgerrors.NewValidation("group_id is required")
	}
	envelope := emailapi.GetEmailEngagementAnalyticsRequest{GroupID: groupID}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, pkgerrors.NewUnexpected("marshal get_email_engagement_analytics request", err)
	}
	reply, err := d.client.Request(ctx, EmailServiceGetEngagementSubject, data)
	if err != nil {
		return nil, err
	}
	if len(reply) == 0 {
		// Treat empty reply as "no data yet" rather than an error; allows
		// the analytics endpoint to surface a usable empty response while
		// the email-service backfills records.
		return &port.EmailEngagement{GroupID: groupID}, nil
	}
	var errResp emailapi.SendEmailErrorResponse
	if jsonErr := json.Unmarshal(reply, &errResp); jsonErr == nil && errResp.Error != "" {
		return nil, pkgerrors.NewServiceUnavailable("email-service returned error", errors.New(errResp.Error))
	}
	var out emailapi.GetEmailEngagementAnalyticsResponse
	if jsonErr := json.Unmarshal(reply, &out); jsonErr != nil {
		return nil, pkgerrors.NewUnexpected("malformed email-service engagement reply", jsonErr)
	}
	return &port.EmailEngagement{
		GroupID:   out.GroupID,
		TotalSent: out.TotalSent,
		Delivered: out.Delivered,
		Opened:    out.Opened,
		Failed:    out.Failed,
	}, nil
}

// GetStatusByEmailID fetches per-recipient state from email-service for one
// previously-dispatched email_id.
func (d *EmailDispatcher) GetStatusByEmailID(ctx context.Context, emailID string) (*port.EmailRecipientRecord, error) {
	if emailID == "" {
		return nil, pkgerrors.NewValidation("email_id is required")
	}
	envelope := emailapi.GetEmailStatusRequest{EmailID: emailID}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, pkgerrors.NewUnexpected("marshal get_email_status request", err)
	}
	reply, err := d.client.Request(ctx, EmailServiceGetEmailStatusSubject, data)
	if err != nil {
		return nil, err
	}
	if len(reply) == 0 {
		return nil, pkgerrors.NewNotFound(fmt.Sprintf("email status for %s not found", emailID))
	}
	var errResp emailapi.SendEmailErrorResponse
	if jsonErr := json.Unmarshal(reply, &errResp); jsonErr == nil && errResp.Error != "" {
		return nil, pkgerrors.NewServiceUnavailable("email-service returned error", errors.New(errResp.Error))
	}
	var out emailapi.EmailRecipientRecord
	if jsonErr := json.Unmarshal(reply, &out); jsonErr != nil {
		return nil, pkgerrors.NewUnexpected("malformed email-service status reply", jsonErr)
	}
	sentAt := out.SentAt
	return &port.EmailRecipientRecord{
		EmailID:    out.EmailID,
		GroupID:    out.GroupID,
		To:         out.To,
		SentAt:     &sentAt,
		Delivered:  out.Delivered,
		Opened:     out.Opened,
		OpenCount:  0,
		LastOpened: out.OpenedAt,
		Failed:     out.Failed,
	}, nil
}
