// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestAppConfigFromEnv_SendGridProvider(t *testing.T) {
	// Satisfy the other required vars so the SendGrid validation is isolated.
	t.Setenv("DATABASE_URL", "postgres://u:p@h:5432/db")
	t.Setenv("REQUIRE_USER_AUTH", "false")
	t.Setenv("SEND_FANOUT_ENABLED", "false")
	t.Setenv("LFX_ENVIRONMENT", "local")
	t.Setenv("EMAIL_PROVIDER", "SendGrid") // case-insensitive

	// EMAIL_PROVIDER=sendgrid without a key must fail, naming the missing var.
	if _, err := AppConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "SENDGRID_API_KEY") {
		t.Fatalf("expected a missing SENDGRID_API_KEY error, got: %v", err)
	}

	// With the key, the provider is selected and lowercased.
	t.Setenv("SENDGRID_API_KEY", "SG.key")
	t.Setenv("SENDGRID_SANDBOX_MODE", "true")
	cfg, err := AppConfigFromEnv()
	if err != nil {
		t.Fatalf("AppConfigFromEnv: %v", err)
	}
	if cfg.EmailProvider != "sendgrid" {
		t.Errorf("EmailProvider = %q, want sendgrid", cfg.EmailProvider)
	}
	if cfg.SendGridAPIKey != "SG.key" {
		t.Errorf("SendGridAPIKey = %q, want SG.key", cfg.SendGridAPIKey)
	}
	if !cfg.SendGridSandboxMode {
		t.Errorf("SendGridSandboxMode = false, want true")
	}
}

func TestAppConfigFromEnv_DefaultProvider(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@h:5432/db")
	t.Setenv("REQUIRE_USER_AUTH", "false")
	t.Setenv("SEND_FANOUT_ENABLED", "false")
	t.Setenv("LFX_ENVIRONMENT", "local")

	cfg, err := AppConfigFromEnv()
	if err != nil {
		t.Fatalf("AppConfigFromEnv: %v", err)
	}
	// Default provider stays the email-service (SES) path — no SendGrid key needed.
	if cfg.EmailProvider != "email-service" {
		t.Errorf("EmailProvider = %q, want email-service", cfg.EmailProvider)
	}
}

func TestParseFromAddressOverrides(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{
			name: "empty string yields empty map",
			raw:  "",
			want: map[string]string{},
		},
		{
			name: "single pair",
			raw:  "aaif=newsletter@lfx.aaif.io",
			want: map[string]string{"aaif": "newsletter@lfx.aaif.io"},
		},
		{
			name: "multiple pairs",
			raw:  "aaif=newsletter@lfx.aaif.io,foo=bar@lfx.foo.io",
			want: map[string]string{"aaif": "newsletter@lfx.aaif.io", "foo": "bar@lfx.foo.io"},
		},
		{
			name: "whitespace trimmed and slug lowercased",
			raw:  "  AAIF = newsletter@lfx.aaif.io ",
			want: map[string]string{"aaif": "newsletter@lfx.aaif.io"},
		},
		{
			name: "malformed entries skipped",
			raw:  "noequals,=novalue,emptyaddr=,good=ok@lfx.io",
			want: map[string]string{"good": "ok@lfx.io"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseFromAddressOverrides(tc.raw)
			if !maps.Equal(got, tc.want) {
				t.Errorf("parseFromAddressOverrides(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseAllowedDomains(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		fallback string
		want     []string
	}{
		{
			name:     "empty string falls back",
			raw:      "",
			fallback: "linuxfoundation.org",
			want:     []string{"linuxfoundation.org"},
		},
		{
			name:     "all-blank entries fall back",
			raw:      " , , ",
			fallback: "linuxfoundation.org",
			want:     []string{"linuxfoundation.org"},
		},
		{
			name:     "single domain",
			raw:      "linuxfoundation.org",
			fallback: "linuxfoundation.org",
			want:     []string{"linuxfoundation.org"},
		},
		{
			name:     "multiple domains",
			raw:      "linuxfoundation.org,aaif.io",
			fallback: "linuxfoundation.org",
			want:     []string{"linuxfoundation.org", "aaif.io"},
		},
		{
			name:     "whitespace trimmed and lowercased",
			raw:      "  LinuxFoundation.org , AAIF.io ",
			fallback: "linuxfoundation.org",
			want:     []string{"linuxfoundation.org", "aaif.io"},
		},
		{
			name:     "blank entries between valid ones skipped",
			raw:      "linuxfoundation.org,,aaif.io",
			fallback: "linuxfoundation.org",
			want:     []string{"linuxfoundation.org", "aaif.io"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAllowedDomains(tc.raw, tc.fallback)
			if !slices.Equal(got, tc.want) {
				t.Errorf("parseAllowedDomains(%q, %q) = %v, want %v", tc.raw, tc.fallback, got, tc.want)
			}
		})
	}
}
