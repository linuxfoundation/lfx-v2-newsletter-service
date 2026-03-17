// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"strings"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	ghostprovider "github.com/linuxfoundation/lfx-v2-newsletter-service/internal/infrastructure/cms/providers/ghost"
)

type cmsRepositoryBuilder func(config *Config, provider string) (domain.NewsletterRepository, error)

var cmsRepositoryBuilders = map[string]cmsRepositoryBuilder{
	"ghost": buildGhostCMSRepository,
}

func buildCMSRepository(config *Config) (domain.NewsletterRepository, string, error) {
	provider := strings.ToLower(strings.TrimSpace(config.CMS.Provider))
	if provider == "" {
		provider = "ghost"
	}

	builder, ok := cmsRepositoryBuilders[provider]
	if !ok {
		return nil, "", fmt.Errorf("unsupported CMS_PROVIDER %q", config.CMS.Provider)
	}

	repository, err := builder(config, provider)
	if err != nil {
		return nil, "", err
	}

	return repository, provider, nil
}

func buildGhostCMSRepository(config *Config, provider string) (domain.NewsletterRepository, error) {
	providerConfig, ok := config.CMSProviders[provider]
	if !ok {
		return nil, fmt.Errorf("missing configuration for CMS provider %q", provider)
	}

	if providerConfig.BaseURL == "" {
		return nil, fmt.Errorf("GHOST_API_BASE_URL is required when CMS_PROVIDER=ghost")
	}
	if providerConfig.APIKey == "" {
		return nil, fmt.Errorf("GHOST_API_KEY is required when CMS_PROVIDER=ghost")
	}

	timeout := providerConfig.Timeout
	if timeout == 0 {
		timeout = config.CMS.Timeout
	}

	ghostConfig := ghostprovider.Config{
		BaseURL:           providerConfig.BaseURL,
		APIKey:            providerConfig.APIKey,
		Timeout:           timeout,
		CacheTTL:          providerConfig.CacheTTL,
		MaxResultsPerPage: providerConfig.MaxResultsPerPage,
	}

	return ghostprovider.NewClient(ghostConfig), nil
}
