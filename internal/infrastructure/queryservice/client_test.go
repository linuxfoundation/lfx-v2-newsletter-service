// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package queryservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// recordedRequest is what the fake query service saw for one call.
type recordedRequest struct {
	Path          string
	Query         map[string]string
	Authorization string
	Accept        string
}

// fakeLayer serves canned responses in order and records each request. When
// responses run out it repeats the last one.
type fakeLayer struct {
	mu        sync.Mutex
	responses []fakeResponse
	requests  []recordedRequest
}

type fakeResponse struct {
	status     int
	body       string
	retryAfter string
	// onServe runs before the response is written.
	onServe func()
}

func (f *fakeLayer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	q := map[string]string{}
	for k := range r.URL.Query() {
		q[k] = r.URL.Query().Get(k)
	}
	f.requests = append(f.requests, recordedRequest{
		Path:          r.URL.Path,
		Query:         q,
		Authorization: r.Header.Get("Authorization"),
		Accept:        r.Header.Get("Accept"),
	})
	idx := len(f.requests) - 1
	if idx >= len(f.responses) {
		idx = len(f.responses) - 1
	}
	resp := f.responses[idx]
	f.mu.Unlock()

	if resp.onServe != nil {
		resp.onServe()
	}
	if resp.retryAfter != "" {
		w.Header().Set("Retry-After", resp.retryAfter)
	}
	status := resp.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(resp.body))
}

func (f *fakeLayer) seen() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

// bearer adds a fixed Authorization header, standing in for the M2M client.
type bearer struct {
	token string
	base  http.RoundTripper
}

func (b *bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

type fakeRefresher struct {
	mu    sync.Mutex
	calls int
	// onRefresh runs on each call, e.g. to change the token the next request carries.
	onRefresh func()
}

func (f *fakeRefresher) ForceRefresh() {
	f.mu.Lock()
	f.calls++
	cb := f.onRefresh
	f.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (f *fakeRefresher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func page(token string, members ...string) string {
	type res struct {
		Type string         `json:"type"`
		ID   string         `json:"id"`
		Data map[string]any `json:"data"`
	}
	out := struct {
		Resources []res  `json:"resources"`
		PageToken string `json:"page_token,omitempty"`
	}{PageToken: token}
	for i, m := range members {
		data := map[string]any{"first_name": "First" + fmt.Sprint(i), "last_name": "Last"}
		if m != "" {
			data["email"] = m
		}
		out.Resources = append(out.Resources, res{Type: "committee_member", ID: fmt.Sprint("id", i), Data: data})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func newTestClient(t *testing.T, layer *fakeLayer, refresher TokenRefresher, mutate func(*Config)) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(layer)
	t.Cleanup(srv.Close)
	cfg := Config{BaseURL: srv.URL, BackoffBase: time.Millisecond}
	if mutate != nil {
		mutate(&cfg)
	}
	hc := &http.Client{Transport: &bearer{token: "t1", base: http.DefaultTransport}}
	c, err := New(cfg, hc, refresher)
	require.NoError(t, err)
	return c, srv
}

func TestListMembers_PagesUntilNoToken(t *testing.T) {
	layer := &fakeLayer{responses: []fakeResponse{
		{body: page("tok-2", "a@example.com", "b@example.com")},
		// A short page that is not the last one: access filtering can do this.
		{body: page("tok-3", "c@example.com")},
		{body: page("", "d@example.com")},
	}}
	c, _ := newTestClient(t, layer, nil, func(cfg *Config) { cfg.PageSize = 2 })

	got, err := c.ListMembers(context.Background(), "committee-1")
	require.NoError(t, err)

	emails := make([]string, 0, len(got.Members))
	for _, m := range got.Members {
		emails = append(emails, m.Email)
	}
	assert.Equal(t, []string{"a@example.com", "b@example.com", "c@example.com", "d@example.com"}, emails)

	reqs := layer.seen()
	require.Len(t, reqs, 3)
	for i, r := range reqs {
		assert.Equal(t, "/query/resources", r.Path)
		assert.Equal(t, "1", r.Query["v"])
		assert.Equal(t, "committee_member", r.Query["type"])
		assert.Equal(t, "committee_uid:committee-1", r.Query["tags"])
		assert.Equal(t, "2", r.Query["page_size"])
		assert.Equal(t, "Bearer t1", r.Authorization)
		assert.Equal(t, "application/json", r.Accept)
		switch i {
		case 0:
			assert.NotContains(t, r.Query, "page_token")
		case 1:
			assert.Equal(t, "tok-2", r.Query["page_token"])
		case 2:
			assert.Equal(t, "tok-3", r.Query["page_token"])
		}
	}
}

func TestListMembers_DefaultPageSizeIsOneThousand(t *testing.T) {
	layer := &fakeLayer{responses: []fakeResponse{{body: page("")}}}
	c, _ := newTestClient(t, layer, nil, nil)

	_, err := c.ListMembers(context.Background(), "c1")
	require.NoError(t, err)
	assert.Equal(t, "1000", layer.seen()[0].Query["page_size"])
}

func TestListMembers_PageCeilingReturnsErrorNotTruncatedList(t *testing.T) {
	// Every page carries a token, so the committee never ends.
	layer := &fakeLayer{responses: []fakeResponse{{body: page("more", "a@example.com")}}}
	c, _ := newTestClient(t, layer, nil, func(cfg *Config) { cfg.MaxPages = 3 })

	got, err := c.ListMembers(context.Background(), "c1")
	require.Error(t, err)
	assert.Empty(t, got.Members)
	var unexpected pkgerrors.Unexpected
	assert.True(t, errors.As(err, &unexpected), "got %T", err)
	assert.Contains(t, err.Error(), "c1")
	assert.Len(t, layer.seen(), 3)
}

func TestListMembers_ExactlyMaxPagesSucceeds(t *testing.T) {
	layer := &fakeLayer{responses: []fakeResponse{
		{body: page("t2", "a@example.com")},
		{body: page("", "b@example.com")},
	}}
	c, _ := newTestClient(t, layer, nil, func(cfg *Config) { cfg.MaxPages = 2 })

	got, err := c.ListMembers(context.Background(), "c1")
	require.NoError(t, err)
	assert.Len(t, got.Members, 2)
}

func TestListMembers_SeventeenThousandMembersNeedsEighteenPages(t *testing.T) {
	const total = 17064
	var responses []fakeResponse
	n := 0
	for p := 0; p < 18; p++ {
		count := min(1000, total-n)
		members := make([]string, count)
		for i := range members {
			members[i] = fmt.Sprintf("user%d@example.com", n+i)
		}
		n += count
		token := ""
		if p < 17 {
			token = fmt.Sprint("t", p+1)
		}
		responses = append(responses, fakeResponse{body: page(token, members...)})
	}
	layer := &fakeLayer{responses: responses}
	c, _ := newTestClient(t, layer, nil, nil)

	got, err := c.ListMembers(context.Background(), "big")
	require.NoError(t, err)
	assert.Len(t, got.Members, total)
	assert.Len(t, layer.seen(), 18)
}

func TestListMembers_SkipsAndCountsMembersWithoutEmail(t *testing.T) {
	layer := &fakeLayer{responses: []fakeResponse{
		{body: page("t2", "a@example.com", "", "  ")},
		{body: page("", "", "b@example.com")},
	}}
	c, _ := newTestClient(t, layer, nil, nil)

	got, err := c.ListMembers(context.Background(), "c1")
	require.NoError(t, err)
	assert.Len(t, got.Members, 2)
	assert.Equal(t, 3, got.SkippedNoEmail)
}

func TestListMembers_ReadsNamesAndEmail(t *testing.T) {
	body := `{"resources":[{"type":"committee_member","id":"m1","data":{"email":"first.last@example.com","first_name":"First","last_name":"Last","status":"Active"}}]}`
	layer := &fakeLayer{responses: []fakeResponse{{body: body}}}
	c, _ := newTestClient(t, layer, nil, nil)

	got, err := c.ListMembers(context.Background(), "c1")
	require.NoError(t, err)
	require.Len(t, got.Members, 1)
	assert.Equal(t, "first.last@example.com", got.Members[0].Email)
	assert.Equal(t, "First", got.Members[0].FirstName)
	assert.Equal(t, "Last", got.Members[0].LastName)
}

func TestMemberEmail(t *testing.T) {
	cases := []struct {
		name  string
		in    resource
		want  string
		wantO bool
	}{
		{"present", resource{Data: resourceData{Email: "a@example.com"}}, "a@example.com", true},
		{"trimmed", resource{Data: resourceData{Email: "  a@example.com "}}, "a@example.com", true},
		{"empty", resource{}, "", false},
		{"blank", resource{Data: resourceData{Email: "   "}}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := memberEmail(tc.in)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantO, ok)
		})
	}
}

func TestListMembers_RejectsEmptyCommitteeUID(t *testing.T) {
	layer := &fakeLayer{responses: []fakeResponse{{body: page("")}}}
	c, _ := newTestClient(t, layer, nil, nil)

	_, err := c.ListMembers(context.Background(), "  ")
	var v pkgerrors.Validation
	require.True(t, errors.As(err, &v))
	assert.Empty(t, layer.seen())
}

func TestListMembers_EncodesCommitteeUID(t *testing.T) {
	layer := &fakeLayer{responses: []fakeResponse{{body: page("")}}}
	c, _ := newTestClient(t, layer, nil, nil)

	_, err := c.ListMembers(context.Background(), "a&page_size=1&x=y")
	require.NoError(t, err)
	r := layer.seen()[0]
	assert.Equal(t, "committee_uid:a&page_size=1&x=y", r.Query["tags"])
	assert.Equal(t, "1000", r.Query["page_size"])
	assert.NotContains(t, r.Query, "x")
}

func TestListMembers_Unauthorized_RefreshesOnceThenSucceeds(t *testing.T) {
	layer := &fakeLayer{responses: []fakeResponse{
		{status: http.StatusUnauthorized, body: `{"message":"expired"}`},
		{body: page("", "a@example.com")},
	}}
	refresher := &fakeRefresher{}
	c, _ := newTestClient(t, layer, refresher, nil)

	got, err := c.ListMembers(context.Background(), "c1")
	require.NoError(t, err)
	assert.Len(t, got.Members, 1)
	assert.Equal(t, 1, refresher.count())
	assert.Len(t, layer.seen(), 2)
}

func TestListMembers_Unauthorized_FailsAfterOneRefresh(t *testing.T) {
	layer := &fakeLayer{responses: []fakeResponse{{status: http.StatusUnauthorized}}}
	refresher := &fakeRefresher{}
	c, _ := newTestClient(t, layer, refresher, nil)

	_, err := c.ListMembers(context.Background(), "c1")
	require.Error(t, err)
	assert.Equal(t, 1, refresher.count())
	assert.Len(t, layer.seen(), 2)
	var unexpected pkgerrors.Unexpected
	assert.True(t, errors.As(err, &unexpected))
	assert.NotContains(t, err.Error(), "t1", "the token must not appear in the error")
}

func TestListMembers_Unauthorized_WithoutRefresherFailsAtOnce(t *testing.T) {
	layer := &fakeLayer{responses: []fakeResponse{{status: http.StatusUnauthorized}}}
	c, _ := newTestClient(t, layer, nil, nil)

	_, err := c.ListMembers(context.Background(), "c1")
	require.Error(t, err)
	assert.Len(t, layer.seen(), 1)
}

func TestListMembers_Unauthorized_RefreshAppliesToNextRequest(t *testing.T) {
	token := "old"
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer new" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(page("", "a@example.com")))
	}))
	defer srv.Close()

	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		tok := token
		mu.Unlock()
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+tok)
		return http.DefaultTransport.RoundTrip(r)
	})}
	refresher := &fakeRefresher{onRefresh: func() { mu.Lock(); token = "new"; mu.Unlock() }}
	c, err := New(Config{BaseURL: srv.URL, BackoffBase: time.Millisecond}, hc, refresher)
	require.NoError(t, err)

	got, err := c.ListMembers(context.Background(), "c1")
	require.NoError(t, err)
	assert.Len(t, got.Members, 1)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestListMembers_StatusMapping(t *testing.T) {
	t.Run("404 is not found", func(t *testing.T) {
		layer := &fakeLayer{responses: []fakeResponse{{status: http.StatusNotFound}}}
		c, _ := newTestClient(t, layer, nil, nil)
		_, err := c.ListMembers(context.Background(), "c1")
		var nf pkgerrors.NotFound
		require.True(t, errors.As(err, &nf), "got %T: %v", err, err)
		assert.Len(t, layer.seen(), 1, "404 is not retried")
	})
	t.Run("403 names the committee and is not retried", func(t *testing.T) {
		layer := &fakeLayer{responses: []fakeResponse{{status: http.StatusForbidden}}}
		c, _ := newTestClient(t, layer, nil, nil)
		_, err := c.ListMembers(context.Background(), "committee-xyz")
		var unexpected pkgerrors.Unexpected
		require.True(t, errors.As(err, &unexpected), "got %T: %v", err, err)
		assert.Contains(t, err.Error(), "committee-xyz")
		assert.NotContains(t, err.Error(), "Bearer")
		assert.Len(t, layer.seen(), 1)
	})
	t.Run("400 is service unavailable and not retried", func(t *testing.T) {
		layer := &fakeLayer{responses: []fakeResponse{{status: http.StatusBadRequest}}}
		c, _ := newTestClient(t, layer, nil, nil)
		_, err := c.ListMembers(context.Background(), "c1")
		var su pkgerrors.ServiceUnavailable
		require.True(t, errors.As(err, &su))
		assert.Len(t, layer.seen(), 1)
	})
	t.Run("500 is retried three times then service unavailable", func(t *testing.T) {
		layer := &fakeLayer{responses: []fakeResponse{{status: http.StatusInternalServerError}}}
		c, _ := newTestClient(t, layer, nil, nil)
		_, err := c.ListMembers(context.Background(), "c1")
		var su pkgerrors.ServiceUnavailable
		require.True(t, errors.As(err, &su))
		assert.Len(t, layer.seen(), 3)
	})
	t.Run("malformed body is not retried", func(t *testing.T) {
		layer := &fakeLayer{responses: []fakeResponse{{body: "{not json"}}}
		c, _ := newTestClient(t, layer, nil, nil)
		_, err := c.ListMembers(context.Background(), "c1")
		var unexpected pkgerrors.Unexpected
		require.True(t, errors.As(err, &unexpected))
		assert.Len(t, layer.seen(), 1)
	})
}

func TestListMembers_RetriesThenSucceeds(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			layer := &fakeLayer{responses: []fakeResponse{
				{status: status},
				{status: status},
				{body: page("", "a@example.com")},
			}}
			c, _ := newTestClient(t, layer, nil, nil)
			got, err := c.ListMembers(context.Background(), "c1")
			require.NoError(t, err)
			assert.Len(t, got.Members, 1)
			assert.Len(t, layer.seen(), 3)
		})
	}
}

func TestListMembers_RetriesTransportError(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			// Drop the connection without a response.
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		_, _ = w.Write([]byte(page("", "a@example.com")))
	}))
	defer srv.Close()

	c, err := New(Config{BaseURL: srv.URL, BackoffBase: time.Millisecond}, &http.Client{}, nil)
	require.NoError(t, err)
	got, err := c.ListMembers(context.Background(), "c1")
	require.NoError(t, err)
	assert.Len(t, got.Members, 1)
	assert.Equal(t, 2, calls)
}

func TestListMembers_HonoursRetryAfter(t *testing.T) {
	layer := &fakeLayer{responses: []fakeResponse{
		{status: http.StatusTooManyRequests, retryAfter: "1"},
		{body: page("")},
	}}
	// The backoff base is a minute, so only Retry-After can make this finish.
	c, _ := newTestClient(t, layer, nil, func(cfg *Config) { cfg.BackoffBase = time.Minute })

	start := time.Now()
	_, err := c.ListMembers(context.Background(), "c1")
	require.NoError(t, err)
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 900*time.Millisecond)
	assert.Less(t, elapsed, 10*time.Second)
}

func TestParseRetryAfter(t *testing.T) {
	assert.Equal(t, time.Duration(0), parseRetryAfter(""))
	assert.Equal(t, 3*time.Second, parseRetryAfter("3"))
	assert.Equal(t, maxRetryAfter, parseRetryAfter("86400"))
	assert.Equal(t, time.Duration(0), parseRetryAfter("-5"))
	assert.Equal(t, time.Duration(0), parseRetryAfter("soon"))
	assert.Equal(t, time.Duration(0), parseRetryAfter(time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)))
	got := parseRetryAfter(time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat))
	assert.Greater(t, got, 5*time.Second)
	assert.LessOrEqual(t, got, 10*time.Second)
}

func TestListMembers_ContextCancelledStopsRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	layer := &fakeLayer{responses: []fakeResponse{{status: http.StatusInternalServerError, onServe: cancel}}}
	c, _ := newTestClient(t, layer, nil, func(cfg *Config) { cfg.BackoffBase = time.Minute })

	_, err := c.ListMembers(ctx, "c1")
	require.Error(t, err)
	assert.Len(t, layer.seen(), 1)
}

func TestListMembers_PerPageTimeoutIsRetried(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			select {
			case <-time.After(2 * time.Second):
			case <-r.Context().Done():
			}
			return
		}
		_, _ = w.Write([]byte(page("", "a@example.com")))
	}))
	defer srv.Close()

	c, err := New(Config{BaseURL: srv.URL, Timeout: 50 * time.Millisecond, BackoffBase: time.Millisecond}, &http.Client{}, nil)
	require.NoError(t, err)
	got, err := c.ListMembers(context.Background(), "c1")
	require.NoError(t, err)
	assert.Len(t, got.Members, 1)
}

func TestListMembers_ResponseBodyIsSizeLimited(t *testing.T) {
	big := `{"resources":[],"padding":"` + strings.Repeat("x", maxResponseBytes+10) + `"}`
	layer := &fakeLayer{responses: []fakeResponse{{body: big}}}
	c, _ := newTestClient(t, layer, nil, nil)

	_, err := c.ListMembers(context.Background(), "c1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
	assert.Len(t, layer.seen(), 1)
}

func TestListMembers_RefusesRedirects(t *testing.T) {
	var otherHostHits int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHostHits++
		_, _ = w.Write([]byte(page("")))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/query/resources", http.StatusFound)
	}))
	defer srv.Close()

	c, err := New(Config{BaseURL: srv.URL, BackoffBase: time.Millisecond}, &http.Client{}, nil)
	require.NoError(t, err)
	_, err = c.ListMembers(context.Background(), "c1")
	require.Error(t, err)
	assert.Zero(t, otherHostHits, "the redirect must not be followed")
}

func TestNew_ValidatesBaseURL(t *testing.T) {
	hc := &http.Client{}
	cases := []struct {
		name    string
		baseURL string
		wantErr string
	}{
		{"empty", "", "scheme"},
		{"no scheme", "query.example.com", "scheme"},
		{"ftp", "ftp://query.example.com", "scheme"},
		{"no host", "https://", "no host"},
		{"plain http to a remote host", "http://query.example.com", "loopback"},
		{"https is fine", "https://query.example.com/", ""},
		{"http to localhost is fine", "http://localhost:8080", ""},
		{"http to 127.0.0.1 is fine", "http://127.0.0.1:8080", ""},
		{"http to ipv6 loopback is fine", "http://[::1]:8080", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Config{BaseURL: tc.baseURL}, hc, nil)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestNew_ValidatesLimits(t *testing.T) {
	hc := &http.Client{}
	_, err := New(Config{BaseURL: "https://q.example.com", PageSize: 1001}, hc, nil)
	require.Error(t, err)
	_, err = New(Config{BaseURL: "https://q.example.com", PageSize: -1}, hc, nil)
	require.Error(t, err)
	_, err = New(Config{BaseURL: "https://q.example.com", MaxPages: -1}, hc, nil)
	require.Error(t, err)
	_, err = New(Config{BaseURL: "https://q.example.com"}, nil, nil)
	require.Error(t, err)

	c, err := New(Config{BaseURL: "https://q.example.com"}, hc, nil)
	require.NoError(t, err)
	assert.Equal(t, DefaultPageSize, c.pageSize)
	assert.Equal(t, DefaultMaxPages, c.maxPages)
	assert.Equal(t, DefaultTimeout, c.timeout)
}

func TestNew_DoesNotChangeCallersClient(t *testing.T) {
	hc := &http.Client{}
	_, err := New(Config{BaseURL: "https://q.example.com"}, hc, nil)
	require.NoError(t, err)
	assert.Nil(t, hc.CheckRedirect)
}
