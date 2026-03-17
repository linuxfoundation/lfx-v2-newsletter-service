// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package design

import (
	. "goa.design/goa/v3/dsl" //nolint:staticcheck // ST1001: the recommended way of using the goa DSL package is with the . import
)

// BearerTokenAttribute is a reusable token attribute for JWT authentication.
func BearerTokenAttribute() {
	Token("bearer_token", String, func() {
		Description("JWT token issued by Heimdall")
		Example("eyJhbGci...")
	})
}

// VersionAttribute is a reusable version attribute.
func VersionAttribute() {
	Attribute("version", String, "Version of the API", func() {
		Enum("1")
		Example("1")
	})
}

// Error types
var BadRequestError = Type("BadRequestError", func() {
	Description("BadRequestError is the DSL type for a bad request error.")
	Attribute("code", String, "HTTP status code", func() {
		Example("400")
	})
	Attribute("message", String, "Error message", func() {
		Example("The request was invalid.")
	})
	Required("code", "message")
})

var NotFoundError = Type("NotFoundError", func() {
	Description("NotFoundError is the DSL type for a not found error.")
	Attribute("code", String, "HTTP status code", func() {
		Example("404")
	})
	Attribute("message", String, "Error message", func() {
		Example("The resource was not found.")
	})
	Required("code", "message")
})

var InternalServerError = Type("InternalServerError", func() {
	Description("InternalServerError is the DSL type for an internal server error.")
	Attribute("code", String, "HTTP status code", func() {
		Example("500")
	})
	Attribute("message", String, "Error message", func() {
		Example("An internal server error occurred.")
	})
	Required("code", "message")
})

var ServiceUnavailableError = Type("ServiceUnavailableError", func() {
	Description("ServiceUnavailableError is the DSL type for a service unavailable error.")
	Attribute("code", String, "HTTP status code", func() {
		Example("503")
	})
	Attribute("message", String, "Error message", func() {
		Example("The service is temporarily unavailable.")
	})
	Required("code", "message")
})

var TooManyRequestsError = Type("TooManyRequestsError", func() {
	Description("TooManyRequestsError is the DSL type for a rate limit error.")
	Attribute("code", String, "HTTP status code", func() {
		Example("429")
	})
	Attribute("message", String, "Error message", func() {
		Example("Rate limit exceeded.")
	})
	Required("code", "message")
})
