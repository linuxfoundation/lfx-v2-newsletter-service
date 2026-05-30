// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package upstream implements the HTTP client newsletter-service uses to
// reach the LFX v2 platform query service. The previous incarnation of this
// package was retired in favor of NATS because Heimdall-minted JWTs failed
// OIDC validation at the query service. This revival depends on that auth
// regression having been fixed; if /query/resources begins silently
// returning empty results in production again, the failure mode is the
// same — verify with a real Heimdall JWT first.
package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/auth"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

const (
	// queryResourcesPath is the LFX v2 query service search endpoint.
	queryResourcesPath = "/query/resources"
	// queryAPIVersion is the required v= query parameter (Goa enum).
	queryAPIVersion = "1"
	// queryRequestTimeout caps each HTTP request to the query service.
	queryRequestTimeout = 10 * time.Second
	// queryPageSize is the page size requested when paginating /query/resources.
	// The contract allows up to 1000; 200 keeps response bodies manageable while
	// minimizing round-trips for typical committee sizes.
	queryPageSize = 200
)

// CommitteeClient implements port.CommitteeClient over the LFX v2 query
// service. ListMembers calls
// GET {platformBaseURL}/query/resources?v=1&type=committee_member&tags=committee_uid:<uid>
// with the inbound user's bearer token attached. Access is FGA-gated at the
// query service: a member appears in the response only if the caller has
// `viewer` on `committee:{committee_uid}`.
type CommitteeClient struct {
	platformBaseURL string
	httpClient      *http.Client
}

// NewCommitteeClient wires a CommitteeClient against the given platform base
// URL (NEWSLETTER_PUBLIC_BASE_URL — e.g. https://lfx-api.k8s.orb.local). The
// query service is mounted under /query on the same gateway.
func NewCommitteeClient(platformBaseURL string) *CommitteeClient {
	return &CommitteeClient{
		platformBaseURL: strings.TrimRight(platformBaseURL, "/"),
		httpClient:      &http.Client{Timeout: queryRequestTimeout},
	}
}

// committeeMemberDTO mirrors the relevant subset of the query service's
// committee_member resource data. Other fields (uid, last_name, voting,
// organization, …) are ignored.
type committeeMemberDTO struct {
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
}

// ListMembers fetches all committee_member resources tagged with
// committee_uid:<uid>. Pagination is handled internally. An empty result
// returns an empty slice (no error): the query service does not distinguish
// "committee does not exist" from "committee exists with zero members" — both
// surface as 200 with `"resources": []`. The orchestrator's recipient merge
// tolerates 0-member committees, so this is a deliberate simplification of
// the previous NATS behavior (which returned NotFound for empty replies).
func (c *CommitteeClient) ListMembers(ctx context.Context, committeeUID string) ([]model.CommitteeMember, error) {
	if committeeUID == "" {
		return nil, pkgerrors.NewValidation("committee_uid is required")
	}
	bearer := auth.BearerFromContext(ctx)
	if bearer == "" {
		return nil, pkgerrors.NewUnexpected("query service bearer token not in context", nil)
	}

	var members []model.CommitteeMember
	pageToken := ""
	for {
		page, err := c.fetchPage(ctx, bearer, committeeUID, pageToken)
		if err != nil {
			return nil, err
		}
		for _, env := range page.Resources {
			var dto committeeMemberDTO
			if jsonErr := json.Unmarshal(env.Data, &dto); jsonErr != nil {
				return nil, pkgerrors.NewUnexpected("malformed query service resource", jsonErr)
			}
			members = append(members, model.CommitteeMember{
				Email:     dto.Email,
				FirstName: dto.FirstName,
			})
		}
		if page.PageToken == "" {
			break
		}
		pageToken = page.PageToken
	}
	return members, nil
}

// fetchPage issues a single /query/resources request and decodes the
// response. Non-2xx responses are mapped to typed errors.
func (c *CommitteeClient) fetchPage(ctx context.Context, bearer, committeeUID, pageToken string) (queryResourcesResponse, error) {
	reqURL := c.buildURL(committeeUID, pageToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return queryResourcesResponse{}, pkgerrors.NewUnexpected("build query request", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")

	slog.DebugContext(ctx, "query service request", "url", reqURL)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return queryResourcesResponse{}, pkgerrors.NewServiceUnavailable("query service request failed", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		return decodeQueryResources(resp.Body)
	case http.StatusBadRequest:
		msg := decodeQueryError(resp.Body)
		if msg == "" {
			msg = "query service rejected request"
		}
		return queryResourcesResponse{}, pkgerrors.NewValidation(msg)
	case http.StatusUnauthorized, http.StatusForbidden:
		msg := decodeQueryError(resp.Body)
		slog.WarnContext(ctx, "query service rejected bearer",
			"status", resp.StatusCode,
			"message", msg,
			"committee_uid", committeeUID,
		)
		return queryResourcesResponse{}, pkgerrors.NewUnexpected(
			fmt.Sprintf("query service auth failed (status %d)", resp.StatusCode), nil)
	case http.StatusServiceUnavailable:
		return queryResourcesResponse{}, pkgerrors.NewServiceUnavailable(
			fmt.Sprintf("query service unavailable: %s", decodeQueryError(resp.Body)), nil)
	default:
		return queryResourcesResponse{}, pkgerrors.NewUnexpected(
			fmt.Sprintf("query service unexpected status %d: %s", resp.StatusCode, decodeQueryError(resp.Body)), nil)
	}
}

// buildURL composes the /query/resources URL for a given committee UID and
// optional page token.
func (c *CommitteeClient) buildURL(committeeUID, pageToken string) string {
	q := url.Values{}
	q.Set("v", queryAPIVersion)
	q.Set("type", "committee_member")
	q.Set("tags", "committee_uid:"+committeeUID)
	q.Set("page_size", fmt.Sprintf("%d", queryPageSize))
	if pageToken != "" {
		q.Set("page_token", pageToken)
	}
	return c.platformBaseURL + queryResourcesPath + "?" + q.Encode()
}
