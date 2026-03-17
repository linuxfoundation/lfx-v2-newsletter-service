// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package domain

import (
	"context"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/models"
)

// NewsletterRepository defines the interface for newsletter data access
type NewsletterRepository interface {
	// GetByTag retrieves newsletters filtered by tag
	GetByTag(ctx context.Context, filter models.NewsletterFilter) ([]models.Newsletter, *models.PaginationMeta, error)
	// GetByID retrieves a specific newsletter by ID
	GetByID(ctx context.Context, id string, includeFields []string) (*models.Newsletter, error)
}
