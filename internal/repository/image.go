// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"database/sql"

	"github.com/uptrace/bun"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

// ImageRepository persists newsletter image metadata.
type ImageRepository struct {
	db *bun.DB
}

// NewImageRepository wires an image repository over the given bun.DB.
func NewImageRepository(db *bun.DB) *ImageRepository {
	return &ImageRepository{db: db}
}

// InsertImage persists newsletter image metadata. Idempotent via ON CONFLICT DO NOTHING,
// since content-addressing means a duplicate upload (same hash) is a no-op.
func (r *ImageRepository) InsertImage(ctx context.Context, img *model.NewsletterImage) error {
	if _, err := r.db.NewInsert().
		Model(img).
		On("CONFLICT (hash) DO NOTHING").
		Exec(ctx); err != nil {
		return err
	}
	return nil
}

// GetImage retrieves newsletter image metadata by hash.
// Returns domain.ErrNotFound if the image does not exist.
func (r *ImageRepository) GetImage(ctx context.Context, hash string) (*model.NewsletterImage, error) {
	img := &model.NewsletterImage{}
	if err := r.db.NewSelect().
		Model(img).
		Where("hash = ?", hash).
		Scan(ctx); err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return img, nil
}
