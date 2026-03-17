// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"fmt"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/models"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

var newsletterTracer = otel.Tracer("newsletter-service")

// NewsletterService handles newsletter-related business logic
type NewsletterService struct {
	repo domain.NewsletterRepository
}

// NewNewsletterService creates a new newsletter service
func NewNewsletterService(repo domain.NewsletterRepository) *NewsletterService {
	return &NewsletterService{
		repo: repo,
	}
}

// GetNewslettersByTag retrieves newsletters filtered by tag
func (s *NewsletterService) GetNewslettersByTag(ctx context.Context, filter models.NewsletterFilter) ([]models.Newsletter, *models.PaginationMeta, string, error) {
	ctx, span := newsletterTracer.Start(ctx, "NewsletterService.GetNewslettersByTag")
	defer span.End()

	span.SetAttributes(
		attribute.String("tag", filter.Tag),
		attribute.Int("limit", filter.Limit),
		attribute.Int("page", filter.Page),
	)

	newsletters, meta, err := s.repo.GetByTag(ctx, filter)
	if err != nil {
		span.RecordError(err)
		return nil, nil, "", fmt.Errorf("failed to get newsletters: %w", err)
	}

	cacheControl := s.generateCacheControl(300) // 5 minutes
	span.SetAttributes(attribute.Int("results_count", len(newsletters)))
	return newsletters, meta, cacheControl, nil
}

// GetNewsletterByID retrieves a specific newsletter by ID
func (s *NewsletterService) GetNewsletterByID(ctx context.Context, id string, includeFields []string) (*models.Newsletter, string, error) {
	ctx, span := newsletterTracer.Start(ctx, "NewsletterService.GetNewsletterByID")
	defer span.End()

	span.SetAttributes(attribute.String("id", id))

	newsletter, err := s.repo.GetByID(ctx, id, includeFields)
	if err != nil {
		span.RecordError(err)
		return nil, "", fmt.Errorf("failed to get newsletter: %w", err)
	}

	cacheControl := s.generateCacheControl(600) // 10 minutes
	return newsletter, cacheControl, nil
}

// generateCacheControl generates a cache control header value
func (s *NewsletterService) generateCacheControl(maxAge int) string {
	return fmt.Sprintf("public, max-age=%d", maxAge)
}
