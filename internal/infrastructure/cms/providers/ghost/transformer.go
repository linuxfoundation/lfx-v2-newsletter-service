// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package ghost

import (
	"strings"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/models"
)

// transformPostToDomain converts a Ghost post to a domain Newsletter
func transformPostToDomain(post GhostPost) models.Newsletter {
	newsletter := models.Newsletter{
		ID:           post.ID,
		UUID:         post.UUID,
		Title:        post.Title,
		Slug:         post.Slug,
		HTML:         post.HTML,
		Excerpt:      post.Excerpt,
		FeatureImage: post.FeatureImage,
		Featured:     post.Featured,
		Visibility:   post.Visibility,
		PublishedAt:  post.PublishedAt,
		CreatedAt:    post.CreatedAt,
		UpdatedAt:    post.UpdatedAt,
		URL:          post.URL,
		ReadingTime:  post.ReadingTime,
	}

	// Transform tags
	if len(post.Tags) > 0 {
		newsletter.Tags = make([]string, len(post.Tags))
		for i, tag := range post.Tags {
			newsletter.Tags[i] = tag.Name
		}
	}

	// Transform authors
	if len(post.Authors) > 0 {
		newsletter.Authors = make([]models.Author, len(post.Authors))
		for i, author := range post.Authors {
			newsletter.Authors[i] = models.Author{
				ID:           author.ID,
				Name:         author.Name,
				Slug:         author.Slug,
				ProfileImage: author.ProfileImage,
			}
		}
	}

	return newsletter
}

// transformPostsToDomain converts multiple Ghost posts to domain Newsletters
func transformPostsToDomain(posts []GhostPost) []models.Newsletter {
	newsletters := make([]models.Newsletter, len(posts))
	for i, post := range posts {
		newsletters[i] = transformPostToDomain(post)
	}
	return newsletters
}

// transformMetaToDomain converts Ghost pagination metadata to domain PaginationMeta
func transformMetaToDomain(meta GhostPagination) *models.PaginationMeta {
	return &models.PaginationMeta{
		Page:  meta.Page,
		Limit: meta.Limit,
		Total: meta.Total,
		Pages: meta.Pages,
	}
}

// joinFields joins an array of field names into a comma-separated string
func joinFields(fields []string) string {
	return strings.Join(fields, ",")
}
