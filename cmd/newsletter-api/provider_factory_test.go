// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package main

import "testing"

func TestBuildCMSRepositoryGhostSuccess(t *testing.T) {
	config := &Config{
		CMS: CMSConfig{
			Provider: "ghost",
			Timeout:  0,
		},
		CMSProviders: map[string]CMSProviderConfig{
			"ghost": {
				BaseURL:           "https://example.ghost.io",
				APIKey:            "key",
				CacheTTL:          0,
				MaxResultsPerPage: 100,
			},
		},
		OTELServiceName:          "",
		OTELServiceVersion:       "",
		OTELExporterOTLPEndpoint: "",
		OTELTracesExporter:       "none",
	}

	repo, provider, err := buildCMSRepository(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo == nil {
		t.Fatal("expected repository to be initialized")
	}
	if provider != "ghost" {
		t.Fatalf("expected provider ghost, got %s", provider)
	}
}

func TestBuildCMSRepositoryUnsupportedProvider(t *testing.T) {
	config := &Config{CMS: CMSConfig{Provider: "contentful"}}

	repo, provider, err := buildCMSRepository(config)
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}
	if repo != nil {
		t.Fatal("expected nil repository on unsupported provider")
	}
	if provider != "" {
		t.Fatalf("expected empty provider on error, got %s", provider)
	}
}

func TestBuildCMSRepositoryMissingGhostConfig(t *testing.T) {
	config := &Config{CMS: CMSConfig{Provider: "ghost"}}

	_, _, err := buildCMSRepository(config)
	if err == nil {
		t.Fatal("expected error when ghost env vars are missing")
	}
}

func TestBuildCMSRepositoryMissingProviderConfig(t *testing.T) {
	config := &Config{
		CMS:          CMSConfig{Provider: "ghost"},
		CMSProviders: map[string]CMSProviderConfig{},
	}

	_, _, err := buildCMSRepository(config)
	if err == nil {
		t.Fatal("expected error for missing provider config")
	}
}
