// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package ghost

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/models"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/infrastructure/cms/core"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const providerName = "ghost"

var tracer = otel.Tracer("cms-provider-client")

// Client represents a Ghost provider adapter client.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *core.HTTPClient
}

// NewClient creates a new Ghost provider client.
func NewClient(config Config) *Client {
	return &Client{
		baseURL:    config.BaseURL,
		apiKey:     config.APIKey,
		httpClient: core.NewHTTPClient(config.Timeout),
	}
}

// GetByTag retrieves newsletters filtered by tag
func (c *Client) GetByTag(ctx context.Context, filter models.NewsletterFilter) ([]models.Newsletter, *models.PaginationMeta, error) {
	ctx, span := tracer.Start(ctx, "provider.GetByTag",
		trace.WithAttributes(
			attribute.String("provider.name", providerName),
			attribute.String("provider.tag", filter.Tag),
			attribute.Int("provider.limit", filter.Limit),
			attribute.Int("provider.page", filter.Page),
		))
	defer span.End()

	// Build query parameters
	params := url.Values{}
	params.Set("key", c.apiKey)
	params.Set("filter", buildTagFilter(filter.Tag))
	params.Set("formats", "html,plaintext")
	params.Set("limit", fmt.Sprintf("%d", filter.Limit))
	params.Set("page", fmt.Sprintf("%d", filter.Page))

	if len(filter.IncludeFields) > 0 {
		params.Set("fields", joinFields(filter.IncludeFields))
	}

	// Make API request
	endpoint := fmt.Sprintf("%s/ghost/api/content/posts/?%s", c.baseURL, params.Encode())
	response, err := c.doRequest(ctx, endpoint)
	if err != nil {
		span.RecordError(err)
		return nil, nil, err
	}

	// Transform to domain models
	newsletters := transformPostsToDomain(response.Posts)
	meta := transformMetaToDomain(response.Meta)

	span.SetAttributes(attribute.Int("provider.results_count", len(newsletters)))
	return newsletters, meta, nil
}

// GetByID retrieves a specific newsletter by ID
func (c *Client) GetByID(ctx context.Context, id string, includeFields []string) (*models.Newsletter, error) {
	ctx, span := tracer.Start(ctx, "provider.GetByID",
		trace.WithAttributes(
			attribute.String("provider.name", providerName),
			attribute.String("provider.id", id),
		))
	defer span.End()

	// Build query parameters
	params := url.Values{}
	params.Set("key", c.apiKey)
	params.Set("filter", fmt.Sprintf("id:%s", id))
	params.Set("formats", "html,plaintext")

	if len(includeFields) > 0 {
		params.Set("fields", joinFields(includeFields))
	}

	// Make API request
	endpoint := fmt.Sprintf("%s/ghost/api/content/posts/?%s", c.baseURL, params.Encode())
	response, err := c.doRequest(ctx, endpoint)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}

	if len(response.Posts) == 0 {
		return nil, domain.ErrNewsletterNotFound
	}

	newsletter := transformPostToDomain(response.Posts[0])
	return &newsletter, nil
}

// doRequest performs the HTTP request to the provider API.
func (c *Client) doRequest(ctx context.Context, endpoint string) (*GhostResponse, error) {
	var ghostResp GhostResponse
	err := c.httpClient.GetJSON(
		ctx,
		endpoint,
		map[string]string{"Accept": "application/json"},
		&ghostResp,
		c.handleErrorResponse,
	)
	if err != nil {
		return nil, err
	}

	return &ghostResp, nil
}

// handleErrorResponse converts provider API errors to domain errors.
func (c *Client) handleErrorResponse(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return domain.ErrInvalidTag
	case http.StatusNotFound:
		return domain.ErrNewsletterNotFound
	case http.StatusUnauthorized:
		return domain.ErrUnauthorized
	case http.StatusTooManyRequests:
		return domain.ErrRateLimitExceeded
	default:
		return fmt.Errorf("provider API error: status %d", resp.StatusCode)
	}
}

func buildTagFilter(tag string) string {
	trimmedTag := strings.TrimSpace(tag)
	escapedTag := strings.ReplaceAll(trimmedTag, "'", "\\'")

	candidates := []string{fmt.Sprintf("tag:'%s'", escapedTag)}
	slugCandidate := toSlugCandidate(trimmedTag)
	if slugCandidate != "" {
		candidates = appendUniqueFilter(candidates, fmt.Sprintf("tag:%s", slugCandidate))
		compactSlug := strings.ReplaceAll(slugCandidate, "-", "")
		if compactSlug != "" {
			candidates = appendUniqueFilter(candidates, fmt.Sprintf("tag:%s", compactSlug))
		}
	}

	return strings.Join(candidates, ",")
}

func appendUniqueFilter(filters []string, candidate string) []string {
	for _, item := range filters {
		if item == candidate {
			return filters
		}
	}
	return append(filters, candidate)
}

func toSlugCandidate(value string) string {
	lower := strings.ToLower(strings.TrimSpace(value))
	if lower == "" {
		return ""
	}

	b := strings.Builder{}
	lastWasDash := false
	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastWasDash = false
			continue
		}

		if !lastWasDash {
			b.WriteRune('-')
			lastWasDash = true
		}
	}

	return strings.Trim(b.String(), "-")
}
