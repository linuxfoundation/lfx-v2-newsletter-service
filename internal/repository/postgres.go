// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package repository contains the bun-backed implementation of the
// NewsletterRepository port.
package repository

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// defaultListLimit is the page size used when ListAll callers don't specify one.
// maxListLimit caps the page size to keep one round-trip bounded.
const (
	defaultListLimit = 20
	maxListLimit     = 100
)

// PostgresNewsletterRepo persists Newsletter aggregates in PostgreSQL via bun.
type PostgresNewsletterRepo struct {
	db *bun.DB
}

// NewPostgresNewsletterRepo wires a Newsletter repository over the given bun.DB.
func NewPostgresNewsletterRepo(db *bun.DB) *PostgresNewsletterRepo {
	return &PostgresNewsletterRepo{db: db}
}

// Create inserts a new Newsletter row. Database defaults populate id, version,
// timestamps; bun copies the generated values back into n.
func (r *PostgresNewsletterRepo) Create(ctx context.Context, n *model.Newsletter) error {
	if _, err := r.db.NewInsert().
		Model(n).
		Returning("*").
		Exec(ctx); err != nil {
		return fmt.Errorf("insert newsletter: %w", err)
	}
	return nil
}

// Get fetches a single newsletter by primary key.
func (r *PostgresNewsletterRepo) Get(ctx context.Context, id uuid.UUID) (*model.Newsletter, error) {
	n := &model.Newsletter{}
	err := r.db.NewSelect().
		Model(n).
		Where("n.id = ?", id).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("select newsletter: %w", err)
	}
	return n, nil
}

// List returns all newsletters in the given project, newest first.
func (r *PostgresNewsletterRepo) List(ctx context.Context, projectUID string) ([]*model.Newsletter, error) {
	var rows []*model.Newsletter
	err := r.db.NewSelect().
		Model(&rows).
		Where("project_uid = ?", projectUID).
		Order("created_at DESC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list newsletters: %w", err)
	}
	return rows, nil
}

// ListAll returns a page of newsletters matching the given filters, ordered by
// updated_at DESC (so the most recently touched draft / most recently sent
// newsletter appears first). The cursor encodes (updated_at, id) so the next
// page resumes deterministically even when many rows share an updated_at.
func (r *PostgresNewsletterRepo) ListAll(ctx context.Context, filters port.ListFilters) (*port.ListPage, error) {
	limit := filters.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}

	q := r.db.NewSelect().
		Model((*model.Newsletter)(nil)).
		Where("project_uid = ?", filters.ProjectUID).
		Order("updated_at DESC").
		Order("id DESC").
		Limit(limit + 1)

	if len(filters.Statuses) > 0 {
		q = q.Where("status IN (?)", bun.In(filters.Statuses))
	}

	if filters.PageToken != "" {
		cursor, err := decodeCursor(filters.PageToken)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid pageToken", domain.ErrInvalidRequest)
		}
		// Keyset pagination: continue from the (updated_at, id) tuple of the last
		// row of the previous page. Tuple comparison handles ties on updated_at.
		q = q.Where("(updated_at, id) < (?, ?)", cursor.UpdatedAt, cursor.ID)
	}

	var rows []*model.Newsletter
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list all newsletters: %w", err)
	}

	page := &port.ListPage{Newsletters: rows}
	if len(rows) > limit {
		// Trim the lookahead row and emit its cursor as the next page token.
		last := rows[limit-1]
		page.Newsletters = rows[:limit]
		page.NextPageToken = encodeCursor(listCursor{UpdatedAt: last.UpdatedAt, ID: last.ID})
	}
	return page, nil
}

// ListSentByCommittee returns a page of sent newsletters whose committee_uids
// contain the given committee, ordered by sent_at DESC (most recently sent
// first). Only status='sent' rows qualify — drafts and in-flight sends are
// member-invisible by design. The cursor encodes (sent_at, id) so the next
// page resumes deterministically even when many rows share a sent_at.
func (r *PostgresNewsletterRepo) ListSentByCommittee(ctx context.Context, committeeUID string, pageToken string) (*port.ListPage, error) {
	q := r.db.NewSelect().
		Model((*model.Newsletter)(nil)).
		Where("status = ?", model.StatusSent).
		// pgdialect.Array forces a Postgres text[] literal for the containment
		// operand; without it bun json-encodes the slice (see Update's
		// committee_uids Set).
		Where("committee_uids @> ?", pgdialect.Array([]string{committeeUID})).
		Order("sent_at DESC").
		Order("id DESC").
		Limit(defaultListLimit + 1)

	if pageToken != "" {
		cursor, err := decodeSentCursor(pageToken)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid pageToken", domain.ErrInvalidRequest)
		}
		// Keyset pagination: continue from the (sent_at, id) tuple of the last
		// row of the previous page. Tuple comparison handles ties on sent_at.
		q = q.Where("(sent_at, id) < (?, ?)", cursor.SentAt, cursor.ID)
	}

	var rows []*model.Newsletter
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list sent newsletters by committee: %w", err)
	}

	page := &port.ListPage{Newsletters: rows}
	if len(rows) > defaultListLimit {
		last := rows[defaultListLimit-1]
		page.Newsletters = rows[:defaultListLimit]
		// last.SentAt is always set in practice: both sent transitions
		// (MarkSent, RecoverStuckSending) persist sent_at, and this query
		// filters status='sent'. The DB doesn't enforce that invariant with a
		// CHECK, so guard anyway — the failure mode is a suppressed
		// NextPageToken (list truncates at this page) rather than a panic.
		if last.SentAt != nil {
			page.NextPageToken = encodeSentCursor(sentListCursor{SentAt: *last.SentAt, ID: last.ID})
		}
	}
	return page, nil
}

// Update applies optimistic-locking-aware mutations. The query gates on
// (id, expectedVersion) and atomically increments version. If no rows are
// affected, the method follows up with an existence check to disambiguate
// ErrNotFound vs ErrVersionMismatch.
func (r *PostgresNewsletterRepo) Update(ctx context.Context, n *model.Newsletter, expectedVersion int64) (*model.Newsletter, error) {
	res, err := r.db.NewUpdate().
		Model(n).
		Set("subject = ?", n.Subject).
		Set("body_html = ?", n.BodyHTML).
		Set("ed_reply_email = ?", n.EDReplyEmail).
		// pgdialect.Array forces a Postgres text[] literal; without it bun
		// json-encodes the slice and PG raises a "malformed array literal".
		Set("committee_uids = ?", pgdialect.Array(n.CommitteeUIDs)).
		Set("project_uid = ?", n.ProjectUID).
		Set("updated_at = now()").
		Set("version = version + 1").
		Where("id = ? AND version = ?", n.ID, expectedVersion).
		Returning("*").
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("update newsletter: %w", err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("update newsletter rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return nil, r.classifyMissing(ctx, n.ID)
	}

	// bun's Returning("*") populated n with the new row state.
	return n, nil
}

// Delete removes a newsletter by id. Returns ErrNotFound if no row was deleted.
func (r *PostgresNewsletterRepo) Delete(ctx context.Context, id uuid.UUID) error {
	res, err := r.db.NewDelete().
		Model((*model.Newsletter)(nil)).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete newsletter: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete newsletter rows affected: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// MarkSending transitions a draft to status=sending atomically, gated on the
// expected version. Captures the audience size and the lfx-v2-email-service
// group_id at acceptance time: group_id must be persisted before any email is
// dispatched so a crash-recovered row can be marked sent without violating the
// status='sent' ⇒ group_id NOT NULL CHECK, and so analytics can locate the
// per-recipient engagement records. The single UPDATE is the duplicate-send
// guard — a concurrent send observes zero rows affected and is classified.
func (r *PostgresNewsletterRepo) MarkSending(ctx context.Context, id uuid.UUID, groupID string, totalRecipients int, expectedVersion int64) (*model.Newsletter, error) {
	updated := &model.Newsletter{}
	res, err := r.db.NewUpdate().
		Model(updated).
		Set("status = ?", model.StatusSending).
		Set("group_id = ?", groupID).
		Set("total_recipients = ?", totalRecipients).
		Set("updated_at = now()").
		Set("version = version + 1").
		Where("id = ? AND version = ? AND status = ?", id, expectedVersion, model.StatusDraft).
		Returning("*").
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("mark sending: %w", err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("mark sending rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return nil, r.classifySendTransitionMiss(ctx, id, expectedVersion)
	}
	return updated, nil
}

// MarkSent transitions sending → sent, gated on the version produced by
// MarkSending. group_id and total_recipients were persisted at the sending
// transition, so only the terminal status and sent_at remain to be written.
func (r *PostgresNewsletterRepo) MarkSent(ctx context.Context, id uuid.UUID, sentAt time.Time, expectedVersion int64) (*model.Newsletter, error) {
	updated := &model.Newsletter{}
	res, err := r.db.NewUpdate().
		Model(updated).
		Set("status = ?", model.StatusSent).
		Set("sent_at = ?", sentAt).
		Set("updated_at = now()").
		Set("version = version + 1").
		Where("id = ? AND version = ? AND status = ?", id, expectedVersion, model.StatusSending).
		Returning("*").
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("mark sent: %w", err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("mark sent rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return nil, r.classifySendTransitionMiss(ctx, id, expectedVersion)
	}
	return updated, nil
}

// RevertSending returns a sending newsletter to draft after a total fan-out
// failure. Clears the send-time fields so the reverted draft is
// indistinguishable from one that never attempted a send; the next attempt
// mints a fresh group_id.
func (r *PostgresNewsletterRepo) RevertSending(ctx context.Context, id uuid.UUID) error {
	res, err := r.db.NewUpdate().
		Model((*model.Newsletter)(nil)).
		Set("status = ?", model.StatusDraft).
		Set("group_id = NULL").
		Set("total_recipients = 0").
		Set("updated_at = now()").
		Set("version = version + 1").
		Where("id = ? AND status = ?", id, model.StatusSending).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("revert sending: %w", err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("revert sending rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// RecoverStuckSending marks sending rows whose updated_at is older than the
// given age as sent. See the port doc for why recovery lands on sent rather
// than draft. group_id was persisted at the sending transition, so the
// status='sent' ⇒ group_id NOT NULL CHECK holds.
func (r *PostgresNewsletterRepo) RecoverStuckSending(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	res, err := r.db.NewUpdate().
		Model((*model.Newsletter)(nil)).
		Set("status = ?", model.StatusSent).
		Set("sent_at = now()").
		Set("updated_at = now()").
		Set("version = version + 1").
		Where("status = ? AND updated_at < ?", model.StatusSending, cutoff).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("recover stuck sending: %w", err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("recover stuck sending rows affected: %w", err)
	}
	return rowsAffected, nil
}

// RecordOpen inserts a single open event. Repeat hits from the same recipient
// within the same hour collapse to a no-op via the
// uq_opens_newsletter_recipient_hour unique index — this bounds growth on the
// unauthenticated tracking pixel without losing unique-open counts.
func (r *PostgresNewsletterRepo) RecordOpen(ctx context.Context, newsletterID uuid.UUID, recipientHash string) error {
	open := &model.NewsletterOpen{
		NewsletterID:  newsletterID,
		RecipientHash: recipientHash,
	}
	if _, err := r.db.NewInsert().
		Model(open).
		On("CONFLICT ON CONSTRAINT uq_opens_newsletter_recipient_hour DO NOTHING").
		Exec(ctx); err != nil {
		return fmt.Errorf("record open: %w", err)
	}
	return nil
}

// Analytics aggregates the engagement metrics for a single newsletter. Returns
// ErrNotFound if the newsletter doesn't exist; returns zero counts (not an
// error) if it exists but has no opens recorded yet.
func (r *PostgresNewsletterRepo) Analytics(ctx context.Context, newsletterID uuid.UUID) (*model.Analytics, error) {
	n, err := r.Get(ctx, newsletterID)
	if err != nil {
		return nil, err
	}

	type aggRow struct {
		TotalOpens  int        `bun:"total_opens"`
		UniqueOpens int        `bun:"unique_opens"`
		LastEventAt *time.Time `bun:"last_event_at"`
	}
	agg := &aggRow{}
	err = r.db.NewSelect().
		ColumnExpr("COUNT(*) AS total_opens").
		ColumnExpr("COUNT(DISTINCT recipient_hash) AS unique_opens").
		ColumnExpr("MAX(opened_at) AS last_event_at").
		Table("newsletter_opens").
		Where("newsletter_id = ?", newsletterID).
		Scan(ctx, agg)
	if err != nil {
		return nil, fmt.Errorf("aggregate opens: %w", err)
	}

	type dailyRow struct {
		Day         time.Time `bun:"day"`
		Opens       int       `bun:"opens"`
		UniqueOpens int       `bun:"unique_opens"`
	}
	var dailyRows []dailyRow
	err = r.db.NewSelect().
		ColumnExpr("date_trunc('day', opened_at) AS day").
		ColumnExpr("COUNT(*) AS opens").
		ColumnExpr("COUNT(DISTINCT recipient_hash) AS unique_opens").
		Table("newsletter_opens").
		Where("newsletter_id = ?", newsletterID).
		GroupExpr("date_trunc('day', opened_at)").
		OrderExpr("date_trunc('day', opened_at) ASC").
		Scan(ctx, &dailyRows)
	if err != nil {
		return nil, fmt.Errorf("aggregate daily opens: %w", err)
	}

	daily := make([]model.DailyOpens, 0, len(dailyRows))
	for _, d := range dailyRows {
		daily = append(daily, model.DailyOpens{Date: d.Day, Opens: d.Opens, UniqueOpens: d.UniqueOpens})
	}

	delivered := n.TotalRecipients
	openRate := 0.0
	if delivered > 0 {
		openRate = float64(agg.UniqueOpens) / float64(delivered)
	}

	return &model.Analytics{
		NewsletterID:    n.ID,
		Subject:         n.Subject,
		Status:          n.Status,
		SentAt:          n.SentAt,
		TotalRecipients: n.TotalRecipients,
		Delivered:       delivered,
		Failed:          0,
		TotalOpens:      agg.TotalOpens,
		UniqueOpens:     agg.UniqueOpens,
		OpenRate:        openRate,
		DailyOpens:      daily,
		LastEventAt:     agg.LastEventAt,
	}, nil
}

// CreateUnsubscribe records a project-scoped opt-out. Idempotent: a second
// call for the same (project_uid, email) pair is a no-op via the unique
// index. Email is normalized to lowercase before insert so the index matches
// regardless of the case the recipient's mail client used in the URL.
func (r *PostgresNewsletterRepo) CreateUnsubscribe(ctx context.Context, projectUID, email string) error {
	row := &model.NewsletterUnsubscribe{
		ProjectUID: projectUID,
		Email:      strings.ToLower(strings.TrimSpace(email)),
	}
	if _, err := r.db.NewInsert().
		Model(row).
		On("CONFLICT (project_uid, email) DO NOTHING").
		Exec(ctx); err != nil {
		return fmt.Errorf("insert unsubscribe: %w", err)
	}
	return nil
}

// ListUnsubscribedEmails returns the set of lowercased email addresses that
// have opted out of newsletters for the given project. Returned as a map so
// the send orchestrator can filter the recipient list in O(1) per address.
func (r *PostgresNewsletterRepo) ListUnsubscribedEmails(ctx context.Context, projectUID string) (map[string]struct{}, error) {
	var emails []string
	err := r.db.NewSelect().
		Model((*model.NewsletterUnsubscribe)(nil)).
		Column("email").
		Where("project_uid = ?", projectUID).
		Scan(ctx, &emails)
	if err != nil {
		return nil, fmt.Errorf("list unsubscribes: %w", err)
	}
	out := make(map[string]struct{}, len(emails))
	for _, e := range emails {
		out[strings.ToLower(e)] = struct{}{}
	}
	return out, nil
}

// ListUnsubscribes returns all unsubscribes for the given project, ordered by
// created_at DESC. Returns the full NewsletterUnsubscribe model for API responses.
func (r *PostgresNewsletterRepo) ListUnsubscribes(ctx context.Context, projectUID string) ([]*model.NewsletterUnsubscribe, error) {
	var rows []*model.NewsletterUnsubscribe
	err := r.db.NewSelect().
		Model(&rows).
		Where("project_uid = ?", projectUID).
		Order("created_at DESC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list unsubscribes: %w", err)
	}
	return rows, nil
}

// DeleteUnsubscribe deletes a single opt-out record. If the id does not exist
// or belongs to a different project, returns domain.ErrNotFound.
func (r *PostgresNewsletterRepo) DeleteUnsubscribe(ctx context.Context, projectUID string, id uuid.UUID) error {
	result, err := r.db.NewDelete().
		Model((*model.NewsletterUnsubscribe)(nil)).
		Where("id = ? AND project_uid = ?", id, projectUID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete unsubscribe: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete unsubscribe: %w", err)
	}
	if rows == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// classifyMissing distinguishes ErrNotFound from ErrVersionMismatch after an
// Update affected zero rows.
func (r *PostgresNewsletterRepo) classifyMissing(ctx context.Context, id uuid.UUID) error {
	exists, err := r.db.NewSelect().
		Model((*model.Newsletter)(nil)).
		Where("id = ?", id).
		Exists(ctx)
	if err != nil {
		return fmt.Errorf("classify update miss: %w", err)
	}
	if !exists {
		return domain.ErrNotFound
	}
	return domain.ErrVersionMismatch
}

// classifySendTransitionMiss distinguishes the reasons a MarkSending or
// MarkSent update can affect zero rows: not found, already sent, another send
// in progress, or wrong version. Status checks take precedence over the
// version check because the send-transition UPDATEs bump the version — a row
// that moved to sending/sent necessarily has a different version too, and the
// status is the more actionable signal for the caller.
func (r *PostgresNewsletterRepo) classifySendTransitionMiss(ctx context.Context, id uuid.UUID, expectedVersion int64) error {
	existing := &model.Newsletter{}
	err := r.db.NewSelect().
		Model(existing).
		Where("id = ?", id).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("classify send transition miss: %w", err)
	}
	if existing.Status == model.StatusSent {
		return domain.ErrAlreadySent
	}
	if existing.Status == model.StatusSending {
		return domain.ErrSendInProgress
	}
	if existing.Version != expectedVersion {
		return domain.ErrVersionMismatch
	}
	// Unreachable in practice — fall back to version mismatch.
	return domain.ErrVersionMismatch
}

// listCursor is the keyset cursor encoded into NextPageToken for ListAll.
type listCursor struct {
	UpdatedAt time.Time `json:"u"`
	ID        uuid.UUID `json:"i"`
}

func encodeCursor(c listCursor) string {
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(token string) (listCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return listCursor{}, err
	}
	var c listCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return listCursor{}, err
	}
	return c, nil
}

// sentListCursor is the keyset cursor encoded into NextPageToken for
// ListSentByCommittee. Distinct from listCursor because that ordering is
// (updated_at, id) while committee-scoped lists order by (sent_at, id).
type sentListCursor struct {
	SentAt time.Time `json:"s"`
	ID     uuid.UUID `json:"i"`
}

func encodeSentCursor(c sentListCursor) string {
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeSentCursor(token string) (sentListCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return sentListCursor{}, err
	}
	var c sentListCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return sentListCursor{}, err
	}
	return c, nil
}
