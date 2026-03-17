// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	newsletterservice "github.com/linuxfoundation/lfx-v2-newsletter-service/gen/newsletter_service"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/models"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/infrastructure/auth"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	goahttp "goa.design/goa/v3/http"
)

type stubRepo struct {
	filter models.NewsletterFilter
	id     string
	fields []string

	byTagNewsletters []models.Newsletter
	byTagMeta        *models.PaginationMeta
	byTagErr         error

	byIDNewsletter *models.Newsletter
	byIDErr        error
}

type stubAuth struct {
	principal string
	err       error
	token     string
}

func (s *stubAuth) ParsePrincipal(_ context.Context, token string) (string, error) {
	s.token = token
	if s.err != nil {
		return "", s.err
	}
	if token == "" {
		return "", auth.ErrMissingBearerToken
	}
	if s.principal == "" {
		return "", auth.ErrMissingPrincipal
	}
	return s.principal, nil
}

func (s *stubRepo) GetByTag(_ context.Context, filter models.NewsletterFilter) ([]models.Newsletter, *models.PaginationMeta, error) {
	s.filter = filter
	return s.byTagNewsletters, s.byTagMeta, s.byTagErr
}

func (s *stubRepo) GetByID(_ context.Context, id string, includeFields []string) (*models.Newsletter, error) {
	s.id = id
	s.fields = includeFields
	return s.byIDNewsletter, s.byIDErr
}

func TestJWTAuthRequiresToken(t *testing.T) {
	svc := newGoaNewsletterService(service.NewNewsletterService(&stubRepo{}), &stubAuth{principal: "principal-1"})
	_, err := svc.JWTAuth(context.Background(), "", nil)
	if err == nil {
		t.Fatal("expected error for missing token")
	}
	statuser, ok := err.(goahttp.Statuser)
	if !ok {
		t.Fatalf("expected error to implement Statuser, got %T", err)
	}
	if statuser.StatusCode() != 401 {
		t.Fatalf("expected status 401, got %d", statuser.StatusCode())
	}
}

func TestJWTAuthSetsPrincipalInContext(t *testing.T) {
	authStub := &stubAuth{principal: "principal-123"}
	svc := newGoaNewsletterService(service.NewNewsletterService(&stubRepo{}), authStub)

	authCtx, err := svc.JWTAuth(context.Background(), "valid-token", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if authStub.token != "valid-token" {
		t.Fatalf("expected token passed to auth parser")
	}

	principal, ok := auth.PrincipalFromContext(authCtx)
	if !ok {
		t.Fatal("expected principal in context")
	}
	if principal != "principal-123" {
		t.Fatalf("expected principal principal-123, got %s", principal)
	}
}

func TestGetNewslettersByTagMapping(t *testing.T) {
	repo := &stubRepo{
		byTagNewsletters: []models.Newsletter{
			{
				ID:          "id-1",
				UUID:        "uuid-1",
				Title:       "Title",
				Slug:        "slug",
				HTML:        "<p>Hi</p>",
				Excerpt:     "Excerpt",
				Visibility:  "public",
				PublishedAt: time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC),
				CreatedAt:   time.Date(2026, 1, 28, 15, 0, 0, 0, time.UTC),
				UpdatedAt:   time.Date(2026, 2, 1, 9, 55, 0, 0, time.UTC),
				URL:         "https://example.org/post",
				ReadingTime: 5,
				Tags:        []string{"news"},
				Authors: []models.Author{
					{ID: "a1", Name: "Author", Slug: "author"},
				},
			},
		},
		byTagMeta: &models.PaginationMeta{Page: 1, Limit: 15, Total: 1, Pages: 1},
	}
	api := newGoaNewsletterService(service.NewNewsletterService(repo), &stubAuth{principal: "principal-1"})
	include := "id,title"
	payload := &newsletterservice.GetNewslettersByTagPayload{
		Tag:           "news",
		Limit:         15,
		Page:          1,
		IncludeFields: &include,
	}
	res, err := api.GetNewslettersByTag(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Newsletters) != 1 {
		t.Fatalf("expected 1 newsletter, got %d", len(res.Newsletters))
	}
	if repo.filter.Tag != "news" || len(repo.filter.IncludeFields) != 2 {
		t.Fatalf("expected filter fields set from include_fields, got %+v", repo.filter)
	}
}

func TestGetNewsletterByIDMapping(t *testing.T) {
	repo := &stubRepo{
		byIDNewsletter: &models.Newsletter{
			ID:          "id-1",
			Title:       "Title",
			Slug:        "slug",
			HTML:        "<p>Hi</p>",
			Visibility:  "public",
			PublishedAt: time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC),
			CreatedAt:   time.Date(2026, 1, 28, 15, 0, 0, 0, time.UTC),
			UpdatedAt:   time.Date(2026, 2, 1, 9, 55, 0, 0, time.UTC),
			URL:         "https://example.org/post",
		},
	}
	api := newGoaNewsletterService(service.NewNewsletterService(repo), &stubAuth{principal: "principal-1"})
	payload := &newsletterservice.GetNewsletterByIDPayload{ID: "id-1"}
	res, err := api.GetNewsletterByID(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Newsletter == nil || res.Newsletter.ID != "id-1" {
		t.Fatalf("unexpected newsletter result: %+v", res.Newsletter)
	}
}

func TestMapServiceErrorRateLimit(t *testing.T) {
	err := mapServiceError(domain.ErrRateLimitExceeded)
	var tooMany *newsletterservice.TooManyRequestsError
	if !errors.As(err, &tooMany) {
		t.Fatalf("expected TooManyRequestsError, got %T", err)
	}
	if tooMany.Code != "429" {
		t.Fatalf("expected code 429, got %s", tooMany.Code)
	}
}

func TestMapServiceErrorWrappedNotFound(t *testing.T) {
	err := mapServiceError(fmt.Errorf("service wrapper: %w", domain.ErrNewsletterNotFound))
	var notFound *newsletterservice.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected NotFoundError, got %T", err)
	}
	if notFound.Code != "404" {
		t.Fatalf("expected code 404, got %s", notFound.Code)
	}
}
