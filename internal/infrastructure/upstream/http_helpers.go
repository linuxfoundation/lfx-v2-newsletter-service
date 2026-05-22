// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// tokenContextKey is the unexported key type used for bearer token context propagation.
type tokenContextKey struct{}

// BearerTokenContextKey is the context key under which inbound bearer tokens are
// stored. The HTTP middleware sets this; outbound clients read it.
//
// Exported as a value (not a key construction) so callers in this package can
// import it without re-deriving the unexported key shape.
var BearerTokenContextKey = tokenContextKey{}

// BearerTokenFromContext retrieves the inbound bearer token from a context, if any.
func BearerTokenFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	v := ctx.Value(BearerTokenContextKey)
	if v == nil {
		return "", false
	}
	tok, ok := v.(string)
	return tok, ok && tok != ""
}

// parseJSONResponse decodes a successful HTTP JSON response. Non-2xx status
// codes are translated into domain.ErrNotFound (404) or a wrapped error.
func parseJSONResponse[T any](resp *http.Response) (*T, error) {
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, domain.ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview := string(body)
		if len(preview) > 300 {
			preview = preview[:300]
		}
		return nil, pkgerrors.NewServiceUnavailable(
			fmt.Sprintf("upstream returned %d: %s", resp.StatusCode, strings.TrimSpace(preview)),
		)
	}

	var out T
	if len(body) == 0 {
		return &out, nil
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("unmarshal response body: %w", err)
	}
	return &out, nil
}
