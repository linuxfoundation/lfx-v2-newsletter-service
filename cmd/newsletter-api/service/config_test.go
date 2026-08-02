// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"maps"
	"slices"
	"testing"
)

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

func TestAppConfigSelfServeBaseURL(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{
			name: "unset falls back to production default",
			env:  "",
			want: "https://app.lfx.dev",
		},
		{
			name: "override with trailing slash is trimmed",
			env:  "https://app.staging.lfx.dev/",
			want: "https://app.staging.lfx.dev",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Minimal required env so AppConfigFromEnv succeeds without the
			// auth/fan-out required variables.
			t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db")
			t.Setenv("REQUIRE_USER_AUTH", "false")
			t.Setenv("SEND_FANOUT_ENABLED", "false")
			t.Setenv("LFX_SELF_SERVE_BASE_URL", tc.env)

			cfg, err := AppConfigFromEnv()
			if err != nil {
				t.Fatalf("AppConfigFromEnv: %v", err)
			}
			if cfg.SelfServeBaseURL != tc.want {
				t.Errorf("SelfServeBaseURL = %q, want %q", cfg.SelfServeBaseURL, tc.want)
			}
		})
	}
}
