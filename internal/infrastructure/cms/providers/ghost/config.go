// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package ghost

import "time"

// Config holds Ghost provider client configuration.
type Config struct {
	BaseURL           string
	APIKey            string
	Timeout           time.Duration
	CacheTTL          time.Duration
	MaxResultsPerPage int
}

// DefaultConfig returns default provider configuration.
func DefaultConfig() Config {
	return Config{
		Timeout:           30 * time.Second,
		CacheTTL:          5 * time.Minute,
		MaxResultsPerPage: 100,
	}
}
