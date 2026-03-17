// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package utils

import (
	"testing"
)

type myString string

type otherString string

func TestCoalesce(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		tests := []struct {
			name     string
			values   []string
			expected string
		}{
			{
				name:     "returns first non-empty string",
				values:   []string{"", "", "hello", "world"},
				expected: "hello",
			},
			{
				name:     "returns empty string when all empty",
				values:   []string{"", "", ""},
				expected: "",
			},
			{
				name:     "returns empty string when no arguments",
				values:   []string{},
				expected: "",
			},
			{
				name:     "returns first value when non-empty",
				values:   []string{"first", "second", "third"},
				expected: "first",
			},
			{
				name:     "skips empty strings until non-empty found",
				values:   []string{"", "", "", "found", "not-this"},
				expected: "found",
			},
			{
				name:     "single non-empty value",
				values:   []string{"only"},
				expected: "only",
			},
			{
				name:     "single empty value",
				values:   []string{""},
				expected: "",
			},
			{
				name:     "handles spaces as non-empty",
				values:   []string{"", "  ", "hello"},
				expected: "  ",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := Coalesce(tt.values...)
				if result != tt.expected {
					t.Errorf("Coalesce(%v) = %q, expected %q", tt.values, result, tt.expected)
				}
			})
		}
	})

	t.Run("custom string type", func(t *testing.T) {
		tests := []struct {
			name     string
			values   []myString
			expected myString
		}{
			{
				name:     "returns first non-empty custom string",
				values:   []myString{"", "", "hello", "world"},
				expected: "hello",
			},
			{
				name:     "returns empty when all empty",
				values:   []myString{"", ""},
				expected: "",
			},
			{
				name:     "returns empty when no arguments",
				values:   []myString{},
				expected: "",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := Coalesce(tt.values...)
				if result != tt.expected {
					t.Errorf("Coalesce(%v) = %q, expected %q", tt.values, result, tt.expected)
				}
			})
		}
	})
}

func TestCastSlice(t *testing.T) {
	t.Run("string to custom type", func(t *testing.T) {
		input := []string{"a", "b", "c"}
		result := CastSlice[myString](input)
		expected := []myString{"a", "b", "c"}
		if len(result) != len(expected) {
			t.Fatalf("expected len %d, got %d", len(expected), len(result))
		}
		for i := range result {
			if result[i] != expected[i] {
				t.Errorf("index %d: expected %q, got %q", i, expected[i], result[i])
			}
		}
	})

	t.Run("custom type to string", func(t *testing.T) {
		input := []myString{"x", "y"}
		result := CastSlice[string](input)
		expected := []string{"x", "y"}
		if len(result) != len(expected) {
			t.Fatalf("expected len %d, got %d", len(expected), len(result))
		}
		for i := range result {
			if result[i] != expected[i] {
				t.Errorf("index %d: expected %q, got %q", i, expected[i], result[i])
			}
		}
	})

	t.Run("custom type to other custom type", func(t *testing.T) {
		input := []myString{"foo", "bar"}
		result := CastSlice[otherString](input)
		expected := []otherString{"foo", "bar"}
		if len(result) != len(expected) {
			t.Fatalf("expected len %d, got %d", len(expected), len(result))
		}
		for i := range result {
			if result[i] != expected[i] {
				t.Errorf("index %d: expected %q, got %q", i, expected[i], result[i])
			}
		}
	})

	t.Run("nil slice returns nil", func(t *testing.T) {
		result := CastSlice[myString]([]string(nil))
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
	})

	t.Run("empty slice returns empty", func(t *testing.T) {
		result := CastSlice[myString]([]string{})
		if result == nil || len(result) != 0 {
			t.Errorf("expected empty non-nil slice, got %v", result)
		}
	})
}
