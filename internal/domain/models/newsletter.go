// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package models

import "time"

// Newsletter represents a newsletter post from a CMS provider
type Newsletter struct {
	ID           string    `json:"id"`
	UUID         string    `json:"uuid"`
	Title        string    `json:"title"`
	Slug         string    `json:"slug"`
	HTML         string    `json:"html"`
	Excerpt      string    `json:"excerpt"`
	FeatureImage *string   `json:"feature_image,omitempty"`
	Featured     bool      `json:"featured"`
	Visibility   string    `json:"visibility"`
	PublishedAt  time.Time `json:"published_at"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	URL          string    `json:"url"`
	ReadingTime  int       `json:"reading_time"`
	Tags         []string  `json:"tags,omitempty"`
	Authors      []Author  `json:"authors,omitempty"`
}

// Author represents a newsletter author
type Author struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Slug         string  `json:"slug"`
	ProfileImage *string `json:"profile_image,omitempty"`
}

// PaginationMeta represents pagination metadata
type PaginationMeta struct {
	Page  int `json:"page"`
	Limit int `json:"limit"`
	Total int `json:"total"`
	Pages int `json:"pages"`
}

// NewsletterFilter represents filtering options for newsletters
type NewsletterFilter struct {
	Tag           string
	Limit         int
	Page          int
	IncludeFields []string
}
