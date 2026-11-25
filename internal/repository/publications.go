// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// PostgresPublicationRepo persists Publications in PostgreSQL via bun.
type PostgresPublicationRepo struct {
	db *bun.DB
}

// NewPostgresPublicationRepo wires a Publication repository over the given bun.DB.
func NewPostgresPublicationRepo(db *bun.DB) *PostgresPublicationRepo {
	return &PostgresPublicationRepo{db: db}
}

// CreatePublication inserts a publication. A nil CommitteeUIDs would violate the
// column's NOT NULL, so callers must pass an empty slice instead (the service
// always normalizes it).
func (r *PostgresPublicationRepo) CreatePublication(ctx context.Context, p *model.Publication) error {
	if _, err := r.db.NewInsert().Model(p).Returning("*").Exec(ctx); err != nil {
		return fmt.Errorf("insert publication: %w", err)
	}
	return nil
}

// GetPublication fetches one publication within a project.
func (r *PostgresPublicationRepo) GetPublication(ctx context.Context, projectUID string, id uuid.UUID) (*model.Publication, error) {
	p := &model.Publication{}
	err := r.db.NewSelect().
		Model(p).
		Where("p.id = ? AND p.project_uid = ?", id, projectUID).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("select publication: %w", err)
	}
	return p, nil
}

// ListPublications returns a keyset page of the project's named publications
// ordered by updated_at DESC.
func (r *PostgresPublicationRepo) ListPublications(ctx context.Context, projectUID, pageToken string) (*port.PublicationListPage, error) {
	q := r.db.NewSelect().
		Model((*model.Publication)(nil)).
		Where("p.project_uid = ? AND NOT p.implicit", projectUID).
		Order("updated_at DESC").
		Order("id DESC").
		Limit(defaultListLimit + 1)

	if pageToken != "" {
		cursor, err := decodeCursor(pageToken)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid pageToken", domain.ErrInvalidRequest)
		}
		q = q.Where("(updated_at, id) < (?, ?)", cursor.UpdatedAt, cursor.ID)
	}

	var rows []*model.Publication
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list publications: %w", err)
	}

	page := &port.PublicationListPage{Publications: rows}
	if len(rows) > defaultListLimit {
		last := rows[defaultListLimit-1]
		page.Publications = rows[:defaultListLimit]
		page.NextPageToken = encodeCursor(listCursor{UpdatedAt: last.UpdatedAt, ID: last.ID})
	}
	return page, nil
}

// UpdatePublication full-replaces the editable fields of a publication, gated
// on (id, project, expectedVersion), and increments version.
func (r *PostgresPublicationRepo) UpdatePublication(ctx context.Context, p *model.Publication, expectedVersion int64) (*model.Publication, error) {
	res, err := r.db.NewUpdate().
		Model(p).
		Set("name = ?", p.Name).
		Set("template_key = ?", p.TemplateKey).
		Set("from_name = ?", p.FromName).
		Set("reply_to_email = ?", p.ReplyToEmail).
		// pgdialect.Array: see the committee_uids Set in Update.
		Set("committee_uids = ?", pgdialect.Array(p.CommitteeUIDs)).
		Set("updated_at = now()").
		Set("version = version + 1").
		Where("id = ? AND project_uid = ? AND version = ?", p.ID, p.ProjectUID, expectedVersion).
		Returning("*").
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("update publication: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("update publication rows affected: %w", err)
	}
	if affected == 0 {
		if _, getErr := r.GetPublication(ctx, p.ProjectUID, p.ID); getErr != nil {
			return nil, getErr
		}
		return nil, domain.ErrVersionMismatch
	}
	return p, nil
}
