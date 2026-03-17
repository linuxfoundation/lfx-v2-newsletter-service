// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// HTTPClient is a reusable HTTP client helper for CMS providers.
type HTTPClient struct {
	httpClient *http.Client
}

// NewHTTPClient creates a new HTTP client with OpenTelemetry transport.
func NewHTTPClient(timeout time.Duration) *HTTPClient {
	return &HTTPClient{
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

// GetJSON performs a GET request and decodes a successful JSON response.
func (c *HTTPClient) GetJSON(
	ctx context.Context,
	endpoint string,
	headers map[string]string,
	out any,
	handleNonOK func(*http.Response) error,
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	for name, value := range headers {
		req.Header.Set(name, value)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if handleNonOK != nil {
			return handleNonOK(resp)
		}
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	return nil
}
