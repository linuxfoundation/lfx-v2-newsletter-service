// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/uptrace/bun"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// PostgresPublicationRepo persists NewsletterPublication aggregates in PostgreSQL via bun.
type PostgresPublicationRepo struct {
	db *bun.DB
}

// NewPostgresPublicationRepo wires a Publication repository over the given bun.DB.
func NewPostgresPublicationRepo(db *bun.DB) *PostgresPublicationRepo {
	return &PostgresPublicationRepo{db: db}
}

// Create inserts a new NewsletterPublication row. Database defaults populate id, version,
// timestamps; bun copies the generated values back into pub.
func (r *PostgresPublicationRepo) Create(ctx context.Context, pub *model.NewsletterPublication) error {
	if _, err := r.db.NewInsert().
		Model(pub).
		Returning("*").
		Exec(ctx); err != nil {
		// A duplicate (project_uid, slug) is caller-driven input, not a server
		// fault — surface it as a 409 conflict rather than a raw 500.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uq_publications_project_slug" {
			return pkgerrors.NewConflict(fmt.Sprintf("a publication with slug %q already exists in this project", pub.Slug), err)
		}
		return fmt.Errorf("insert publication: %w", err)
	}
	return nil
}

// Get fetches a single publication by project_uid and id.
func (r *PostgresPublicationRepo) Get(ctx context.Context, projectUID string, id uuid.UUID) (*model.NewsletterPublication, error) {
	pub := &model.NewsletterPublication{}
	err := r.db.NewSelect().
		Model(pub).
		Where("p.project_uid = ? AND p.id = ?", projectUID, id).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("select publication: %w", err)
	}
	return pub, nil
}

// List returns all publications in the given project, newest first.
func (r *PostgresPublicationRepo) List(ctx context.Context, filters port.PublicationListFilters) (*port.PublicationListPage, error) {
	limit := filters.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}

	// Fetch one extra row to detect whether a further page exists without a
	// second COUNT query.
	q := r.db.NewSelect().
		Model((*model.NewsletterPublication)(nil)).
		Where("project_uid = ?", filters.ProjectUID).
		Order("created_at DESC").
		Order("id DESC").
		Limit(limit + 1)

	if filters.PageToken != "" {
		cursor, err := decodePublicationCursor(filters.PageToken)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid pageToken", domain.ErrInvalidRequest)
		}
		// Keyset pagination: continue from the (created_at, id) tuple of the last
		// row of the previous page. Tuple comparison handles ties on created_at,
		// which a bare OFFSET would skip or duplicate under concurrent inserts.
		q = q.Where("(created_at, id) < (?, ?)", cursor.CreatedAt, cursor.ID)
	}

	var rows []*model.NewsletterPublication
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list publications: %w", err)
	}

	page := &port.PublicationListPage{Publications: rows}
	if len(rows) > limit {
		last := rows[limit-1]
		page.Publications = rows[:limit]
		page.NextPageToken = encodePublicationCursor(publicationListCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	return page, nil
}

// publicationListCursor is the keyset cursor encoded into NextPageToken for the
// publication list. Distinct from listCursor because publications order by
// (created_at, id) rather than (updated_at, id).
type publicationListCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        uuid.UUID `json:"i"`
}

func encodePublicationCursor(c publicationListCursor) string {
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodePublicationCursor(token string) (publicationListCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return publicationListCursor{}, err
	}
	var c publicationListCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return publicationListCursor{}, err
	}
	// A token that decodes but carries no keyset position is rejected rather
	// than used. An encoded "{}" or "null" unmarshals without error and leaves
	// both fields zero, and the resulting
	// "(created_at, id) < ('0001-01-01', uuid-zero)" filter would return an
	// empty page for a project that has publications. Both fields are always
	// populated by encodePublicationCursor, so a zero value means the token was
	// not one we issued.
	if c.CreatedAt.IsZero() || c.ID == uuid.Nil {
		return publicationListCursor{}, errors.New("cursor fields must be non-zero")
	}
	return c, nil
}

// GetDefault returns the default publication for the given project.
func (r *PostgresPublicationRepo) GetDefault(ctx context.Context, projectUID string) (*model.NewsletterPublication, error) {
	pub := &model.NewsletterPublication{}
	err := r.db.NewSelect().
		Model(pub).
		Where("project_uid = ? AND is_default = true", projectUID).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("select default publication: %w", err)
	}
	return pub, nil
}

// Update applies optimistic-locking-aware mutations. The query gates on
// (id, expectedVersion) and atomically increments version. If no rows are
// affected, the method follows up with an existence check to disambiguate
// ErrNotFound vs ErrVersionMismatch.
func (r *PostgresPublicationRepo) Update(ctx context.Context, pub *model.NewsletterPublication, expectedVersion int64) error {
	res, err := r.db.NewUpdate().
		Model(pub).
		Set("name = ?", pub.Name).
		Set("slug = ?", pub.Slug).
		Set("wrapper_content = ?", pub.WrapperContent).
		Set("template_set_id = ?", pub.TemplateSetID).
		// editor_type and sender_email are part of the same partial update the
		// service builds, so they belong in the SET list. Without them a caller
		// changing the composer or the From address would get a 200 and a
		// response body showing the new value, while the row kept the old one.
		Set("editor_type = ?", pub.EditorType).
		Set("sender_email = ?", pub.SenderEmail).
		Set("view_online_base = ?", pub.ViewOnlineBase).
		Set("updated_at = now()").
		Set("version = version + 1").
		Where("id = ? AND project_uid = ? AND version = ?", pub.ID, pub.ProjectUID, expectedVersion).
		Returning("*").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("update publication: %w", err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update publication rows affected: %w", err)
	}
	if rowsAffected == 0 {
		// Check if the record exists
		exists, err := r.db.NewSelect().
			Model((*model.NewsletterPublication)(nil)).
			Where("id = ? AND project_uid = ?", pub.ID, pub.ProjectUID).
			Exists(ctx)
		if err != nil {
			return fmt.Errorf("check publication existence: %w", err)
		}
		if !exists {
			return domain.ErrNotFound
		}
		return domain.ErrVersionMismatch
	}

	return nil
}

// BackfillLegacyPublications files editions written before publications existed,
// and is safe to call repeatedly. It is the same work schema.Apply does at
// startup, exposed so a periodic sweep can close the one window startup cannot:
// a legacy pod that writes a row AFTER the last new pod has already applied the
// schema. Without the sweep such a row stays unfiled until the next deploy.
//
// Re-running is safe because it only ever touches rows with
// publication_id_set = false. The current code stamps that flag on every insert
// and on any explicit file/unfile, so a row a user deliberately unfiled carries
// the flag and is invisible here. Only a legacy writer, which does not know the
// column, can leave it false. That is what makes this a sweep rather than the
// reconciliation loop an earlier revision had, which could not tell the two
// apart and re-filed deliberate unfilings on every restart.
//
// Returns the number of editions filed.
func (r *PostgresPublicationRepo) BackfillLegacyPublications(ctx context.Context) (int64, error) {
	var filed int64
	err := r.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// Give each project that has legacy editions somewhere to put them.
		if _, err := tx.NewRaw(`
            INSERT INTO newsletter_publications (project_uid, slug, name, is_default, created_by)
            SELECT DISTINCT n.project_uid, 'default', 'Newsletter', true, 'system-backfill'
            FROM newsletters n
            WHERE n.publication_id IS NULL
              AND NOT n.publication_id_set
              AND NOT EXISTS (
                SELECT 1 FROM newsletter_publications p
                WHERE p.project_uid = n.project_uid AND p.is_default
              )
            ON CONFLICT (project_uid, slug) DO UPDATE
                SET is_default = true,
                    version = newsletter_publications.version + 1,
                    updated_at = now()`).Exec(ctx); err != nil {
			return fmt.Errorf("backfill sweep: create default publications: %w", err)
		}

		// The attach is an UPDATE on `newsletters`, so it trips the armed-schedule
		// guard for any row with a live batch_id. Same authorization the startup
		// path uses, and for the same reason: this only populates publication_id
		// and touches no scheduling state.
		if _, err := tx.NewRaw(`SET LOCAL app.allow_armed_mutation = 'true'`).Exec(ctx); err != nil {
			return fmt.Errorf("backfill sweep: authorize armed mutation: %w", err)
		}

		// version moves because publication_id is part of the resource a caller
		// holds an ETag for. updated_at deliberately does not: bumping it would
		// reshuffle the (updated_at, id) keyset order of every legacy edition and
		// shove them to the top of the list.
		res, err := tx.NewRaw(`
            UPDATE newsletters n
            SET publication_id = p.id,
                publication_id_set = true,
                version = n.version + 1
            FROM newsletter_publications p
            WHERE n.publication_id IS NULL
              AND NOT n.publication_id_set
              AND p.project_uid = n.project_uid
              AND p.is_default`).Exec(ctx)
		if err != nil {
			return fmt.Errorf("backfill sweep: attach editions: %w", err)
		}
		filed, err = res.RowsAffected()
		if err != nil {
			return fmt.Errorf("backfill sweep: rows affected: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return filed, nil
}
