// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package upstream

import (
	"encoding/json"
	"fmt"
	"io"
)

// queryResourcesResponse mirrors the LFX v2 query service /query/resources
// response envelope. See
// https://github.com/linuxfoundation/lfx-v2-query-service/blob/main/design/types.go
// for the canonical schema.
type queryResourcesResponse struct {
	Resources    []resourceEnvelope `json:"resources"`
	PageToken    string             `json:"page_token,omitempty"`
	CacheControl string             `json:"cache_control,omitempty"`
}

// resourceEnvelope is one indexed resource returned by /query/resources. The
// data shape varies by resource type; callers decode Data into the
// type-specific struct.
type resourceEnvelope struct {
	Type string          `json:"type"`
	ID   string          `json:"id"`
	Data json.RawMessage `json:"data"`
}

// queryErrorBody is the shape declared by Goa for non-2xx responses on
// /query/resources. The 400 path for CEL syntax errors uses `error` instead
// of `message` per the query-service README, so both are decoded.
type queryErrorBody struct {
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// decodeQueryResources reads and decodes a successful /query/resources body.
func decodeQueryResources(body io.Reader) (queryResourcesResponse, error) {
	var out queryResourcesResponse
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return queryResourcesResponse{}, fmt.Errorf("decode query response: %w", err)
	}
	return out, nil
}

// decodeQueryError extracts the human-readable message from an error response.
// Returns an empty string if the body is unparseable; the caller should fall
// back to the HTTP status text in that case.
func decodeQueryError(body io.Reader) string {
	var e queryErrorBody
	if err := json.NewDecoder(body).Decode(&e); err != nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Error
}
