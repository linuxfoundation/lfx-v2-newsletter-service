// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
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
		DeliveredAt:  r.DeliveredAt,
		Opened:       r.Opened,
		OpenCount:    r.OpenCount,
		LastOpened:   r.LastOpenedAt,
		OpenedAtList: openedAt,
		Failed:       r.Failed,
		FailedAt:     r.FailedAt,
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

// sendgridRevertedGroupRow is the tombstone for a reverted send group. reverted_at
// defaults at the DB layer, so only group_id is written on insert.
type sendgridRevertedGroupRow struct {
	bun.BaseModel `bun:"table:sendgrid_reverted_groups,alias:srg"`

	GroupID string `bun:"group_id,pk"`
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

// RevertGroup tombstones a group and purges its engagement, atomically. It runs
// in one transaction so the tombstone and the deletes commit together: it first
// records the group in sendgrid_reverted_groups (idempotent), then deletes the
// group's open events (joined through its engagement rows on email_id) and the
// engagement rows themselves. The tombstone durably marks the group reverted so
// the BEFORE INSERT trigger on sendgrid_recipient_engagement rejects any late
// webhook that would otherwise self-heal a purged row and re-persist recipient
// PII. Used after a fully-failed send reverts to draft and clears its group_id.
func (s *SendGridEngagementStore) RevertGroup(ctx context.Context, groupID string) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// Take the same per-group advisory lock the engagement insert trigger takes,
		// so this revert and any concurrent webhook write for the group are ordered
		// rather than racing under READ COMMITTED. Held until this tx commits.
		if _, err := tx.NewRaw(
			"SELECT pg_advisory_xact_lock(hashtextextended('sendgrid_group:' || ?, 0))", groupID,
		).Exec(ctx); err != nil {
			return fmt.Errorf("sendgrid lock reverted group: %w", err)
		}
		if _, err := tx.NewInsert().
			Model(&sendgridRevertedGroupRow{GroupID: groupID}).
			On("CONFLICT (group_id) DO NOTHING").
			Exec(ctx); err != nil {
			return fmt.Errorf("sendgrid tombstone reverted group: %w", err)
		}
		if _, err := tx.NewDelete().
			Table("sendgrid_open_events").
			Where("email_id IN (SELECT email_id FROM sendgrid_recipient_engagement WHERE group_id = ?)", groupID).
			Exec(ctx); err != nil {
			return fmt.Errorf("sendgrid delete open events by group: %w", err)
		}
		if _, err := tx.NewDelete().
			Table("sendgrid_recipient_engagement").
			Where("group_id = ?", groupID).
			Exec(ctx); err != nil {
			return fmt.Errorf("sendgrid delete engagement by group: %w", err)
		}
		return nil
	})
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
		On("CONFLICT (email_id) DO UPDATE SET delivered = TRUE, delivered_at = EXCLUDED.delivered_at WHERE sre.delivered = FALSE").
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
		On("CONFLICT (email_id) DO UPDATE SET failed = TRUE, failed_at = EXCLUDED.failed_at WHERE sre.failed = FALSE").
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
		// Serialize against RevertGroup and skip a tombstoned group before writing
		// anything. The engagement trigger already rejects the rollup insert, but
		// the open-event row is not covered by it (sendgrid_open_events has no
		// group_id), so without this check a delayed open for a reverted group
		// would still commit an orphan open-event row. Same advisory lock as the
		// trigger / RevertGroup, so this open and a concurrent revert are ordered.
		if _, err := tx.NewRaw(
			"SELECT pg_advisory_xact_lock(hashtextextended('sendgrid_group:' || ?, 0))", groupID,
		).Exec(ctx); err != nil {
			return fmt.Errorf("sendgrid apply open (lock): %w", err)
		}
		var reverted bool
		if err := tx.NewRaw(
			"SELECT EXISTS(SELECT 1 FROM sendgrid_reverted_groups WHERE group_id = ?)", groupID,
		).Scan(ctx, &reverted); err != nil {
			return fmt.Errorf("sendgrid apply open (tombstone check): %w", err)
		}
		if reverted {
			return nil // group reverted — drop the open so no orphan row is left
		}
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
			On("CONFLICT (email_id) DO UPDATE SET opened = TRUE, open_count = sre.open_count + 1, last_opened_at = GREATEST(sre.last_opened_at, EXCLUDED.last_opened_at)").
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

// RecipientsByGroupID returns every recipient record for a group (bounded by the
// recipient count), for the unique-opens count and failed-recipient list. It
// does not load the raw open-event timestamps — the daily-opens series comes
// from GroupDailyOpens, which aggregates in SQL.
func (s *SendGridEngagementStore) RecipientsByGroupID(ctx context.Context, groupID string) ([]port.EmailRecipientRecord, error) {
	var rows []sendgridEngagementRow
	if err := s.db.NewSelect().Model(&rows).Where("sre.group_id = ?", groupID).Scan(ctx); err != nil {
		return nil, fmt.Errorf("sendgrid recipients: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	out := make([]port.EmailRecipientRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toPortRecord(nil))
	}
	return out, nil
}

// MaxOpensPerRecipient caps each recipient's opened_at_list on the
// per-recipient analytics read. Bounds the query's transfer and memory even
// for a recipient with pathological repeat opens (mail-client prefetchers);
// the cap keeps the most recent opens.
const MaxOpensPerRecipient = 500

// RecipientRecordsByGroupID returns every recipient record for a group WITH its
// ascending open-timestamp series, for the per-recipient analytics endpoint.
// Recipient count is bounded by the committee audience; each recipient's open
// series is capped at MaxOpensPerRecipient most-recent opens, enforced in SQL
// (a ROW_NUMBER window in a subquery — Postgres forbids window functions
// directly in WHERE) so neither transfer nor heap is unbounded. An unknown
// group returns an empty slice, matching RecipientsByGroupID.
func (s *SendGridEngagementStore) RecipientRecordsByGroupID(ctx context.Context, groupID string) ([]port.EmailRecipientRecord, error) {
	var rows []sendgridEngagementRow
	if err := s.db.NewSelect().Model(&rows).Where("sre.group_id = ?", groupID).Scan(ctx); err != nil {
		return nil, fmt.Errorf("sendgrid recipient records: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ranked := s.db.NewSelect().
		ColumnExpr("soe.sg_event_id, soe.email_id, soe.opened_at").
		ColumnExpr("ROW_NUMBER() OVER (PARTITION BY soe.email_id ORDER BY soe.opened_at DESC) AS rn").
		TableExpr("sendgrid_open_events AS soe").
		Join("JOIN sendgrid_recipient_engagement AS sre ON sre.email_id = soe.email_id").
		Where("sre.group_id = ?", groupID)
	var evs []sendgridOpenEventRow
	if err := s.db.NewSelect().
		ColumnExpr("sg_event_id, email_id, opened_at").
		TableExpr("(?) AS ranked_events", ranked).
		Where("rn <= ?", MaxOpensPerRecipient).
		OrderExpr("opened_at ASC").
		Scan(ctx, &evs); err != nil {
		return nil, fmt.Errorf("sendgrid recipient open events: %w", err)
	}
	opensByEmail := make(map[string][]time.Time, len(rows))
	for _, ev := range evs {
		opensByEmail[ev.EmailID] = append(opensByEmail[ev.EmailID], ev.OpenedAt)
	}
	out := make([]port.EmailRecipientRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toPortRecord(opensByEmail[r.EmailID]))
	}
	return out, nil
}

// GroupEngagementDetail computes the group's bounded analytics detail with a few
// SQL aggregates — no raw per-open data is loaded: the unique-open count (rows
// flagged opened), the per-UTC-day opens series (GROUP BY, ascending), the last
// open instant (a nullable MAX), and the deduplicated failed-recipient
// addresses. lastEvent is nil for a group with no opens.
func (s *SendGridEngagementStore) GroupEngagementDetail(ctx context.Context, groupID string) (*port.GroupEngagementDetail, error) {
	detail := &port.GroupEngagementDetail{FailedRecipients: []string{}}

	if unique, err := s.db.NewSelect().
		Table("sendgrid_recipient_engagement").
		Where("group_id = ?", groupID).
		Where("opened").
		Count(ctx); err != nil {
		return nil, fmt.Errorf("sendgrid group unique opens: %w", err)
	} else {
		detail.UniqueOpens = unique
	}

	type dayRow struct {
		Day    time.Time `bun:"day"`
		Opens  int       `bun:"opens"`
		Unique int       `bun:"uniq"`
	}
	var rows []dayRow
	if err := s.db.NewSelect().
		ColumnExpr("date_trunc('day', soe.opened_at AT TIME ZONE 'UTC') AS day").
		ColumnExpr("COUNT(*) AS opens").
		ColumnExpr("COUNT(DISTINCT soe.email_id) AS uniq").
		TableExpr("sendgrid_open_events AS soe").
		Join("JOIN sendgrid_recipient_engagement AS sre ON sre.email_id = soe.email_id").
		Where("sre.group_id = ?", groupID).
		GroupExpr("date_trunc('day', soe.opened_at AT TIME ZONE 'UTC')").
		OrderExpr("day ASC").
		Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("sendgrid group daily opens: %w", err)
	}
	detail.DailyOpens = make([]model.DailyOpens, 0, len(rows))
	for _, r := range rows {
		detail.DailyOpens = append(detail.DailyOpens, model.DailyOpens{Date: r.Day.UTC(), Opens: r.Opens, UniqueOpens: r.Unique})
	}

	// The last open instant is a single scalar MAX. It is NULL for a group with no
	// opens, so scan into a nullable value rather than time.Time (which errors on
	// NULL) — a no-opens group returns an empty series and nil lastEvent.
	var maxAt sql.NullTime
	if err := s.db.NewSelect().
		ColumnExpr("MAX(soe.opened_at) AS max_at").
		TableExpr("sendgrid_open_events AS soe").
		Join("JOIN sendgrid_recipient_engagement AS sre ON sre.email_id = soe.email_id").
		Where("sre.group_id = ?", groupID).
		Scan(ctx, &maxAt); err != nil {
		return nil, fmt.Errorf("sendgrid group last open: %w", err)
	}
	if maxAt.Valid {
		u := maxAt.Time.UTC()
		detail.LastEventAt = &u
	}

	// Failed recipients: only the flagged rows' addresses (bounded), lowercased
	// and deduplicated to match the recipient-resolution convention.
	var failedRows []struct {
		To string `bun:"to_email"`
	}
	if err := s.db.NewSelect().
		ColumnExpr("to_email").
		Table("sendgrid_recipient_engagement").
		Where("group_id = ?", groupID).
		Where("failed").
		OrderExpr("email_id ASC").
		Scan(ctx, &failedRows); err != nil {
		return nil, fmt.Errorf("sendgrid group failed recipients: %w", err)
	}
	seen := make(map[string]struct{}, len(failedRows))
	for _, r := range failedRows {
		email := strings.ToLower(strings.TrimSpace(r.To))
		if email == "" {
			continue
		}
		if _, dup := seen[email]; dup {
			continue
		}
		seen[email] = struct{}{}
		detail.FailedRecipients = append(detail.FailedRecipients, email)
	}
	return detail, nil
}

// openTimesForEmail returns the ascending open timestamps for one email_id. This
// is per-recipient (bounded by one recipient's opens) and feeds the single-
// recipient status read, not the group analytics path.
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
