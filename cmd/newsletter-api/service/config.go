// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package service provides configuration and dependency-injection wiring for
// the newsletter-api binary. All environment variable reads live in this
// package; the rest of the codebase receives typed values.
package service

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// AppConfig holds all runtime configuration read from environment variables.
type AppConfig struct {
	// Server
	Port     string
	LogLevel string

	// Database (required) — DatabaseURL is the resolved DSN used at runtime.
	// It is either set directly via DATABASE_URL (external mode) or composed
	// in-process from PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE (CloudNativePG
	// mode). Composing in-process keeps the cluster-app password out of the
	// pod spec — the env-var interpolation pattern would embed it verbatim.
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

	// If DATABASE_URL is not set, compose it from PG* env vars in-process so
	// the cluster-app password is never embedded as a literal string in the
	// pod spec (where it would be visible via `kubectl describe pod`).
	if cfg.DatabaseURL == "" {
		if composed, ok := composeDatabaseURL(); ok {
			cfg.DatabaseURL = composed
		}
	}

	var missing []string
	if cfg.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL (or PGHOST/PGUSER/PGPASSWORD/PGDATABASE)")
	}
	if cfg.CommitteeServiceURL == "" {
		missing = append(missing, "COMMITTEE_SERVICE_URL")
	}
	if cfg.RequireUserAuth && cfg.JWKSURL == "" {
		missing = append(missing, "JWKS_URL (required when REQUIRE_USER_AUTH=true)")
	}
	if cfg.RequireUserAuth && cfg.ExpectedAudience == "" {
		missing = append(missing, "JWT_AUDIENCE (required when REQUIRE_USER_AUTH=true)")
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}

	return cfg, nil
}

// composeDatabaseURL builds a Postgres DSN from PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE.
// Returns ok=false when the required fields are missing so the caller can fall
// through to the missing-env-var error path. PGPORT defaults to 5432.
func composeDatabaseURL() (string, bool) {
	host := strings.TrimSpace(os.Getenv("PGHOST"))
	user := strings.TrimSpace(os.Getenv("PGUSER"))
	password := os.Getenv("PGPASSWORD")
	database := strings.TrimSpace(os.Getenv("PGDATABASE"))
	if host == "" || user == "" || password == "" || database == "" {
		return "", false
	}
	port := strings.TrimSpace(os.Getenv("PGPORT"))
	if port == "" {
		port = "5432"
	}
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   host + ":" + port,
		Path:   "/" + database,
	}
	return u.String(), true
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
