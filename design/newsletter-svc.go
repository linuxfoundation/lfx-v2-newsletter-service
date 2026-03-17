// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package design

import (
	. "goa.design/goa/v3/dsl" //nolint:staticcheck // ST1001: the recommended way of using the goa DSL package is with the . import
)

// JWTAuth is the DSL JWT security type for authentication.
var JWTAuth = JWTSecurity("jwt", func() {
	Description("Heimdall authorization")
})

var _ = Service("Newsletter Service", func() {
	Description("Service for retrieving newsletter content from configurable CMS providers")

	// GET newsletters by tag
	Method("get-newsletters-by-tag", func() {
		Description("Get newsletters filtered by tag")

		Security(JWTAuth)

		Payload(func() {
			BearerTokenAttribute()
			VersionAttribute()
			Attribute("tag", String, "Tag to filter newsletters", func() {
				Example("news")
			})
			Attribute("limit", Int, "Maximum number of results", func() {
				Default(15)
				Minimum(1)
				Maximum(100)
			})
			Attribute("page", Int, "Page number for pagination", func() {
				Default(1)
				Minimum(1)
			})
			Attribute("include_fields", String, "Comma-separated fields to include", func() {
				Example("id,title,slug,html,excerpt,published_at")
			})
			Required("tag")
		})

		Result(func() {
			Attribute("newsletters", ArrayOf(Newsletter))
			Attribute("meta", PaginationMeta)
			Attribute("cache_control", String, "Cache control header")
			Required("newsletters", "meta")
		})

		Error("BadRequest", BadRequestError)
		Error("NotFound", NotFoundError)
		Error("TooManyRequests", TooManyRequestsError)
		Error("InternalServerError", InternalServerError)
		Error("ServiceUnavailable", ServiceUnavailableError)

		HTTP(func() {
			GET("/newsletters")
			Param("version:v")
			Param("tag")
			Param("limit")
			Param("page")
			Param("include_fields")
			Header("bearer_token:Authorization")
			Response(StatusOK, func() {
				Header("cache_control:Cache-Control")
			})
			Response("BadRequest", StatusBadRequest)
			Response("NotFound", StatusNotFound)
			Response("TooManyRequests", StatusTooManyRequests)
			Response("InternalServerError", StatusInternalServerError)
			Response("ServiceUnavailable", StatusServiceUnavailable)
		})
	})

	// GET newsletter by ID
	Method("get-newsletter-by-id", func() {
		Description("Get a specific newsletter by ID")

		Security(JWTAuth)

		Payload(func() {
			BearerTokenAttribute()
			VersionAttribute()
			Attribute("id", String, "Newsletter ID", func() {
				Example("68ee73450e28060001e59695")
			})
			Attribute("include_fields", String, "Comma-separated fields to include", func() {
				Example("id,title,slug,html,excerpt,published_at")
			})
			Required("id")
		})

		Result(func() {
			Attribute("newsletter", Newsletter)
			Attribute("cache_control", String, "Cache control header")
			Required("newsletter")
		})

		Error("NotFound", NotFoundError)
		Error("TooManyRequests", TooManyRequestsError)
		Error("InternalServerError", InternalServerError)
		Error("ServiceUnavailable", ServiceUnavailableError)

		HTTP(func() {
			GET("/newsletters/{id}")
			Param("version:v")
			Param("id")
			Param("include_fields")
			Header("bearer_token:Authorization")
			Response(StatusOK, func() {
				Header("cache_control:Cache-Control")
			})
			Response("NotFound", StatusNotFound)
			Response("TooManyRequests", StatusTooManyRequests)
			Response("InternalServerError", StatusInternalServerError)
			Response("ServiceUnavailable", StatusServiceUnavailable)
		})
	})
})
