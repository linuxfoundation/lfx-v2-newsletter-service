// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

// memberInsertBatch bounds the rows per INSERT. Each row binds four values and
// Postgres allows 65535 bind values per statement.
const memberInsertBatch = 2000

// maxAudienceErrorLength caps the stored failure message.
const maxAudienceErrorLength = 500

// PostgresAudienceRepo persists audience snapshots via bun.
type PostgresAudienceRepo struct {
	db *bun.DB
}

// NewPostgresAudienceRepo wires an audience repository over the given bun.DB.
func NewPostgresAudienceRepo(db *bun.DB) *PostgresAudienceRepo {
	return &PostgresAudienceRepo{db: db}
}

// audienceMemberRow is one row of newsletter_audience_members.
type audienceMemberRow struct {
	bun.BaseModel `bun:"table:newsletter_audience_members"`

	NewsletterID uuid.UUID `bun:"newsletter_id,pk,type:uuid"`
	Email        string    `bun:"email,pk"`
	FirstName    string    `bun:"first_name"`
	LastName     string    `bun:"last_name"`
}

// BeginResolve claims the audience for a new resolution. The statement inserts
// the row, or moves an existing 'ready' or 'failed' row back to 'processing'.
// The WHERE clause on the conflict branch leaves a 'processing' row untouched,
// so exactly one caller sees a changed row while a resolution is in flight, on
// any pod.
func (r *PostgresAudienceRepo) BeginResolve(ctx context.Context, newsletterID uuid.UUID, projectUID string, committeeUIDs []string) (bool, error) {
	uids, err := json.Marshal(committeeUIDs)
	if err != nil {
		return false, fmt.Errorf("marshal committee uids: %w", err)
	}
	res, err := r.db.ExecContext(ctx, `
INSERT INTO newsletter_audiences
    (newsletter_id, project_uid, committee_uids, status, recipient_count, skipped_no_email, error, requested_at, resolved_at, updated_at)
VALUES (?, ?, ?::jsonb, 'processing', 0, 0, NULL, now(), NULL, now())
ON CONFLICT (newsletter_id) DO UPDATE SET
    project_uid      = EXCLUDED.project_uid,
    committee_uids   = EXCLUDED.committee_uids,
    status           = 'processing',
    recipient_count  = 0,
    skipped_no_email = 0,
    error            = NULL,
    requested_at     = now(),
    resolved_at      = NULL,
    updated_at       = now()
WHERE newsletter_audiences.status <> 'processing'`,
		newsletterID, projectUID, string(uids))
	if err != nil {
		return false, fmt.Errorf("begin audience resolve: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("begin audience resolve rows affected: %w", err)
	}
	return n > 0, nil
}

// ReplaceMembers deletes the snapshot's members and inserts the new ones in one
// transaction. It first touches the audience row, which only matches while the
// row is 'processing' and the newsletter is still a draft. That stops a job
// whose row was already failed by the stale sweep, or whose newsletter has been
// sent meanwhile, from writing members. A running job is protected from the
// stale sweep by AUDIENCE_STALE_TTL exceeding AUDIENCE_JOB_TIMEOUT, not by this
// touch, which only happens once the reads have finished.
func (r *PostgresAudienceRepo) ReplaceMembers(ctx context.Context, newsletterID uuid.UUID, members []model.CommitteeMember) error {
	return r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE newsletter_audiences SET updated_at = now()
WHERE newsletter_id = ? AND status = 'processing'
  AND EXISTS (SELECT 1 FROM newsletters WHERE id = ? AND status = 'draft')`,
			newsletterID, newsletterID)
		if err != nil {
			return fmt.Errorf("touch audience: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("touch audience rows affected: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("%w: audience is not processing or the newsletter left draft", domain.ErrNotFound)
		}

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM newsletter_audience_members WHERE newsletter_id = ?`, newsletterID); err != nil {
			return fmt.Errorf("delete audience members: %w", err)
		}

		for start := 0; start < len(members); start += memberInsertBatch {
			end := min(start+memberInsertBatch, len(members))
			rows := make([]audienceMemberRow, 0, end-start)
			for _, m := range members[start:end] {
				rows = append(rows, audienceMemberRow{
					NewsletterID: newsletterID,
					Email:        m.Email,
					FirstName:    m.FirstName,
					LastName:     m.LastName,
				})
			}
			if _, err := tx.NewInsert().Model(&rows).On("CONFLICT (newsletter_id, email) DO NOTHING").Exec(ctx); err != nil {
				return fmt.Errorf("insert audience members: %w", err)
			}
		}
		return nil
	})
}

// DeleteMembers removes every member of the newsletter's snapshot. It is called
// once the newsletter is sent, so the snapshot keeps its counts but no longer
// holds member emails or names. Deleting an absent snapshot is not an error.
func (r *PostgresAudienceRepo) DeleteMembers(ctx context.Context, newsletterID uuid.UUID) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM newsletter_audience_members WHERE newsletter_id = ?`, newsletterID); err != nil {
		return fmt.Errorf("delete audience members: %w", err)
	}
	return nil
}

// MarkReady completes a resolution. It matches only a 'processing' row.
func (r *PostgresAudienceRepo) MarkReady(ctx context.Context, newsletterID uuid.UUID, count, skippedNoEmail int) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE newsletter_audiences
SET status = 'ready', recipient_count = ?, skipped_no_email = ?, error = NULL,
    resolved_at = now(), updated_at = now()
WHERE newsletter_id = ? AND status = 'processing'`,
		count, skippedNoEmail, newsletterID)
	if err != nil {
		return fmt.Errorf("mark audience ready: %w", err)
	}
	return requireProcessingRow(res, "mark audience ready")
}

// MarkFailed ends a resolution with a failure message. It matches only a
// 'processing' row.
func (r *PostgresAudienceRepo) MarkFailed(ctx context.Context, newsletterID uuid.UUID, msg string) error {
	if runes := []rune(msg); len(runes) > maxAudienceErrorLength {
		msg = string(runes[:maxAudienceErrorLength])
	}
	res, err := r.db.ExecContext(ctx, `
UPDATE newsletter_audiences
SET status = 'failed', error = ?, updated_at = now()
WHERE newsletter_id = ? AND status = 'processing'`,
		msg, newsletterID)
	if err != nil {
		return fmt.Errorf("mark audience failed: %w", err)
	}
	return requireProcessingRow(res, "mark audience failed")
}

func requireProcessingRow(res sql.Result, op string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s rows affected: %w", op, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: audience is not processing", domain.ErrNotFound)
	}
	return nil
}

// Get returns the audience for a newsletter, or domain.ErrNotFound.
func (r *PostgresAudienceRepo) Get(ctx context.Context, newsletterID uuid.UUID) (*model.Audience, error) {
	var (
		a          model.Audience
		uidsJSON   string
		status     string
		errMsg     sql.NullString
		resolvedAt sql.NullTime
	)
	err := r.db.QueryRowContext(ctx, `
SELECT newsletter_id, project_uid, committee_uids::text, status, recipient_count,
       skipped_no_email, error, requested_at, resolved_at, updated_at
FROM newsletter_audiences WHERE newsletter_id = ?`, newsletterID).
		Scan(&a.NewsletterID, &a.ProjectUID, &uidsJSON, &status, &a.RecipientCount,
			&a.SkippedNoEmail, &errMsg, &a.RequestedAt, &resolvedAt, &a.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("select audience: %w", err)
	}
	if err := json.Unmarshal([]byte(uidsJSON), &a.CommitteeUIDs); err != nil {
		return nil, fmt.Errorf("decode audience committee uids: %w", err)
	}
	a.Status = model.AudienceStatus(status)
	a.Error = errMsg.String
	if resolvedAt.Valid {
		t := resolvedAt.Time
		a.ResolvedAt = &t
	}
	return &a, nil
}

// ListMembers returns the snapshot members ordered by email.
func (r *PostgresAudienceRepo) ListMembers(ctx context.Context, newsletterID uuid.UUID) ([]model.CommitteeMember, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT email, first_name, last_name FROM newsletter_audience_members
WHERE newsletter_id = ? ORDER BY email`, newsletterID)
	if err != nil {
		return nil, fmt.Errorf("select audience members: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]model.CommitteeMember, 0)
	for rows.Next() {
		var m model.CommitteeMember
		if err := rows.Scan(&m.Email, &m.FirstName, &m.LastName); err != nil {
			return nil, fmt.Errorf("scan audience member: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audience members: %w", err)
	}
	return out, nil
}

// FailStale fails 'processing' rows that have not moved for olderThan. The
// UPDATE is status and time gated, so concurrent sweeps on several pods fail

// PurgeSentMembers deletes the snapshot members of every newsletter that is
// sent or scheduled. The send purges inline; this catches the paths that do not
// (scheduled sends settled by the sweep, rows recovered from 'sending', a failed
// inline purge). A 'sending' newsletter keeps its members, because a send that
// fails for every recipient reverts it to draft.
func (r *PostgresAudienceRepo) PurgeSentMembers(ctx context.Context) (int, error) {
	res, err := r.db.ExecContext(ctx, `
DELETE FROM newsletter_audience_members m
USING newsletters n
WHERE m.newsletter_id = n.id AND n.status IN ('sent', 'scheduled')`)
	if err != nil {
		return 0, fmt.Errorf("purge sent audience members: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("purge sent audience members rows affected: %w", err)
	}
	return int(n), nil
}

// each row once.
func (r *PostgresAudienceRepo) FailStale(ctx context.Context, olderThan time.Duration) (int, error) {
	res, err := r.db.ExecContext(ctx, `
UPDATE newsletter_audiences
SET status = 'failed', error = 'audience resolution did not finish', updated_at = now()
WHERE status = 'processing' AND updated_at < now() - make_interval(secs => ?)`,
		olderThan.Seconds())
	if err != nil {
		return 0, fmt.Errorf("fail stale audiences: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("fail stale audiences rows affected: %w", err)
	}
	return int(n), nil
}
