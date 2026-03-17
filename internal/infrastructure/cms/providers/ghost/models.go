// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package ghost

import "time"

// GhostResponse represents the response from Ghost API
type GhostResponse struct {
	Posts []GhostPost     `json:"posts"`
	Meta  GhostPagination `json:"meta"`
}

// GhostPost represents a post from Ghost API
type GhostPost struct {
	ID           string        `json:"id"`
	UUID         string        `json:"uuid"`
	Title        string        `json:"title"`
	Slug         string        `json:"slug"`
	HTML         string        `json:"html"`
	Excerpt      string        `json:"excerpt"`
	FeatureImage *string       `json:"feature_image"`
	Featured     bool          `json:"featured"`
	Visibility   string        `json:"visibility"`
	PublishedAt  time.Time     `json:"published_at"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	URL          string        `json:"url"`
	ReadingTime  int           `json:"reading_time"`
	Tags         []GhostTag    `json:"tags,omitempty"`
	Authors      []GhostAuthor `json:"authors,omitempty"`
}

// GhostTag represents a tag from Ghost API
type GhostTag struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// GhostAuthor represents an author from Ghost API
type GhostAuthor struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Slug         string  `json:"slug"`
	ProfileImage *string `json:"profile_image"`
}

// GhostPagination represents pagination metadata from Ghost API
type GhostPagination struct {
	Page  int `json:"page"`
	Limit int `json:"limit"`
	Pages int `json:"pages"`
	Total int `json:"total"`
}
