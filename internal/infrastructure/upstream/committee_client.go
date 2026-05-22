// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package upstream contains HTTP clients for sibling LFX v2 microservices.
package upstream

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
)

// defaultHTTPTimeout caps individual upstream calls. Per-request contexts may
// shorten it further.
const defaultHTTPTimeout = 10 * time.Second

// defaultQueryPageSize is the page size for paginated /query/resources calls.
const defaultQueryPageSize = 100

// CommitteeQueryClient resolves committee members through the LFX v2 query service.
//
// The query service exposes /query/resources with type=committee_member and
// tags=committee_uid:<uid>, returning paginated CommitteeMember records.
type CommitteeQueryClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewCommitteeQueryClient wires a CommitteeClient against the LFX v2 query service.
// baseURL should be the API gateway URL routing /query/resources to the query service.
func NewCommitteeQueryClient(baseURL string, httpClient *http.Client) *CommitteeQueryClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &CommitteeQueryClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
	}
}

// committeeMemberDTO mirrors the relevant subset of the query-service response.
type committeeMemberDTO struct {
	Data struct {
		Email     string `json:"email"`
		FirstName string `json:"first_name"`
	} `json:"data"`
}

// queryResourcesResponse mirrors the paginated response shape from /query/resources.
type queryResourcesResponse struct {
	Resources  []committeeMemberDTO `json:"resources"`
	Pagination struct {
		NextPageToken string `json:"next_page_token"`
	} `json:"pagination"`
}

// GetMembers paginates through all committee members and returns the
// (email, first name) tuples needed for newsletter recipient resolution.
func (c *CommitteeQueryClient) GetMembers(ctx context.Context, committeeUID string) ([]model.CommitteeMember, error) {
	bearer, _ := BearerTokenFromContext(ctx)

	var out []model.CommitteeMember
	pageToken := ""

	for {
		q := url.Values{}
		// The query service requires v=1 on every /query/resources request.
		// The error message claims "\"version\" is missing" but the actual URL
		// param key is `v` — Goa's generated validation labels the field by
		// its DSL name, not its URL alias.
		q.Set("v", "1")
		q.Set("type", "committee_member")
		q.Set("tags", "committee_uid:"+committeeUID)
		q.Set("page_size", fmt.Sprintf("%d", defaultQueryPageSize))
		if pageToken != "" {
			q.Set("page_token", pageToken)
		}

		reqURL := c.baseURL + "/query/resources?" + q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, http.NoBody)
		if err != nil {
			return nil, fmt.Errorf("build query request: %w", err)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("query committee members: %w", err)
		}
		body, parseErr := parseJSONResponse[queryResourcesResponse](resp)
		if parseErr != nil {
			return nil, fmt.Errorf("parse committee members: %w", parseErr)
		}

		for _, r := range body.Resources {
			out = append(out, model.CommitteeMember{
				Email:     r.Data.Email,
				FirstName: r.Data.FirstName,
			})
		}

		if body.Pagination.NextPageToken == "" {
			break
		}
		pageToken = body.Pagination.NextPageToken
	}

	return out, nil
}
