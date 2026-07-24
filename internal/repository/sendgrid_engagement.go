// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// sendgridEngagementRow is the bun model for the sendgrid_recipient_engagement
// table: one row per SendGrid send, keyed by the email_id minted into custom_args.
type sendgridEngagementRow struct {
	bun.BaseModel `bun:"table:sendgrid_recipient_engagement,alias:sre"`

	EmailID      string     `bun:"email_id,pk"`
	GroupID      string     `bun:"group_id"`
	ToEmail      string     `bun:"to_email"`
	SentAt       *time.Time `bun:"sent_at"`
	Delivered    bool       `bun:"delivered"`
	DeliveredAt  *time.Time `bun:"delivered_at"`
	Opened       bool       `bun:"opened"`
	OpenCount    int        `bun:"open_count"`
	LastOpenedAt *time.Time `bun:"last_opened_at"`
	Failed       bool       `bun:"failed"`
	FailedAt     *time.Time `bun:"failed_at"`
}

func (r sendgridEngagementRow) toPortRecord(openedAt []time.Time) port.EmailRecipientRecord {
	return port.EmailRecipientRecord{
		EmailID:      r.EmailID,
		GroupID:      r.GroupID,
		To:           r.ToEmail,
		SentAt:       r.SentAt,
		Delivered:    r.Delivered,
		Opened:       r.Opened,
		OpenCount:    r.OpenCount,
		LastOpened:   r.LastOpenedAt,
		OpenedAtList: openedAt,
		Failed:       r.Failed,
	}
}

// sendgridOpenEventRow is the bun model for sendgrid_open_events: one row per
// open, deduplicated by the SendGrid sg_event_id. group_id is derived by joining
// to sendgrid_recipient_engagement on email_id.
type sendgridOpenEventRow struct {
	bun.BaseModel `bun:"table:sendgrid_open_events,alias:soe"`

	SGEventID string    `bun:"sg_event_id,pk"`
	EmailID   string    `bun:"email_id"`
	OpenedAt  time.Time `bun:"opened_at"`
}

// SendGridEngagementStore implements sendgrid.EngagementStore over PostgreSQL.
// The compile-time interface assertion lives at the wiring site (the service
// package) to keep this package free of an infrastructure import.
type SendGridEngagementStore struct {
	db *bun.DB
}

// NewSendGridEngagementStore wires the store over the given bun.DB.
func NewSendGridEngagementStore(db *bun.DB) *SendGridEngagementStore {
	return &SendGridEngagementStore{db: db}
}

// RecordSent inserts the initial engagement row for an accepted send. Idempotent
// on email_id: a duplicate is a no-op so the original send row wins.
func (s *SendGridEngagementStore) RecordSent(ctx context.Context, emailID, groupID, to string, sentAt time.Time) error {
	row := &sendgridEngagementRow{EmailID: emailID, GroupID: groupID, ToEmail: to, SentAt: &sentAt}
	if _, err := s.db.NewInsert().
		Model(row).
		On("CONFLICT (email_id) DO NOTHING").
		Exec(ctx); err != nil {
		return fmt.Errorf("sendgrid record sent: %w", err)
	}
	return nil
}

// RecordSentBatch inserts the initial engagement rows for a whole accepted chunk
// in one statement. Idempotent on email_id (ON CONFLICT DO NOTHING), so a
// redelivery or overlap leaves existing rows intact. A nil/empty slice is a no-op.
func (s *SendGridEngagementStore) RecordSentBatch(ctx context.Context, sents []port.SentRow) error {
	if len(sents) == 0 {
		return nil
	}
	rows := make([]sendgridEngagementRow, len(sents))
	for i, r := range sents {
		at := r.SentAt
		rows[i] = sendgridEngagementRow{EmailID: r.EmailID, GroupID: r.GroupID, ToEmail: r.To, SentAt: &at}
	}
	if _, err := s.db.NewInsert().
		Model(&rows).
		On("CONFLICT (email_id) DO NOTHING").
		Exec(ctx); err != nil {
		return fmt.Errorf("sendgrid record sent batch: %w", err)
	}
	return nil
}

// ApplyDelivered marks the recipient delivered (first delivery wins). It upserts
// on email_id, so a delivered event still lands its engagement even if RecordSent
// never persisted the row (e.g. the post-send record write failed) — the row is
// created from the event's group_id / to_email rather than lost. group_id and
// to_email are only set on insert; an existing RecordSent row keeps its values.
func (s *SendGridEngagementStore) ApplyDelivered(ctx context.Context, emailID, groupID, to string, at time.Time) error {
	row := &sendgridEngagementRow{EmailID: emailID, GroupID: groupID, ToEmail: to, Delivered: true, DeliveredAt: &at}
	if _, err := s.db.NewInsert().
		Model(row).
		On("CONFLICT (email_id) DO UPDATE SET delivered = TRUE, delivered_at = EXCLUDED.delivered_at WHERE sendgrid_recipient_engagement.delivered = FALSE").
		Exec(ctx); err != nil {
		return fmt.Errorf("sendgrid apply delivered: %w", err)
	}
	return nil
}

// ApplyFailed marks the recipient failed — bounce / dropped / spamreport (first
// failure wins). Upserts on email_id for the same self-healing reason as
// ApplyDelivered.
func (s *SendGridEngagementStore) ApplyFailed(ctx context.Context, emailID, groupID, to string, at time.Time) error {
	row := &sendgridEngagementRow{EmailID: emailID, GroupID: groupID, ToEmail: to, Failed: true, FailedAt: &at}
	if _, err := s.db.NewInsert().
		Model(row).
		On("CONFLICT (email_id) DO UPDATE SET failed = TRUE, failed_at = EXCLUDED.failed_at WHERE sendgrid_recipient_engagement.failed = FALSE").
		Exec(ctx); err != nil {
		return fmt.Errorf("sendgrid apply failed: %w", err)
	}
	return nil
}

// ApplyOpen records one open, deduplicated by sgEventID. The event insert and the
// recipient rollup run in one transaction so they commit together: a redelivery
// after a partial failure re-runs both rather than leaving the open counted in
// sendgrid_open_events but never rolled up. The rollup upserts on email_id, so an
// open still lands even if RecordSent never persisted the row. Only a genuinely
// new event bumps open_count / opened / last_opened_at.
func (s *SendGridEngagementStore) ApplyOpen(ctx context.Context, sgEventID, emailID, groupID, to string, at time.Time) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		ev := &sendgridOpenEventRow{SGEventID: sgEventID, EmailID: emailID, OpenedAt: at}
		res, err := tx.NewInsert().
			Model(ev).
			On("CONFLICT (sg_event_id) DO NOTHING").
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("sendgrid apply open (event): %w", err)
		}
		if affected, _ := res.RowsAffected(); affected == 0 {
			return nil // duplicate webhook delivery — already counted
		}
		row := &sendgridEngagementRow{EmailID: emailID, GroupID: groupID, ToEmail: to, Opened: true, OpenCount: 1, LastOpenedAt: &at}
		if _, err := tx.NewInsert().
			Model(row).
			On("CONFLICT (email_id) DO UPDATE SET opened = TRUE, open_count = sendgrid_recipient_engagement.open_count + 1, last_opened_at = GREATEST(sendgrid_recipient_engagement.last_opened_at, EXCLUDED.last_opened_at)").
			Exec(ctx); err != nil {
			return fmt.Errorf("sendgrid apply open (rollup): %w", err)
		}
		return nil
	})
}

// Engagement returns the per-group rollup. Opened is the raw open total
// (SUM(open_count)); UniqueOpens counts recipient rows whose boolean opened is
// true. Keeping them distinct matches the email-service dispatcher and avoids
// sum(DailyOpens) exceeding TotalOpens for a newsletter with repeat opens.
func (s *SendGridEngagementStore) Engagement(ctx context.Context, groupID string) (*port.EmailEngagement, error) {
	type aggRow struct {
		TotalSent   int `bun:"total_sent"`
		Delivered   int `bun:"delivered"`
		Opened      int `bun:"opened"`
		UniqueOpens int `bun:"unique_opens"`
		Failed      int `bun:"failed"`
	}
	agg := &aggRow{}
	if err := s.db.NewSelect().
		ColumnExpr("COUNT(*) AS total_sent").
		ColumnExpr("COUNT(*) FILTER (WHERE delivered) AS delivered").
		// Opened is the raw open total (feeds analytics TotalOpens, matching the
		// email-service dispatcher); UniqueOpens counts recipients who opened at
		// least once. Keeping them distinct avoids sum(DailyOpens) > TotalOpens.
		ColumnExpr("COALESCE(SUM(open_count), 0) AS opened").
		ColumnExpr("COUNT(*) FILTER (WHERE opened) AS unique_opens").
		ColumnExpr("COUNT(*) FILTER (WHERE failed) AS failed").
		Table("sendgrid_recipient_engagement").
		Where("group_id = ?", groupID).
		Scan(ctx, agg); err != nil {
		return nil, fmt.Errorf("sendgrid engagement: %w", err)
	}
	return &port.EmailEngagement{
		GroupID:     groupID,
		TotalSent:   agg.TotalSent,
		Delivered:   agg.Delivered,
		Opened:      agg.Opened,
		UniqueOpens: agg.UniqueOpens,
		Failed:      agg.Failed,
	}, nil
}

// RecipientByEmailID returns one recipient's engagement record with its open series.
func (s *SendGridEngagementStore) RecipientByEmailID(ctx context.Context, emailID string) (*port.EmailRecipientRecord, error) {
	row := &sendgridEngagementRow{}
	if err := s.db.NewSelect().Model(row).Where("sre.email_id = ?", emailID).Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("sendgrid recipient: %w", err)
	}
	opens, err := s.openTimesForEmail(ctx, emailID)
	if err != nil {
		return nil, err
	}
	rec := row.toPortRecord(opens)
	return &rec, nil
}

// RecipientsByGroupID returns every recipient record for a group, each carrying
// its open series so the analytics daily-opens time series can be built.
func (s *SendGridEngagementStore) RecipientsByGroupID(ctx context.Context, groupID string) ([]port.EmailRecipientRecord, error) {
	var rows []sendgridEngagementRow
	if err := s.db.NewSelect().Model(&rows).Where("sre.group_id = ?", groupID).Scan(ctx); err != nil {
		return nil, fmt.Errorf("sendgrid recipients: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	opensByEmail, err := s.openTimesForGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	out := make([]port.EmailRecipientRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toPortRecord(opensByEmail[r.EmailID]))
	}
	return out, nil
}

// openTimesForEmail returns the ascending open timestamps for one email_id.
func (s *SendGridEngagementStore) openTimesForEmail(ctx context.Context, emailID string) ([]time.Time, error) {
	var evs []sendgridOpenEventRow
	if err := s.db.NewSelect().
		Model(&evs).
		Where("soe.email_id = ?", emailID).
		OrderExpr("soe.opened_at ASC").
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("sendgrid open times: %w", err)
	}
	times := make([]time.Time, 0, len(evs))
	for _, e := range evs {
		times = append(times, e.OpenedAt)
	}
	return times, nil
}

// openTimesForGroup returns ascending open timestamps grouped by email_id for a
// group, via a join from open events to the group's engagement rows.
func (s *SendGridEngagementStore) openTimesForGroup(ctx context.Context, groupID string) (map[string][]time.Time, error) {
	type openRow struct {
		EmailID  string    `bun:"email_id"`
		OpenedAt time.Time `bun:"opened_at"`
	}
	var rows []openRow
	if err := s.db.NewSelect().
		ColumnExpr("soe.email_id").
		ColumnExpr("soe.opened_at").
		TableExpr("sendgrid_open_events AS soe").
		Join("JOIN sendgrid_recipient_engagement AS sre ON sre.email_id = soe.email_id").
		Where("sre.group_id = ?", groupID).
		OrderExpr("soe.opened_at ASC").
		Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("sendgrid group open times: %w", err)
	}
	out := make(map[string][]time.Time, len(rows))
	for _, r := range rows {
		out[r.EmailID] = append(out[r.EmailID], r.OpenedAt)
	}
	return out, nil
}
