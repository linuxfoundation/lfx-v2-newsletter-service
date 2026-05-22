// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package service provides configuration and dependency-injection wiring for
// the newsletter-api binary. All environment variable reads live in this
// package; the rest of the codebase receives typed values.
package service

import (
	"fmt"
	"os"
	"strings"
)

// AppConfig holds all runtime configuration read from environment variables.
type AppConfig struct {
	// Server
	Port     string
	LogLevel string

	// Database (required)
	DatabaseURL string

	// Upstream services (required)
	CommitteeServiceURL string

	// Auth
	JWKSURL          string
	ExpectedAudience string
	RequireUserAuth  bool

	// Environment (informational)
	LFXEnvironment string
}

// Defaults centralizes default values referenced from AppConfigFromEnv.
const (
	defaultPort = "8080"
)

// AppConfigFromEnv reads AppConfig from environment variables, applying defaults
// where reasonable. It returns an error if a required variable is missing.
func AppConfigFromEnv() (AppConfig, error) {
	cfg := AppConfig{
		Port:                envOr("PORT", defaultPort),
		LogLevel:            os.Getenv("LOG_LEVEL"),
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		CommitteeServiceURL: os.Getenv("COMMITTEE_SERVICE_URL"),
		JWKSURL:             os.Getenv("JWKS_URL"),
		ExpectedAudience:    os.Getenv("JWT_AUDIENCE"),
		RequireUserAuth:     boolOr("REQUIRE_USER_AUTH", true),
		LFXEnvironment:      os.Getenv("LFX_ENVIRONMENT"),
	}

	var missing []string
	if cfg.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if cfg.CommitteeServiceURL == "" {
		missing = append(missing, "COMMITTEE_SERVICE_URL")
	}
	if cfg.RequireUserAuth && cfg.JWKSURL == "" {
		missing = append(missing, "JWKS_URL (required when REQUIRE_USER_AUTH=true)")
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}

	return cfg, nil
}

// Validate is reserved for future invariant checks; currently a no-op.
func (c AppConfig) Validate() error {
	return nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func boolOr(key string, fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch raw {
	case "":
		return fallback
	case "1", "true", "yes", "y":
		return true
	case "0", "false", "no", "n":
		return false
	default:
		return fallback
	}
}
