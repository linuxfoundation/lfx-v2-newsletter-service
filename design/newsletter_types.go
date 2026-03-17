// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package design

import (
	. "goa.design/goa/v3/dsl" //nolint:staticcheck // ST1001: the recommended way of using the goa DSL package is with the . import
)

// Author represents newsletter author information
var Author = Type("Author", func() {
	Description("Newsletter author information")
	Attribute("id", String, "Author ID")
	Attribute("name", String, "Author name")
	Attribute("slug", String, "Author slug")
	Attribute("profile_image", String, "Author profile image URL")
	Required("id", "name")
})

// PaginationMeta represents pagination metadata
var PaginationMeta = Type("PaginationMeta", func() {
	Description("Pagination metadata")
	Attribute("page", Int, "Current page number", func() {
		Example(1)
	})
	Attribute("limit", Int, "Results per page", func() {
		Example(15)
	})
	Attribute("total", Int, "Total number of results", func() {
		Example(42)
	})
	Attribute("pages", Int, "Total number of pages", func() {
		Example(3)
	})
	Required("page", "limit", "total", "pages")
})

// Newsletter represents newsletter content from a CMS provider
var Newsletter = Type("Newsletter", func() {
	Description("Newsletter content from a CMS provider")

	Attribute("id", String, "CMS content ID", func() {
		Example("68ee73450e28060001e59695")
	})
	Attribute("uuid", String, "Provider content UUID", func() {
		Format(FormatUUID)
		Example("2b9d3ef6-6068-4af0-99e0-f353d7c3bb5e")
	})
	Attribute("title", String, "Newsletter title", func() {
		Example("LFX Platform Updates - February 2026")
	})
	Attribute("slug", String, "URL-friendly slug", func() {
		Example("lfx-updates-february-2026")
	})
	Attribute("html", String, "HTML content of the newsletter", func() {
		Example("<p>This month's updates include...</p>")
	})
	Attribute("excerpt", String, "Short excerpt of the newsletter", func() {
		Example("This month's updates include new features...")
	})
	Attribute("feature_image", String, "URL to feature image", func() {
		Example("https://cms.example.org/content/images/2026/02/feature.jpg")
	})
	Attribute("featured", Boolean, "Whether the newsletter is featured", func() {
		Example(false)
	})
	Attribute("visibility", String, "Visibility status", func() {
		Example("public")
	})
	Attribute("published_at", String, "Publication timestamp", func() {
		Format(FormatDateTime)
		Example("2026-02-01T10:00:00.000Z")
	})
	Attribute("created_at", String, "Creation timestamp", func() {
		Format(FormatDateTime)
		Example("2026-01-28T15:00:00.000Z")
	})
	Attribute("updated_at", String, "Last update timestamp", func() {
		Format(FormatDateTime)
		Example("2026-02-01T09:55:00.000Z")
	})
	Attribute("url", String, "Public URL to the newsletter", func() {
		Example("https://cms.example.org/lfx-updates-february-2026/")
	})
	Attribute("reading_time", Int, "Estimated reading time in minutes", func() {
		Example(5)
	})
	Attribute("tags", ArrayOf(String), "Associated tags", func() {
		Example([]string{"news", "updates", "lfx"})
	})
	Attribute("authors", ArrayOf(Author), "Newsletter authors")

	Required("id", "title", "slug", "html", "visibility", "published_at", "url")
})
