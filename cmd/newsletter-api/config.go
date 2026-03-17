// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"os"
	"time"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/infrastructure/auth"
)

type CMSConfig struct {
	Provider string
	Timeout  time.Duration
}

type CMSProviderConfig struct {
	BaseURL           string
	APIKey            string
	Timeout           time.Duration
	CacheTTL          time.Duration
	MaxResultsPerPage int
}

type JWTConfig struct {
	JWKSURL                    string
	Audience                   string
	Issuer                     string
	ClockSkew                  time.Duration
	DisabledMockLocalPrincipal string
}

// Config holds the application configuration
type Config struct {
	Port     string
	LogLevel string

	CMS          CMSConfig
	CMSProviders map[string]CMSProviderConfig
	JWT          JWTConfig

	// OpenTelemetry configuration
	OTELServiceName          string
	OTELServiceVersion       string
	OTELExporterOTLPEndpoint string
	OTELExporterOTLPInsecure bool
	OTELTracesExporter       string
	OTELTracesSampleRatio    float64
}

// LoadConfig loads configuration from environment variables
func LoadConfig() *Config {
	cmsTimeout := getDurationEnv("CMS_TIMEOUT", 30*time.Second)
	ghostTimeout := getDurationEnv("GHOST_API_TIMEOUT", cmsTimeout)

	return &Config{
		Port:     getEnv("PORT", "8080"),
		LogLevel: getEnv("LOG_LEVEL", "info"),
		CMS: CMSConfig{
			Provider: getEnv("CMS_PROVIDER", "ghost"),
			Timeout:  cmsTimeout,
		},
		CMSProviders: map[string]CMSProviderConfig{
			"ghost": {
				BaseURL:           getEnv("GHOST_API_BASE_URL", ""),
				APIKey:            getEnv("GHOST_API_KEY", ""),
				Timeout:           ghostTimeout,
				CacheTTL:          getDurationEnv("GHOST_CACHE_TTL", 5*time.Minute),
				MaxResultsPerPage: getIntEnv("GHOST_MAX_RESULTS_PER_PAGE", 100),
			},
		},
		JWT: JWTConfig{
			JWKSURL:                    getEnv("JWKS_URL", ""),
			Audience:                   getEnv("JWT_AUDIENCE", auth.DefaultAudience),
			Issuer:                     getEnv("JWT_ISSUER", auth.DefaultIssuer),
			ClockSkew:                  getDurationEnv("JWT_CLOCK_SKEW", auth.DefaultClockSkew),
			DisabledMockLocalPrincipal: getEnv("JWT_AUTH_DISABLED_MOCK_LOCAL_PRINCIPAL", ""),
		},
		OTELServiceName:          getEnv("OTEL_SERVICE_NAME", "lfx-v2-newsletter-service"),
		OTELServiceVersion:       getEnv("OTEL_SERVICE_VERSION", ""),
		OTELExporterOTLPEndpoint: getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		OTELExporterOTLPInsecure: getBoolEnv("OTEL_EXPORTER_OTLP_INSECURE", false),
		OTELTracesExporter:       getEnv("OTEL_TRACES_EXPORTER", "none"),
		OTELTracesSampleRatio:    getFloat64Env("OTEL_TRACES_SAMPLE_RATIO", 1.0),
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getIntEnv(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		var result int
		if _, err := fmt.Sscanf(value, "%d", &result); err == nil {
			return result
		}
	}
	return fallback
}

func getBoolEnv(key string, fallback bool) bool {
	if value := os.Getenv(key); value != "" {
		return value == "true" || value == "1"
	}
	return fallback
}

func getFloat64Env(key string, fallback float64) float64 {
	if value := os.Getenv(key); value != "" {
		var result float64
		if _, err := fmt.Sscanf(value, "%f", &result); err == nil {
			return result
		}
	}
	return fallback
}

func getDurationEnv(key string, fallback time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return fallback
}
