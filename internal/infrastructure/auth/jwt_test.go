// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestJWTAuthenticatorParsePrincipal(t *testing.T) {
	t.Parallel()

	now := time.Now()
	token, jwksURL := newSignedTokenAndJWKS(t, jwt.MapClaims{
		"iss":       DefaultIssuer,
		"aud":       DefaultAudience,
		"principal": "principal-1",
		"exp":       now.Add(30 * time.Minute).Unix(),
		"iat":       now.Add(-time.Minute).Unix(),
	})

	authenticator, err := NewJWTAuthenticator(JWTAuthConfig{
		JWKSURL:   jwksURL,
		Issuer:    DefaultIssuer,
		Audience:  DefaultAudience,
		ClockSkew: DefaultClockSkew,
	})
	if err != nil {
		t.Fatalf("unexpected error creating authenticator: %v", err)
	}
	defer authenticator.Close()

	principal, err := authenticator.ParsePrincipal(context.Background(), token)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if principal != "principal-1" {
		t.Fatalf("expected principal-1, got %s", principal)
	}
}

func TestJWTAuthenticatorRequiresPrincipalClaim(t *testing.T) {
	t.Parallel()

	now := time.Now()
	token, jwksURL := newSignedTokenAndJWKS(t, jwt.MapClaims{
		"iss": DefaultIssuer,
		"aud": DefaultAudience,
		"exp": now.Add(30 * time.Minute).Unix(),
		"iat": now.Add(-time.Minute).Unix(),
	})

	authenticator, err := NewJWTAuthenticator(JWTAuthConfig{
		JWKSURL:   jwksURL,
		Issuer:    DefaultIssuer,
		Audience:  DefaultAudience,
		ClockSkew: DefaultClockSkew,
	})
	if err != nil {
		t.Fatalf("unexpected error creating authenticator: %v", err)
	}
	defer authenticator.Close()

	_, err = authenticator.ParsePrincipal(context.Background(), token)
	if err == nil {
		t.Fatal("expected missing principal error")
	}
	if err != ErrMissingPrincipal {
		t.Fatalf("expected ErrMissingPrincipal, got %v", err)
	}
}

func TestJWTAuthenticatorRejectsWrongAudience(t *testing.T) {
	t.Parallel()

	now := time.Now()
	token, jwksURL := newSignedTokenAndJWKS(t, jwt.MapClaims{
		"iss":       DefaultIssuer,
		"aud":       "wrong-audience",
		"principal": "principal-1",
		"exp":       now.Add(30 * time.Minute).Unix(),
		"iat":       now.Add(-time.Minute).Unix(),
	})

	authenticator, err := NewJWTAuthenticator(JWTAuthConfig{
		JWKSURL:   jwksURL,
		Issuer:    DefaultIssuer,
		Audience:  DefaultAudience,
		ClockSkew: DefaultClockSkew,
	})
	if err != nil {
		t.Fatalf("unexpected error creating authenticator: %v", err)
	}
	defer authenticator.Close()

	_, err = authenticator.ParsePrincipal(context.Background(), token)
	if err == nil {
		t.Fatal("expected invalid bearer token error")
	}
}

func TestJWTAuthenticatorRejectsWrongIssuer(t *testing.T) {
	t.Parallel()

	now := time.Now()
	token, jwksURL := newSignedTokenAndJWKS(t, jwt.MapClaims{
		"iss":       "wrong-issuer",
		"aud":       DefaultAudience,
		"principal": "principal-1",
		"exp":       now.Add(30 * time.Minute).Unix(),
		"iat":       now.Add(-time.Minute).Unix(),
	})

	authenticator, err := NewJWTAuthenticator(JWTAuthConfig{
		JWKSURL:   jwksURL,
		Issuer:    DefaultIssuer,
		Audience:  DefaultAudience,
		ClockSkew: DefaultClockSkew,
	})
	if err != nil {
		t.Fatalf("unexpected error creating authenticator: %v", err)
	}
	defer authenticator.Close()

	_, err = authenticator.ParsePrincipal(context.Background(), token)
	if err == nil {
		t.Fatal("expected invalid bearer token error")
	}
}

func TestJWTAuthenticatorMockLocalPrincipalBypass(t *testing.T) {
	t.Parallel()

	authenticator, err := NewJWTAuthenticator(JWTAuthConfig{
		MockLocalPrincipal: "local-dev-principal",
	})
	if err != nil {
		t.Fatalf("unexpected error creating authenticator: %v", err)
	}

	principal, err := authenticator.ParsePrincipal(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error parsing principal in mock mode: %v", err)
	}
	if principal != "local-dev-principal" {
		t.Fatalf("expected local-dev-principal, got %s", principal)
	}
}

func TestJWTAuthenticatorRequiresJWKSURLWhenMockDisabled(t *testing.T) {
	t.Parallel()

	_, err := NewJWTAuthenticator(JWTAuthConfig{})
	if err == nil {
		t.Fatal("expected error when JWKS_URL is missing")
	}
	if err != ErrMissingJWKSURL {
		t.Fatalf("expected ErrMissingJWKSURL, got %v", err)
	}
}

func newSignedTokenAndJWKS(t *testing.T, claims jwt.MapClaims) (string, string) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	kid := "test-key"
	n := base64.RawURLEncoding.EncodeToString(privateKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.PublicKey.E)).Bytes())

	jwks := fmt.Sprintf(`{"keys":[{"kty":"RSA","kid":"%s","alg":"PS256","use":"sig","n":"%s","e":"%s"}]}`,
		kid, n, e)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(jwks))
	}))
	t.Cleanup(server.Close)

	tok := jwt.NewWithClaims(jwt.SigningMethodPS256, claims)
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(privateKey)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	return signed, server.URL
}
