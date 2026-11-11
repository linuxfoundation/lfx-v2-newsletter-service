// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package m2m builds an HTTP client that calls LFX services as this service
// itself. It gets an Auth0 access token with the client credentials grant and
// a private key JWT client assertion, caches the token, and adds it to every
// request as a bearer token.
package m2m

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/auth0/go-auth0/authentication"
	"github.com/auth0/go-auth0/authentication/oauth"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/oauth2"
)

// tokenExpiryLeeway makes a cached token count as expired this long before its
// real expiry, so a request never starts with a token that is about to lapse.
const tokenExpiryLeeway = 60 * time.Second

// clientAssertionAlg is the signing algorithm for the private key JWT client
// assertion.
const clientAssertionAlg = "RS256"

// IdentityConfig holds the Auth0 settings for the service's own identity.
// PrivateKey is a PEM string. It is never logged.
type IdentityConfig struct {
	ClientID   string
	PrivateKey string
	Domain     string
	Audience   string
}

// Identity is an HTTP client that authenticates as this service, plus the
// control to discard its cached token.
type Identity struct {
	client *http.Client
	source *refreshableSource
}

// Client returns the HTTP client that adds the service's bearer token to every
// request.
func (i *Identity) Client() *http.Client { return i.client }

// ForceRefresh discards the cached token, so the next request mints a new one.
// Callers use it after the upstream service rejects the cached token with 401.
func (i *Identity) ForceRefresh() { i.source.ForceRefresh() }

// NewIdentityClient returns an HTTP client that authenticates as this service.
// It returns an error when the client ID, private key or domain is missing.
// The private key is parsed when the first token is minted, so a malformed key
// is reported on first use and not at startup.
func NewIdentityClient(ctx context.Context, cfg IdentityConfig, timeout time.Duration) (*http.Client, error) {
	id, err := NewIdentity(ctx, cfg, timeout)
	if err != nil {
		return nil, err
	}
	return id.Client(), nil
}

// NewIdentity is NewIdentityClient, but it also returns the control for
// discarding the cached token.
func NewIdentity(ctx context.Context, cfg IdentityConfig, timeout time.Duration) (*Identity, error) {
	if strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.PrivateKey) == "" || strings.TrimSpace(cfg.Domain) == "" {
		return nil, errors.New("incomplete service identity: client id, private key and domain are all required")
	}

	// The token source outlives the startup context, so it must not inherit
	// its cancellation.
	detached := context.WithoutCancel(ctx)
	idp := &http.Client{Timeout: timeout}
	authConfig, err := authentication.New(detached, cfg.Domain,
		authentication.WithClientID(cfg.ClientID),
		authentication.WithClientAssertion(cfg.PrivateKey, clientAssertionAlg),
		authentication.WithClient(idp),
	)
	if err != nil {
		return nil, fmt.Errorf("create auth0 authentication client: %w", err)
	}

	base := &auth0TokenSource{ctx: detached, auth: authConfig, audience: cfg.Audience, timeout: timeout}
	source := newRefreshableSource(base, tokenExpiryLeeway)

	client := &http.Client{
		Transport: &tokenTransport{
			source: source,
			base:   otelhttp.NewTransport(http.DefaultTransport),
		},
		Timeout: timeout,
	}
	return &Identity{client: client, source: source}, nil
}

// auth0TokenSource mints an access token with the client credentials grant.
type auth0TokenSource struct {
	ctx      context.Context
	auth     *authentication.Authentication
	audience string
	timeout  time.Duration
}

func (a *auth0TokenSource) Token() (*oauth2.Token, error) {
	ctx, cancel := context.WithTimeout(a.ctx, a.timeout)
	defer cancel()

	set, err := a.auth.OAuth.LoginWithClientCredentials(ctx,
		oauth.LoginWithClientCredentialsRequest{Audience: a.audience},
		oauth.IDTokenValidationOptions{},
	)
	if err != nil {
		return nil, fmt.Errorf("client credentials login: %w", err)
	}
	if set.AccessToken == "" {
		return nil, errors.New("client credentials login returned no access token")
	}
	tok := &oauth2.Token{
		AccessToken: set.AccessToken,
		TokenType:   set.TokenType,
	}
	if set.ExpiresIn > 0 {
		tok.Expiry = time.Now().Add(time.Duration(set.ExpiresIn) * time.Second)
	}
	return tok, nil
}

// refreshableSource caches tokens from a base source and can drop the cache.
// oauth2.ReuseTokenSource cannot be invalidated, so ForceRefresh swaps in a new
// ReuseTokenSource that has no cached token.
type refreshableSource struct {
	base    oauth2.TokenSource
	leeway  time.Duration
	mu      sync.Mutex
	current oauth2.TokenSource
}

func newRefreshableSource(base oauth2.TokenSource, leeway time.Duration) *refreshableSource {
	return &refreshableSource{
		base:    base,
		leeway:  leeway,
		current: oauth2.ReuseTokenSourceWithExpiry(nil, base, leeway),
	}
}

// Token returns the cached token, or mints one when there is none or it is
// within the expiry leeway.
func (s *refreshableSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	src := s.current
	s.mu.Unlock()
	return src.Token()
}

// ForceRefresh drops the cached token.
func (s *refreshableSource) ForceRefresh() {
	s.mu.Lock()
	s.current = oauth2.ReuseTokenSourceWithExpiry(nil, s.base, s.leeway)
	s.mu.Unlock()
}

// tokenTransport adds the bearer token to each request. Unlike oauth2.Transport
// it stops waiting for a token when the request's context ends, so cancelling a
// caller, such as an audience job at shutdown, is not held up by a slow Auth0
// login. The login itself runs on to its own timeout in the background.
type tokenTransport struct {
	source oauth2.TokenSource
	base   http.RoundTripper
}

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	type result struct {
		tok *oauth2.Token
		err error
	}
	ch := make(chan result, 1)
	go func() {
		tok, err := t.source.Token()
		ch <- result{tok: tok, err: err}
	}()
	var r result
	select {
	case <-req.Context().Done():
		closeRequestBody(req)
		return nil, req.Context().Err()
	case r = <-ch:
	}
	if r.err != nil {
		closeRequestBody(req)
		return nil, r.err
	}
	// A RoundTripper must not change the caller's request.
	out := req.Clone(req.Context())
	r.tok.SetAuthHeader(out)
	return t.base.RoundTrip(out)
}

func closeRequestBody(req *http.Request) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
}
