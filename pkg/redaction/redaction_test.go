// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package redaction

import (
	"testing"
)

func TestRedact(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "very short string (1 char)",
			input:    "a",
			expected: "**",
		},
		{
			name:     "very short string (2 chars)",
			input:    "ab",
			expected: "**",
		},
		{
			name:     "short string (3 chars)",
			input:    "abc",
			expected: "a****",
		},
		{
			name:     "short string (5 chars)",
			input:    "abcde",
			expected: "a****",
		},
		{
			name:     "longer string (6 chars)",
			input:    "abcdef",
			expected: "abc****",
		},
		{
			name:     "long username",
			input:    "johndoe123",
			expected: "joh****",
		},
		{
			name:     "long sensitive data",
			input:    "verylongsensitivedata",
			expected: "ver****",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Redact(tt.input)
			if result != tt.expected {
				t.Errorf("Redact(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestRedactEmail(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty email",
			input:    "",
			expected: "",
		},
		{
			name:     "invalid email format",
			input:    "notanemail",
			expected: "not****",
		},
		{
			name:     "short local part",
			input:    "a@example.com",
			expected: "**@example.com",
		},
		{
			name:     "medium local part",
			input:    "john@example.com",
			expected: "j****@example.com",
		},
		{
			name:     "long local part",
			input:    "johndoe@example.com",
			expected: "joh****@example.com",
		},
		{
			name:     "complex email",
			input:    "john.doe+test@company.co.uk",
			expected: "joh****@company.co.uk",
		},
		{
			name:     "very long local part",
			input:    "verylongusername@domain.org",
			expected: "ver****@domain.org",
		},
		{
			name:  "multiple at signs (invalid email)",
			input: "a@b@c.com",
			// fallback to Redact of the whole string
			expected: "a@b****",
		},
		{
			name:  "unicode local part",
			input: "jóse@example.com",
			// 4 runes -> first rune + ****
			expected: "j****@example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RedactEmail(tt.input)
			if result != tt.expected {
				t.Errorf("RedactEmail(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// Benchmarks to ensure redaction performance is acceptable
func BenchmarkRedact(b *testing.B) {
	testString := "johndoe123@example.com"
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		Redact(testString)
	}
}

func BenchmarkRedactEmail(b *testing.B) {
	testEmail := "john.doe+test@company.co.uk"
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		RedactEmail(testEmail)
	}
}
