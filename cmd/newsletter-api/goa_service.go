// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	newsletterservice "github.com/linuxfoundation/lfx-v2-newsletter-service/gen/newsletter_service"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/models"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/infrastructure/auth"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	goahttp "goa.design/goa/v3/http"
	"goa.design/goa/v3/security"
)

type goaNewsletterService struct {
	svc        *service.NewsletterService
	authParser auth.PrincipalParser
}

func newGoaNewsletterService(svc *service.NewsletterService, authParser auth.PrincipalParser) *goaNewsletterService {
	return &goaNewsletterService{svc: svc, authParser: authParser}
}

// JWTAuth implements the authorization logic for the JWT security scheme.
func (s *goaNewsletterService) JWTAuth(ctx context.Context, token string, _ *security.JWTScheme) (context.Context, error) {
	if s.authParser == nil {
		return ctx, unauthorizedError{err: errors.New("JWT authenticator unavailable")}
	}

	principal, err := s.authParser.ParsePrincipal(ctx, token)
	if err != nil {
		return ctx, unauthorizedError{err: err}
	}

	return auth.ContextWithPrincipal(ctx, principal), nil
}

// GetNewslettersByTag retrieves newsletters filtered by tag.
func (s *goaNewsletterService) GetNewslettersByTag(ctx context.Context, payload *newsletterservice.GetNewslettersByTagPayload) (*newsletterservice.GetNewslettersByTagResult, error) {
	filter := models.NewsletterFilter{
		Tag:   payload.Tag,
		Limit: payload.Limit,
		Page:  payload.Page,
	}
	if payload.IncludeFields != nil {
		filter.IncludeFields = splitFields(*payload.IncludeFields)
	}

	newsletters, meta, cacheControl, err := s.svc.GetNewslettersByTag(ctx, filter)
	if err != nil {
		return nil, mapServiceError(err)
	}

	result := &newsletterservice.GetNewslettersByTagResult{
		Newsletters: toGoaNewsletters(newsletters),
		Meta:        toGoaPagination(meta),
	}
	if cacheControl != "" {
		result.CacheControl = &cacheControl
	}

	return result, nil
}

// GetNewsletterByID retrieves a specific newsletter by ID.
func (s *goaNewsletterService) GetNewsletterByID(ctx context.Context, payload *newsletterservice.GetNewsletterByIDPayload) (*newsletterservice.GetNewsletterByIDResult, error) {
	includeFields := []string{}
	if payload.IncludeFields != nil {
		includeFields = splitFields(*payload.IncludeFields)
	}

	newsletter, cacheControl, err := s.svc.GetNewsletterByID(ctx, payload.ID, includeFields)
	if err != nil {
		return nil, mapServiceError(err)
	}

	result := &newsletterservice.GetNewsletterByIDResult{
		Newsletter: toGoaNewsletter(*newsletter),
	}
	if cacheControl != "" {
		result.CacheControl = &cacheControl
	}

	return result, nil
}

func splitFields(value string) []string {
	parts := strings.Split(value, ",")
	fields := make([]string, 0, len(parts))
	for _, part := range parts {
		field := strings.TrimSpace(part)
		if field == "" {
			continue
		}
		fields = append(fields, field)
	}
	return fields
}

func toGoaNewsletters(items []models.Newsletter) []*newsletterservice.Newsletter {
	result := make([]*newsletterservice.Newsletter, len(items))
	for i, item := range items {
		result[i] = toGoaNewsletter(item)
	}
	return result
}

func toGoaNewsletter(item models.Newsletter) *newsletterservice.Newsletter {
	publishedAt := item.PublishedAt.UTC().Format(time.RFC3339)
	createdAt := item.CreatedAt.UTC().Format(time.RFC3339)
	updatedAt := item.UpdatedAt.UTC().Format(time.RFC3339)
	featured := item.Featured
	readingTime := item.ReadingTime

	result := &newsletterservice.Newsletter{
		ID:          item.ID,
		Title:       item.Title,
		Slug:        item.Slug,
		HTML:        item.HTML,
		Visibility:  item.Visibility,
		PublishedAt: publishedAt,
		URL:         item.URL,
		Tags:        item.Tags,
		Authors:     toGoaAuthors(item.Authors),
		Featured:    &featured,
		ReadingTime: &readingTime,
	}

	if item.UUID != "" {
		uuid := item.UUID
		result.UUID = &uuid
	}
	if item.Excerpt != "" {
		excerpt := item.Excerpt
		result.Excerpt = &excerpt
	}
	if item.FeatureImage != nil {
		result.FeatureImage = item.FeatureImage
	}
	if createdAt != "" {
		result.CreatedAt = &createdAt
	}
	if updatedAt != "" {
		result.UpdatedAt = &updatedAt
	}

	return result
}

func toGoaAuthors(items []models.Author) []*newsletterservice.Author {
	result := make([]*newsletterservice.Author, len(items))
	for i, item := range items {
		result[i] = &newsletterservice.Author{
			ID:           item.ID,
			Name:         item.Name,
			Slug:         stringPointer(item.Slug),
			ProfileImage: item.ProfileImage,
		}
	}
	return result
}

func toGoaPagination(meta *models.PaginationMeta) *newsletterservice.PaginationMeta {
	if meta == nil {
		return nil
	}
	return &newsletterservice.PaginationMeta{
		Page:  meta.Page,
		Limit: meta.Limit,
		Total: meta.Total,
		Pages: meta.Pages,
	}
}

func mapServiceError(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalidTag):
		return &newsletterservice.BadRequestError{
			Code:    "400",
			Message: "The request was invalid.",
		}
	case errors.Is(err, domain.ErrNewsletterNotFound):
		return &newsletterservice.NotFoundError{
			Code:    "404",
			Message: "The resource was not found.",
		}
	case errors.Is(err, domain.ErrRateLimitExceeded):
		return &newsletterservice.TooManyRequestsError{
			Code:    "429",
			Message: "Rate limit exceeded.",
		}
	default:
		return &newsletterservice.InternalServerError{
			Code:    "500",
			Message: "An internal server error occurred.",
		}
	}
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

type unauthorizedError struct {
	err error
}

func (e unauthorizedError) Error() string {
	return e.err.Error()
}

func (e unauthorizedError) Unwrap() error {
	return e.err
}

func (e unauthorizedError) StatusCode() int {
	return http.StatusUnauthorized
}

var _ goahttp.Statuser = unauthorizedError{}
