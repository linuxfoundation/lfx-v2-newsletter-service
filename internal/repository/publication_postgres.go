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

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
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
func (r *PostgresPublicationRepo) List(ctx context.Context, projectUID string) ([]*model.NewsletterPublication, error) {
	var rows []*model.NewsletterPublication
	err := r.db.NewSelect().
		Model(&rows).
		Where("project_uid = ?", projectUID).
		Order("created_at DESC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list publications: %w", err)
	}
	return rows, nil
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
