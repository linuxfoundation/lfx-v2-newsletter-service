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
		// body_layout can approach the 1 MiB request cap; list rows discard it
		// (see toAPIListItem), so never materialize it here — a 100-row page would
		// otherwise fetch up to ~100 MiB of JSON only to throw it away. Get/single
		// -row paths still SELECT it (they need the layout).
		ExcludeColumn("body_layout").
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
		// body_layout can approach the 1 MiB request cap; committee-list rows
		// discard it (see toAPICommitteeNewsletter), so never materialize it here
		// — matching ListAll.
		ExcludeColumn("body_layout").
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
	// Guard against legacy-pod mutations of armed scheduled rows during rolling
	// deploys. Armed rows (batch_id IS NOT NULL) are managed by the scheduled-sends
	// feature and must not be updated by older code paths (LFXV2-2685).
	current := &model.Newsletter{}
	err := r.db.NewSelect().
		Model(current).
		Where("id = ?", n.ID).
		Scan(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("check armed status: %w", err)
	}
	if err == nil && current.BatchID != nil {
		return nil, fmt.Errorf("%w: cannot update armed scheduled row", domain.ErrInvalidRequest)
	}

	res, err := r.db.NewUpdate().
		Model(n).
		Set("subject = ?", n.Subject).
		Set("body_html = ?", n.BodyHTML).
		// body_layout is the editor's structured layout; nil persists SQL NULL so
		// updating a layout-less draft doesn't write an empty JSONB value.
		Set("body_layout = ?", nullableJSONB(n.BodyLayout)).
		Set("ed_reply_email = ?", n.EDReplyEmail).
		// pgdialect.Array forces a Postgres text[] literal; without it bun
		// json-encodes the slice and PG raises a "malformed array literal".
		Set("committee_uids = ?", pgdialect.Array(n.CommitteeUIDs)).
		Set("project_uid = ?", n.ProjectUID).
		// Full replace: an omitted/null scheduled_at clears it, consistent with
		// every other field here (LFXV2-2685).
		Set("scheduled_at = ?", n.ScheduledAt).
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
// Rejects deletion of armed scheduled rows (batch_id IS NOT NULL) to prevent
// legacy-pod mutations during rolling deploys (LFXV2-2685).
func (r *PostgresNewsletterRepo) Delete(ctx context.Context, id uuid.UUID) error {
	// Atomically delete only a still-draft row to avoid a TOCTOU race where
	// MarkSending could claim the row (immediate or scheduled) between the
	// caller's status check and this delete. batch_id IS NULL alone is not
	// enough: it only rules out an armed *scheduled* row, but an immediate
	// send's sending row also has a NULL batch_id and must not be deletable
	// out from under an in-flight fan-out.
	res, err := r.db.NewDelete().
		Model((*model.Newsletter)(nil)).
		Where("id = ? AND status = ?", id, model.StatusDraft).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete newsletter: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete newsletter rows affected: %w", err)
	}
	if n == 0 {
		// The row doesn't exist OR it has moved past draft (sending, scheduled,
		// or sent). Distinguish the error by checking the row.
		current := &model.Newsletter{}
		err := r.db.NewSelect().
			Model(current).
			Where("id = ?", id).
			Scan(ctx)
		if err != nil && errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("classify delete miss: %w", err)
		}
		// Row exists but is no longer a draft (raced past our caller's check).
		switch current.Status {
		case model.StatusSent:
			return domain.ErrAlreadySent
		case model.StatusScheduled:
			return domain.ErrScheduled
		default:
			return domain.ErrSendInProgress
		}
	}
	return nil
}

// MarkSending transitions a draft to status=sending atomically, gated on the
// expected version. Captures the audience size, the dispatching provider
// (send_provider), and the correlation group_id at acceptance time: group_id
// must be persisted before any email is
// dispatched so a crash-recovered row can be marked sent without violating the
// status='sent' ⇒ group_id NOT NULL CHECK, and so analytics can locate the
// per-recipient engagement records. The single UPDATE is the duplicate-send
// guard — a concurrent send observes zero rows affected and is classified.
//
// scheduledAt/batchID (LFXV2-2685) are persisted in the same UPDATE when this
// fan-out is arming a scheduled release, so a crash immediately afterwards
// still leaves RecoverStuckSending enough state to settle to 'scheduled'
// rather than 'sent'. Nil/empty for an immediate send.
func (r *PostgresNewsletterRepo) MarkSending(ctx context.Context, id uuid.UUID, groupID, sendProvider string, totalRecipients int, expectedVersion int64, scheduledAt *time.Time, batchID string) (*model.Newsletter, error) {
	updated := &model.Newsletter{}

	// Wrap the GUC flag and UPDATE in the same transaction so the trigger sees it.
	// If scheduling, authorize armed mutations by setting the GUC before the update.
	if scheduledAt != nil {
		err := r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			// Set the GUC flag in this transaction
			if _, err := tx.NewRaw("SET LOCAL app.allow_armed_mutation = 'true'").Exec(ctx); err != nil {
				return fmt.Errorf("set armed mutation flag: %w", err)
			}
			// Now run the UPDATE in the same transaction
			res, err := tx.NewUpdate().
				Model(updated).
				Set("status = ?", model.StatusSending).
				Set("group_id = ?", groupID).
				Set("send_provider = ?", sendProvider).
				Set("total_recipients = ?", totalRecipients).
				Set("updated_at = now()").
				Set("version = version + 1").
				Set("scheduled_at = ?", *scheduledAt).
				Set("batch_id = ?", batchID).
				Where("id = ? AND version = ? AND status = ?", id, expectedVersion, model.StatusDraft).
				Returning("*").
				Exec(ctx)
			if err != nil {
				return fmt.Errorf("mark sending: %w", err)
			}
			rowsAffected, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("mark sending rows affected: %w", err)
			}
			if rowsAffected == 0 {
				// Classify through the transaction to avoid pool exhaustion when
				// concurrent schedule requests all hold transactions waiting for
				// another pool connection (LFXV2-2685).
				return r.classifySendTransitionMissWithDB(ctx, tx, id, expectedVersion)
			}
			return nil
		})
		return updated, err
	}

	// Non-scheduled send: no GUC needed, just run the UPDATE directly
	res, err := r.db.NewUpdate().
		Model(updated).
		Set("status = ?", model.StatusSending).
		Set("group_id = ?", groupID).
		Set("send_provider = ?", sendProvider).
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
// A zero-recipient scheduled send settles straight to sent with a non-nil
// batch_id still on the row, so the GUC must be set unconditionally here —
// same as MarkScheduled/SettleDueScheduled. It is a no-op when batch_id is
// NULL, since the trigger only checks OLD.batch_id IS NOT NULL.
func (r *PostgresNewsletterRepo) MarkSent(ctx context.Context, id uuid.UUID, sentAt time.Time, expectedVersion int64) (*model.Newsletter, error) {
	updated := &model.Newsletter{}
	err := r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewRaw("SET LOCAL app.allow_armed_mutation = 'true'").Exec(ctx); err != nil {
			return fmt.Errorf("set armed mutation flag: %w", err)
		}
		res, err := tx.NewUpdate().
			Model(updated).
			Set("status = ?", model.StatusSent).
			Set("sent_at = ?", sentAt).
			Set("updated_at = now()").
			Set("version = version + 1").
			Where("id = ? AND version = ? AND status = ?", id, expectedVersion, model.StatusSending).
			Returning("*").
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("mark sent: %w", err)
		}
		rowsAffected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("mark sent rows affected: %w", err)
		}
		if rowsAffected == 0 {
			return r.classifySendTransitionMissWithDB(ctx, tx, id, expectedVersion)
		}
		return nil
	})
	return updated, err
}

// MarkScheduled transitions sending → scheduled, gated on the version
// produced by MarkSending (LFXV2-2685). scheduled_at and batch_id were
// already persisted at the sending transition, so only the terminal status
// remains to be written.
func (r *PostgresNewsletterRepo) MarkScheduled(ctx context.Context, id uuid.UUID, expectedVersion int64) (*model.Newsletter, error) {
	updated := &model.Newsletter{}
	// Wrap GUC and UPDATE in the same transaction so the trigger sees the flag.
	err := r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewRaw("SET LOCAL app.allow_armed_mutation = 'true'").Exec(ctx); err != nil {
			return fmt.Errorf("set armed mutation flag: %w", err)
		}
		res, err := tx.NewUpdate().
			Model(updated).
			Set("status = ?", model.StatusScheduled).
			Set("updated_at = now()").
			Set("version = version + 1").
			Where("id = ? AND version = ? AND status = ?", id, expectedVersion, model.StatusSending).
			Returning("*").
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("mark scheduled: %w", err)
		}
		rowsAffected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("mark scheduled rows affected: %w", err)
		}
		if rowsAffected == 0 {
			return r.classifySendTransitionMissWithDB(ctx, tx, id, expectedVersion)
		}
		return nil
	})
	return updated, err
}

// RevertSending returns a sending newsletter to draft after a total fan-out
// failure. Clears the send-time fields so the reverted draft is
// indistinguishable from one that never attempted a send; the next attempt
// mints a fresh group_id.
func (r *PostgresNewsletterRepo) RevertSending(ctx context.Context, id uuid.UUID) error {
	// Wrap GUC and UPDATE in the same transaction so the trigger sees the flag.
	return r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewRaw("SET LOCAL app.allow_armed_mutation = 'true'").Exec(ctx); err != nil {
			return fmt.Errorf("set armed mutation flag: %w", err)
		}
		res, err := tx.NewUpdate().
			Model((*model.Newsletter)(nil)).
			Set("status = ?", model.StatusDraft).
			Set("group_id = NULL").
			Set("batch_id = NULL").
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
	})
}

// RevertScheduled cancels an armed schedule and returns the newsletter to
// draft (LFXV2-2685), gated on expectedVersion since this transition is
// user-initiated (CancelScheduled) rather than a crash-recovery sweep.
// Accepts both scheduled (waiting at provider) and sending (fan-out in
// progress) states to handle cancellation of in-flight batches. Clears
// group_id/batch_id and resets total_recipients, but deliberately RETAINS
// scheduled_at: the cancelled newsletter still carries the author's saved
// intent, which they may edit or re-arm via a fresh /schedule call.
// batchID ties the revert to the specific batch that was cancelled, preventing
// a delayed duplicate cancel from reverting a newer send (LFXV2-2685).
func (r *PostgresNewsletterRepo) RevertScheduled(ctx context.Context, id uuid.UUID, expectedVersion int64, batchID *string) (*model.Newsletter, error) {
	updated := &model.Newsletter{}
	// Wrap GUC and UPDATE in the same transaction so the trigger sees the flag.
	err := r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewRaw("SET LOCAL app.allow_armed_mutation = 'true'").Exec(ctx); err != nil {
			return fmt.Errorf("set armed mutation flag: %w", err)
		}
		// Tolerate version bumps from concurrent fan-out settlement, but still require
		// the status to be in the right state to revert (scheduled or sending).
		// During a rolling deploy, a cancel request might race against a background
		// job that's already advanced the version while settling the send. Both must
		// succeed without corrupting state: if the row is still scheduled/sending,
		// revert it regardless of version; if it has transitioned to sent, fail with
		// ErrAlreadySent, not ErrVersionMismatch. Predicate on batch_id to ensure a
		// delayed duplicate cancel reverts only the same batch, not a newer send.
		res, err := tx.NewUpdate().
			Model(updated).
			Set("status = ?", model.StatusDraft).
			Set("group_id = NULL").
			Set("batch_id = NULL").
			Set("total_recipients = 0").
			Set("updated_at = now()").
			Set("version = version + 1").
			Where("id = ? AND status IN (?, ?) AND batch_id = ?", id, model.StatusScheduled, model.StatusSending, batchID).
			Returning("*").
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("revert scheduled: %w", err)
		}
		rowsAffected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("revert scheduled rows affected: %w", err)
		}
		if rowsAffected == 0 {
			// The update didn't match any rows. The row might not exist, might be in
			// a different status (e.g. sent, draft), or might have been deleted.
			// Classify the miss to return the appropriate error. Use the transaction
			// context to avoid holding the transaction while acquiring another pool
			// connection, which would risk pool exhaustion (LFXV2-2685).
			return r.classifySendTransitionMissWithDB(ctx, tx, id, expectedVersion)
		}
		return nil
	})
	return updated, err
}

// SettleDueScheduled marks scheduled rows whose scheduled_at has passed as
// sent (LFXV2-2685). Reconciliation of our display state only — SendGrid owns
// the actual release timing; this just lets the row's status catch up to
// what has (or should have) already happened. group_id was persisted at the
// sending transition, so the status='sent' ⇒ group_id NOT NULL CHECK holds.
func (r *PostgresNewsletterRepo) SettleDueScheduled(ctx context.Context, now time.Time) (int64, error) {
	var rowsAffected int64
	// Wrap GUC and UPDATE in the same transaction so the trigger sees the flag.
	err := r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewRaw("SET LOCAL app.allow_armed_mutation = 'true'").Exec(ctx); err != nil {
			return fmt.Errorf("set armed mutation flag: %w", err)
		}
		res, err := tx.NewUpdate().
			Model((*model.Newsletter)(nil)).
			Set("status = ?", model.StatusSent).
			Set("sent_at = scheduled_at").
			Set("updated_at = now()").
			Set("version = version + 1").
			Where("status = ? AND scheduled_at <= ?", model.StatusScheduled, now).
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("settle due scheduled: %w", err)
		}
		var err2 error
		rowsAffected, err2 = res.RowsAffected()
		if err2 != nil {
			return fmt.Errorf("settle due scheduled rows affected: %w", err2)
		}
		return nil
	})
	return rowsAffected, err
}

// RecoverStuckSending marks sending rows whose updated_at is older than the
// given age as sent, except rows with an armed SendGrid batch (batch_id IS
// NOT NULL), which settle to scheduled instead (LFXV2-2685): the provider is
// still holding those messages for a future release, so flipping straight to
// sent would misreport our display state (and the next SettleDueScheduled
// sweep will catch up once scheduled_at actually passes). Keyed off batch_id,
// not scheduled_at, because MarkSending preserves saved scheduled_at on
// immediate sends; a crash during MarkSending would misclassify an immediate
// send with saved-but-unarmed intent as scheduled if we checked scheduled_at
// instead. See the port doc for why recovery lands on sent (or scheduled)
// rather than draft. group_id was persisted at the sending transition, so the
// status='sent' ⇒ group_id NOT NULL CHECK holds either way.
func (r *PostgresNewsletterRepo) RecoverStuckSending(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	res, err := r.db.NewUpdate().
		Model((*model.Newsletter)(nil)).
		Set("status = ?", model.StatusSent).
		Set("sent_at = now()").
		Set("updated_at = now()").
		Set("version = version + 1").
		Where("status = ? AND updated_at < ? AND batch_id IS NULL", model.StatusSending, cutoff).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("recover stuck sending: %w", err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("recover stuck sending rows affected: %w", err)
	}

	// This branch touches armed rows (batch_id IS NOT NULL), so the GUC must
	// be set in the same transaction as the UPDATE or the trigger rejects it —
	// stranding every crashed scheduled fan-out in 'sending' forever.
	var scheduledAffected int64
	err = r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewRaw("SET LOCAL app.allow_armed_mutation = 'true'").Exec(ctx); err != nil {
			return fmt.Errorf("set armed mutation flag: %w", err)
		}
		scheduledRes, err := tx.NewUpdate().
			Model((*model.Newsletter)(nil)).
			Set("status = ?", model.StatusScheduled).
			Set("updated_at = now()").
			Set("version = version + 1").
			Where("status = ? AND updated_at < ? AND batch_id IS NOT NULL", model.StatusSending, cutoff).
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("recover stuck sending (scheduled): %w", err)
		}
		scheduledAffected, err = scheduledRes.RowsAffected()
		if err != nil {
			return fmt.Errorf("recover stuck sending (scheduled) rows affected: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return rowsAffected + scheduledAffected, nil
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
//
// Use classifySendTransitionMissWithDB to classify through a transaction,
// avoiding pool connection exhaustion when classifying within a transaction.
func (r *PostgresNewsletterRepo) classifySendTransitionMiss(ctx context.Context, id uuid.UUID, expectedVersion int64) error {
	return r.classifySendTransitionMissWithDB(ctx, r.db, id, expectedVersion)
}

// classifySendTransitionMissWithDB classifies through an arbitrary bun.IDB
// (pool or transaction). Use this when classifying within a transaction to
// avoid holding a transaction open while acquiring another pool connection
// (pool exhaustion / deadlock risk).
func (r *PostgresNewsletterRepo) classifySendTransitionMissWithDB(ctx context.Context, db bun.IDB, id uuid.UUID, expectedVersion int64) error {
	existing := &model.Newsletter{}
	err := db.NewSelect().
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
	if existing.Status == model.StatusScheduled {
		return domain.ErrScheduled
	}
	if existing.Version != expectedVersion {
		return domain.ErrVersionMismatch
	}
	// Unreachable in practice — fall back to version mismatch.
	return domain.ErrVersionMismatch
}

// nullableJSONB normalizes a raw JSON value for a nullable JSONB column. An
// empty or nil payload becomes an untyped nil so the driver writes SQL NULL —
// a zero-length []byte would otherwise be sent as an empty string, which JSONB
// rejects.
func nullableJSONB(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
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
	// json.Unmarshal happily accepts `{}` or `null`, leaving zero fields; a
	// zero (sent_at, id) cursor would silently produce an empty/mispositioned
	// page instead of surfacing as an invalid token. No legitimate cursor has
	// either field zero — encodeSentCursor only runs for sent rows.
	if c.SentAt.IsZero() || c.ID == uuid.Nil {
		return sentListCursor{}, errors.New("cursor fields must be non-zero")
	}
	return c, nil
}
