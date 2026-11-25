// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package queryservice reads committee members from the LFX V2 query service.
//
// The HTTP client passed to New adds the service's own M2M bearer token (see
// internal/infrastructure/m2m). This package never sees the token and never
// forwards the inbound user's token.
package queryservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

const (
	// DefaultPageSize is the largest page the query service accepts.
	DefaultPageSize = 1000
	// DefaultMaxPages bounds the paging loop. With the default page size it
	// covers 100,000 members of one committee.
	DefaultMaxPages = 100
	// DefaultTimeout bounds one page request. The query service bounds its own
	// request at 30s.
	DefaultTimeout = 15 * time.Second

	// maxPageSize is the query service's documented page size maximum.
	maxPageSize = 1000

	maxAttempts        = 3
	defaultBackoffBase = 500 * time.Millisecond
	maxRetryAfter      = 30 * time.Second
	maxResponseBytes   = 8 << 20

	resourcePath  = "/query/resources"
	resourceType  = "committee_member"
	tagCommittee  = "committee_uid:"
	queryVersion  = "1"
	acceptHeader  = "application/json"
	retryAfterKey = "Retry-After"
)

// TokenRefresher discards the cached outbound token so the next request mints a
// new one. The client calls it once per page after a 401.
type TokenRefresher interface {
	ForceRefresh()
}

// Config configures a Client. Zero values select the defaults above.
type Config struct {
	BaseURL  string
	PageSize int
	MaxPages int
	// Timeout bounds one page request, including its body read.
	Timeout time.Duration
	// BackoffBase is the delay before the first retry. It doubles for each
	// further retry. Zero selects 500ms.
	BackoffBase time.Duration
}

// Client lists committee members through the query service.
type Client struct {
	baseURL     string
	pageSize    int
	maxPages    int
	timeout     time.Duration
	backoffBase time.Duration
	http        *http.Client
	refresher   TokenRefresher
}

// New validates cfg and returns a Client. httpClient must add the bearer token
// to each request. refresher may be nil, in which case a 401 fails at once.
//
// Every request carries a bearer token, so plain http is refused unless the
// host is a loopback address, and redirects are refused so the token is never
// sent to another host.
func New(cfg Config, httpClient *http.Client, refresher TokenRefresher) (*Client, error) {
	if httpClient == nil {
		return nil, errors.New("query service: http client is required")
	}
	u, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("query service: invalid base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("query service: base URL scheme must be http or https")
	}
	if u.Hostname() == "" {
		return nil, errors.New("query service: base URL has no host")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return nil, errors.New("query service: plain http is only allowed for a loopback host")
	}

	pageSize := cfg.PageSize
	if pageSize == 0 {
		pageSize = DefaultPageSize
	}
	if pageSize < 1 || pageSize > maxPageSize {
		return nil, fmt.Errorf("query service: page size must be between 1 and %d", maxPageSize)
	}
	maxPages := cfg.MaxPages
	if maxPages == 0 {
		maxPages = DefaultMaxPages
	}
	if maxPages < 1 {
		return nil, errors.New("query service: max pages must be at least 1")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	backoff := cfg.BackoffBase
	if backoff <= 0 {
		backoff = defaultBackoffBase
	}

	// Copy so the caller's client keeps its own redirect policy.
	hc := *httpClient
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	return &Client{
		baseURL:     strings.TrimRight(u.String(), "/"),
		pageSize:    pageSize,
		maxPages:    maxPages,
		timeout:     timeout,
		backoffBase: backoff,
		http:        &hc,
		refresher:   refresher,
	}, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// pageResponse is the subset of the query service reply this client reads. The
// endpoint has no total or has_more field. page_token is set only when the raw
// page was full, so paging continues until it is absent.
type pageResponse struct {
	Resources []resource `json:"resources"`
	PageToken string     `json:"page_token"`
}

type resource struct {
	Type string       `json:"type"`
	ID   string       `json:"id"`
	Data resourceData `json:"data"`
}

type resourceData struct {
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	// ProjectUID is optional in the committee_member resource.
	ProjectUID string `json:"project_uid"`
}

// memberEmail is the only place that knows where a committee member's email
// lives. Today it is a field on the committee_member resource. When email moves
// to its own indexed entity, this function is what changes.
func memberEmail(r resource) (string, bool) {
	email := strings.TrimSpace(r.Data.Email)
	return email, email != ""
}

// ListMembers returns every member of the committee, following page_token until
// the query service stops returning one. It never stops on a short page,
// because access filtering can shorten a page that is not the last. If the
// committee needs more than the configured number of pages it returns an error
// and no members.
func (c *Client) ListMembers(ctx context.Context, committeeUID string) (model.CommitteeMembers, error) {
	committeeUID = strings.TrimSpace(committeeUID)
	if committeeUID == "" {
		return model.CommitteeMembers{}, pkgerrors.NewValidation("committee_uid is required")
	}

	start := time.Now()
	var out model.CommitteeMembers
	pageToken := ""
	pages := 0
	for {
		if pages >= c.maxPages {
			return model.CommitteeMembers{}, pkgerrors.NewUnexpected(fmt.Sprintf(
				"committee %s has more than %d pages of members at page size %d", committeeUID, c.maxPages, c.pageSize))
		}
		page, err := c.fetchPage(ctx, committeeUID, pageToken)
		if err != nil {
			return model.CommitteeMembers{}, err
		}
		pages++

		for _, r := range page.Resources {
			email, ok := memberEmail(r)
			if !ok {
				out.SkippedNoEmail++
				continue
			}
			out.Members = append(out.Members, model.CommitteeMember{
				Email:      email,
				FirstName:  r.Data.FirstName,
				LastName:   r.Data.LastName,
				ProjectUID: strings.TrimSpace(r.Data.ProjectUID),
			})
		}
		if page.PageToken == "" {
			break
		}
		pageToken = page.PageToken
	}

	slog.InfoContext(ctx, "committee members read from query service",
		"committee_uid", committeeUID,
		"pages", pages,
		"members", len(out.Members),
		"skipped_no_email", out.SkippedNoEmail,
		"duration", time.Since(start).String(),
	)
	return out, nil
}

var errUnauthorized = errors.New("query service returned 401")

// fetchPage reads one page. It retries transport errors, 429 and 5xx up to
// maxAttempts times with exponential backoff. A 401 discards the cached token
// and retries once without using an attempt. Other statuses fail at once.
func (c *Client) fetchPage(ctx context.Context, committeeUID, pageToken string) (*pageResponse, error) {
	refreshed := false
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; {
		page, retryAfter, err := c.fetchOnce(ctx, committeeUID, pageToken)
		if err == nil {
			return page, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, pkgerrors.NewServiceUnavailable("committee member lookup cancelled", ctxErr)
		}
		if errors.Is(err, errUnauthorized) {
			if refreshed || c.refresher == nil {
				return nil, pkgerrors.NewUnexpected(fmt.Sprintf(
					"query service rejected the service credentials while reading committee %s", committeeUID))
			}
			refreshed = true
			c.refresher.ForceRefresh()
			continue
		}
		var retryable retryableError
		if !errors.As(err, &retryable) {
			return nil, err
		}
		lastErr = err
		if attempt == maxAttempts {
			break
		}
		wait := retryAfter
		if wait <= 0 {
			wait = c.backoffBase << (attempt - 1)
		}
		slog.WarnContext(ctx, "query service page request failed, retrying",
			"committee_uid", committeeUID,
			"attempt", attempt,
			"retry_in", wait.String(),
			"error", err.Error(),
		)
		if err := sleep(ctx, wait); err != nil {
			return nil, pkgerrors.NewServiceUnavailable("committee member lookup cancelled", err)
		}
		attempt++
	}
	return nil, pkgerrors.NewServiceUnavailable(
		fmt.Sprintf("query service unavailable while reading committee %s", committeeUID), lastErr)
}

// retryableError marks a failure that fetchPage may retry.
type retryableError struct{ err error }

func (e retryableError) Error() string { return e.err.Error() }
func (e retryableError) Unwrap() error { return e.err }

func (c *Client) fetchOnce(ctx context.Context, committeeUID, pageToken string) (*pageResponse, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	q := url.Values{}
	q.Set("v", queryVersion)
	q.Set("type", resourceType)
	q.Set("tags", tagCommittee+committeeUID)
	q.Set("page_size", strconv.Itoa(c.pageSize))
	if pageToken != "" {
		q.Set("page_token", pageToken)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+resourcePath+"?"+q.Encode(), nil)
	if err != nil {
		return nil, 0, pkgerrors.NewUnexpected("build query service request", err)
	}
	req.Header.Set("Accept", acceptHeader)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, retryableError{err: fmt.Errorf("query service request failed: %w", scrubURL(err))}
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		if err != nil {
			return nil, 0, retryableError{err: fmt.Errorf("read query service response: %w", err)}
		}
		if len(body) > maxResponseBytes {
			return nil, 0, pkgerrors.NewUnexpected(fmt.Sprintf("query service response exceeds %d bytes", maxResponseBytes))
		}
		var page pageResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, 0, pkgerrors.NewUnexpected("malformed query service response", err)
		}
		return &page, 0, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, 0, errUnauthorized
	case resp.StatusCode == http.StatusForbidden:
		return nil, 0, pkgerrors.NewUnexpected(fmt.Sprintf(
			"query service denied access to committee %s members for the service identity", committeeUID))
	case resp.StatusCode == http.StatusNotFound:
		return nil, 0, pkgerrors.NewNotFound("committee not found")
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, parseRetryAfter(resp.Header.Get(retryAfterKey)), retryableError{
			err: fmt.Errorf("query service returned status %d", resp.StatusCode)}
	default:
		return nil, 0, pkgerrors.NewServiceUnavailable(fmt.Sprintf("query service returned status %d", resp.StatusCode))
	}
}

// scrubURL drops the request URL from a transport error. The URL carries the
// committee UID and page token, which are not secret, but the error text is
// returned up the stack and the URL adds nothing to it.
func scrubURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// parseRetryAfter reads a Retry-After header as whole seconds or an HTTP date.
// It returns zero when the header is absent or unusable, and never more than
// maxRetryAfter.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var d time.Duration
	if secs, err := strconv.Atoi(v); err == nil {
		d = time.Duration(secs) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = time.Until(t)
	}
	if d < 0 {
		return 0
	}
	return min(d, maxRetryAfter)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
