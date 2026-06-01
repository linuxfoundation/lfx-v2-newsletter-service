// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package nats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	emailapi "github.com/linuxfoundation/lfx-v2-email-service/pkg/api"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// openEventWire mirrors email-service's per-event entry inside opened_at_list.
// Defined locally instead of leaning on emailapi because the pinned
// email-service version (v0.1.3) predates the OpenedAtList field and we want
// to deserialize the newer shape without a dep bump.
type openEventWire struct {
	EventID  string    `json:"event_id"`
	OpenedAt time.Time `json:"opened_at"`
}

// emailRecipientWire is the lenient JSON shape used to decode replies from
// lfx.email-service.get_email_status. Decoding through this struct (instead of
// emailapi.EmailRecipientRecord) lets newsletter-service accept both:
//   - the older flat shape exposed by email-service v0.1.3 (`opened_at`
//     single timestamp; no list), and
//   - the newer shape (`opened_at_list` per-event series + `last_opened_at`).
//
// Whichever email-service version is deployed, the higher layers see a single
// canonical `port.EmailRecipientRecord` carrying OpenedAtList — populated from
// the list when available, falling back to a one-element slice when only the
// flat timestamp is present.
type emailRecipientWire struct {
	GroupID      string          `json:"group_id"`
	EmailID      string          `json:"email_id"`
	To           string          `json:"to"`
	Subject      string          `json:"subject"`
	SentAt       time.Time       `json:"sent_at"`
	Delivered    bool            `json:"delivered"`
	DeliveredAt  *time.Time      `json:"delivered_at,omitempty"`
	Opened       bool            `json:"opened"`
	OpenedAt     *time.Time      `json:"opened_at,omitempty"`
	OpenedAtList []openEventWire `json:"opened_at_list,omitempty"`
	LastOpenedAt *time.Time      `json:"last_opened_at,omitempty"`
	Failed       bool            `json:"failed"`
	FailedAt     *time.Time      `json:"failed_at,omitempty"`
}

// toPortRecord normalizes the wire shape into port.EmailRecipientRecord.
// OpenedAtList is always populated when the recipient has any open data, so
// downstream callers don't need to know which email-service version replied.
func (w emailRecipientWire) toPortRecord() port.EmailRecipientRecord {
	sentAt := w.SentAt
	var openedAtList []time.Time
	switch {
	case len(w.OpenedAtList) > 0:
		openedAtList = make([]time.Time, 0, len(w.OpenedAtList))
		for _, ev := range w.OpenedAtList {
			openedAtList = append(openedAtList, ev.OpenedAt)
		}
	case w.OpenedAt != nil:
		openedAtList = []time.Time{*w.OpenedAt}
	}
	lastOpened := w.LastOpenedAt
	if lastOpened == nil {
		lastOpened = w.OpenedAt
	}
	if lastOpened == nil && len(openedAtList) > 0 {
		// Synthesize last-opened from the maximum of the per-event list when
		// neither legacy field is present.
		max := openedAtList[0]
		for _, t := range openedAtList[1:] {
			if t.After(max) {
				max = t
			}
		}
		lastOpened = &max
	}
	return port.EmailRecipientRecord{
		EmailID:      w.EmailID,
		GroupID:      w.GroupID,
		To:           w.To,
		SentAt:       &sentAt,
		Delivered:    w.Delivered,
		Opened:       w.Opened,
		OpenCount:    len(openedAtList),
		LastOpened:   lastOpened,
		OpenedAtList: openedAtList,
		Failed:       w.Failed,
	}
}

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

// GetStatusByGroupID fetches the per-recipient records for every email
// dispatched under the given group_id. Email-service's by-group reply is a
// JSON array of EmailRecipientRecord (one per email_id in the group).
//
// Used by AnalyticsService to populate DailyOpens (bucketed by OpenedAt) and
// UniqueOpens (count of records where Opened == true) — the scalar engagement
// summary doesn't expose either.
func (d *EmailDispatcher) GetStatusByGroupID(ctx context.Context, groupID string) ([]port.EmailRecipientRecord, error) {
	if groupID == "" {
		return nil, pkgerrors.NewValidation("group_id is required")
	}
	envelope := emailapi.GetEmailStatusRequest{GroupID: groupID}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, pkgerrors.NewUnexpected("marshal get_email_status by group request", err)
	}
	reply, err := d.client.Request(ctx, EmailServiceGetEmailStatusSubject, data)
	if err != nil {
		return nil, err
	}
	if len(reply) == 0 {
		// No records yet — treat as empty rather than an error so analytics
		// can degrade gracefully on a freshly-sent newsletter where the
		// group index hasn't propagated.
		return nil, nil
	}
	var errResp emailapi.SendEmailErrorResponse
	if jsonErr := json.Unmarshal(reply, &errResp); jsonErr == nil && errResp.Error != "" {
		return nil, pkgerrors.NewServiceUnavailable("email-service returned error", errors.New(errResp.Error))
	}
	var out []emailRecipientWire
	if jsonErr := json.Unmarshal(reply, &out); jsonErr != nil {
		return nil, pkgerrors.NewUnexpected("malformed email-service group-status reply", jsonErr)
	}
	records := make([]port.EmailRecipientRecord, 0, len(out))
	for _, r := range out {
		records = append(records, r.toPortRecord())
	}
	return records, nil
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
	var out emailRecipientWire
	if jsonErr := json.Unmarshal(reply, &out); jsonErr != nil {
		return nil, pkgerrors.NewUnexpected("malformed email-service status reply", jsonErr)
	}
	rec := out.toPortRecord()
	return &rec, nil
}
