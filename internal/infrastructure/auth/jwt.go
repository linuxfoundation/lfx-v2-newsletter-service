// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

const (
	DefaultIssuer    = "heimdall"
	DefaultAudience  = "lfx-v2-newsletter-service"
	DefaultClockSkew = 5 * time.Second
)

var (
	ErrMissingJWKSURL     = errors.New("missing JWKS_URL")
	ErrMissingBearerToken = errors.New("missing bearer token")
	ErrInvalidBearerToken = errors.New("invalid bearer token")
	ErrMissingPrincipal   = errors.New("missing principal claim")
	ErrValidatorNotReady  = errors.New("jwt validator not ready")
)

type JWTAuthConfig struct {
	JWKSURL            string
	Issuer             string
	Audience           string
	ClockSkew          time.Duration
	MockLocalPrincipal string
}

type PrincipalParser interface {
	ParsePrincipal(ctx context.Context, token string) (string, error)
}

type JWTClaims struct {
	Principal string `json:"principal"`
	jwt.RegisteredClaims
}

type JWTAuthenticator struct {
	config JWTAuthConfig
	keys   keyfunc.Keyfunc
	cancel context.CancelFunc
}

func NewJWTAuthenticator(config JWTAuthConfig) (*JWTAuthenticator, error) {
	cfg := applyDefaults(config)

	if strings.TrimSpace(cfg.MockLocalPrincipal) != "" {
		return &JWTAuthenticator{config: cfg}, nil
	}

	if strings.TrimSpace(cfg.JWKSURL) == "" {
		return nil, ErrMissingJWKSURL
	}

	ctx, cancel := context.WithCancel(context.Background())
	keys, err := keyfunc.NewDefaultCtx(ctx, []string{cfg.JWKSURL})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("initialize JWKS provider: %w", err)
	}

	return &JWTAuthenticator{
		config: cfg,
		keys:   keys,
		cancel: cancel,
	}, nil
}

func (a *JWTAuthenticator) ParsePrincipal(ctx context.Context, token string) (string, error) {
	if mock := strings.TrimSpace(a.config.MockLocalPrincipal); mock != "" {
		return mock, nil
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return "", ErrMissingBearerToken
	}

	if a.keys == nil {
		return "", ErrValidatorNotReady
	}

	claims := &JWTClaims{}
	_, err := jwt.ParseWithClaims(
		token,
		claims,
		a.keys.KeyfuncCtx(ctx),
		jwt.WithValidMethods([]string{jwt.SigningMethodPS256.Alg()}),
		jwt.WithIssuer(a.config.Issuer),
		jwt.WithAudience(a.config.Audience),
		jwt.WithLeeway(a.config.ClockSkew),
		jwt.WithIssuedAt(),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidBearerToken, err)
	}

	principal := strings.TrimSpace(claims.Principal)
	if principal == "" {
		return "", ErrMissingPrincipal
	}

	return principal, nil
}

func (a *JWTAuthenticator) Close() {
	if a.cancel != nil {
		a.cancel()
	}
}

type principalContextKey struct{}

func ContextWithPrincipal(ctx context.Context, principal string) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) (string, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(string)
	if !ok {
		return "", false
	}
	principal = strings.TrimSpace(principal)
	if principal == "" {
		return "", false
	}
	return principal, true
}

func applyDefaults(config JWTAuthConfig) JWTAuthConfig {
	if strings.TrimSpace(config.Issuer) == "" {
		config.Issuer = DefaultIssuer
	}
	if strings.TrimSpace(config.Audience) == "" {
		config.Audience = DefaultAudience
	}
	if config.ClockSkew <= 0 {
		config.ClockSkew = DefaultClockSkew
	}
	return config
}
