// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package m2m

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// countingSource returns a new token on every call and records how many times
// it was asked to mint one.
type countingSource struct {
	mints  atomic.Int32
	expiry func() time.Time
	err    error
}

func (c *countingSource) Token() (*oauth2.Token, error) {
	if c.err != nil {
		return nil, c.err
	}
	n := c.mints.Add(1)
	tok := &oauth2.Token{AccessToken: "token-" + string(rune('0'+n)), TokenType: "Bearer"}
	if c.expiry != nil {
		tok.Expiry = c.expiry()
	}
	return tok, nil
}

func TestRefreshableSource_CachesToken(t *testing.T) {
	base := &countingSource{expiry: func() time.Time { return time.Now().Add(time.Hour) }}
	src := newRefreshableSource(base, tokenExpiryLeeway)

	for range 10 {
		tok, err := src.Token()
		require.NoError(t, err)
		assert.Equal(t, "token-1", tok.AccessToken)
	}
	assert.EqualValues(t, 1, base.mints.Load())
}

func TestRefreshableSource_ExpiresEarly(t *testing.T) {
	// The token is valid for 30s, which is inside the 60s leeway, so every call
	// must mint a new token.
	base := &countingSource{expiry: func() time.Time { return time.Now().Add(30 * time.Second) }}
	src := newRefreshableSource(base, tokenExpiryLeeway)

	_, err := src.Token()
	require.NoError(t, err)
	_, err = src.Token()
	require.NoError(t, err)
	assert.EqualValues(t, 2, base.mints.Load())
}

func TestRefreshableSource_KeepsTokenOutsideLeeway(t *testing.T) {
	base := &countingSource{expiry: func() time.Time { return time.Now().Add(2 * time.Minute) }}
	src := newRefreshableSource(base, tokenExpiryLeeway)

	_, err := src.Token()
	require.NoError(t, err)
	_, err = src.Token()
	require.NoError(t, err)
	assert.EqualValues(t, 1, base.mints.Load())
}

func TestRefreshableSource_ForceRefreshMintsNewToken(t *testing.T) {
	base := &countingSource{expiry: func() time.Time { return time.Now().Add(time.Hour) }}
	src := newRefreshableSource(base, tokenExpiryLeeway)

	first, err := src.Token()
	require.NoError(t, err)

	src.ForceRefresh()

	second, err := src.Token()
	require.NoError(t, err)
	assert.NotEqual(t, first.AccessToken, second.AccessToken)
	assert.EqualValues(t, 2, base.mints.Load())

	third, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, second.AccessToken, third.AccessToken)
	assert.EqualValues(t, 2, base.mints.Load())
}

func TestRefreshableSource_PropagatesMintError(t *testing.T) {
	base := &countingSource{err: errors.New("idp unavailable")}
	src := newRefreshableSource(base, tokenExpiryLeeway)

	_, err := src.Token()
	require.Error(t, err)
}

func TestIdentityClient_AddsBearerTokenAndMintsOnce(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	base := &countingSource{expiry: func() time.Time { return time.Now().Add(time.Hour) }}
	src := newRefreshableSource(base, tokenExpiryLeeway)
	client := &http.Client{Transport: &oauth2.Transport{Source: src}}

	for range 5 {
		resp, err := client.Get(srv.URL)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	}
	require.Len(t, seen, 5)
	for _, h := range seen {
		assert.Equal(t, "Bearer token-1", h)
	}
	assert.EqualValues(t, 1, base.mints.Load())
}

func TestNewIdentity_RefusesIncompleteCredentials(t *testing.T) {
	full := IdentityConfig{ClientID: "id", PrivateKey: "key", Domain: "example.auth0.com", Audience: "aud"}
	cases := map[string]func(c *IdentityConfig){
		"missing client id":   func(c *IdentityConfig) { c.ClientID = "" },
		"missing private key": func(c *IdentityConfig) { c.PrivateKey = "  " },
		"missing domain":      func(c *IdentityConfig) { c.Domain = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := full
			mutate(&cfg)
			id, err := NewIdentity(context.Background(), cfg, time.Second)
			require.Error(t, err)
			assert.Nil(t, id)
			assert.Contains(t, err.Error(), "incomplete service identity")
		})
	}
}

func TestNewIdentity_CompleteCredentialsBuildClient(t *testing.T) {
	id, err := NewIdentity(context.Background(),
		IdentityConfig{ClientID: "id", PrivateKey: "key", Domain: "example.auth0.com", Audience: "aud"}, time.Second)
	require.NoError(t, err)
	require.NotNil(t, id.Client())
	assert.Equal(t, time.Second, id.Client().Timeout)
}

func TestNewIdentityClient_RefusesIncompleteCredentials(t *testing.T) {
	client, err := NewIdentityClient(context.Background(), IdentityConfig{ClientID: "id", Domain: "example.auth0.com"}, time.Second)
	require.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "incomplete service identity")
}
