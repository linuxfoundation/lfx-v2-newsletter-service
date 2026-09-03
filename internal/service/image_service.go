// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"fmt"
	"time"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/repository"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service/imageingest"
)

// ImageService orchestrates newsletter image uploads, storage, and retrieval.
type ImageService struct {
	repo  *repository.ImageRepository
	store port.ImageStore
	cfg   imageingest.Limits
}

// NewImageService wires an ImageService with the given dependencies.
func NewImageService(repo *repository.ImageRepository, store port.ImageStore, cfg imageingest.Limits) *ImageService {
	return &ImageService{
		repo:  repo,
		store: store,
		cfg:   cfg,
	}
}

// UploadImage validates, resizes, encodes, stores, and records the image.
// Returns the persisted image metadata on success.
func (s *ImageService) UploadImage(ctx context.Context, projectUID, uploadedBy string, data []byte, declaredContentType string) (*model.NewsletterImage, error) {
	// Run the ingest pipeline (validation, resize, re-encode).
	result, err := imageingest.Ingest(data, declaredContentType, s.cfg)
	if err != nil {
		return nil, err
	}

	// Store bytes in S3.
	if err := s.store.Put(ctx, result.Hash, result.Data, result.ContentType); err != nil {
		return nil, fmt.Errorf("store image: %w", err)
	}

	// Record metadata in the database.
	img := &model.NewsletterImage{
		Hash:        result.Hash,
		ProjectUID:  projectUID,
		ContentType: result.ContentType,
		Width:       result.Width,
		Height:      result.Height,
		ByteSize:    result.ByteSize,
		UploadedBy:  uploadedBy,
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.repo.InsertImage(ctx, img); err != nil {
		return nil, fmt.Errorf("record image: %w", err)
	}

	return img, nil
}

// GetImage retrieves the image bytes from storage, if it exists.
// Returns (data, contentType, error). On success, contentType matches the
// persisted content type. Returns domain.ErrNotFound if the image doesn't exist.
func (s *ImageService) GetImage(ctx context.Context, hash string) ([]byte, string, error) {
	data, contentType, err := s.store.Get(ctx, hash)
	if err != nil {
		return nil, "", err
	}
	return data, contentType, nil
}
