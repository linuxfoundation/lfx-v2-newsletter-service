// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package utils

import (
	"strconv"
	"testing"
	"time"
)

func TestStringPtr(t *testing.T) {
	tests := []string{
		"",
		"hello",
		"hello world",
		"special chars: !@#$%^&*()",
		"unicode: 你好世界",
	}

	for _, test := range tests {
		t.Run(test, func(t *testing.T) {
			ptr := StringPtr(test)
			if ptr == nil {
				t.Error("expected non-nil pointer")
				return
			}
			if *ptr != test {
				t.Errorf("expected %q, got %q", test, *ptr)
			}
		})
	}
}

func TestStringValue(t *testing.T) {
	// Test with nil pointer
	result := StringValue(nil)
	if result != "" {
		t.Errorf("expected empty string for nil pointer, got %q", result)
	}

	// Test with valid pointer
	tests := []string{
		"",
		"hello",
		"hello world",
		"special chars: !@#$%^&*()",
		"unicode: 你好世界",
	}

	for _, test := range tests {
		t.Run(test, func(t *testing.T) {
			ptr := &test
			result := StringValue(ptr)
			if result != test {
				t.Errorf("expected %q, got %q", test, result)
			}
		})
	}
}

func TestStringPtrValueRoundTrip(t *testing.T) {
	tests := []string{
		"",
		"hello",
		"hello world",
		"special chars: !@#$%^&*()",
	}

	for _, test := range tests {
		t.Run(test, func(t *testing.T) {
			// Convert to pointer and back
			ptr := StringPtr(test)
			result := StringValue(ptr)
			if result != test {
				t.Errorf("round trip failed: expected %q, got %q", test, result)
			}
		})
	}
}

func TestBoolPtr(t *testing.T) {
	tests := []struct {
		name  string
		value bool
	}{
		{"true", true},
		{"false", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ptr := BoolPtr(test.value)
			if ptr == nil {
				t.Error("expected non-nil pointer")
				return
			}
			if *ptr != test.value {
				t.Errorf("expected %t, got %t", test.value, *ptr)
			}
		})
	}
}

func TestBoolValue(t *testing.T) {
	// Test with nil pointer
	result := BoolValue(nil)
	if result != false {
		t.Errorf("expected false for nil pointer, got %t", result)
	}

	// Test with valid pointers
	tests := []struct {
		name  string
		value bool
	}{
		{"true", true},
		{"false", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ptr := &test.value
			result := BoolValue(ptr)
			if result != test.value {
				t.Errorf("expected %t, got %t", test.value, result)
			}
		})
	}
}

func TestBoolPtrValueRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		value bool
	}{
		{"true", true},
		{"false", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Convert to pointer and back
			ptr := BoolPtr(test.value)
			result := BoolValue(ptr)
			if result != test.value {
				t.Errorf("round trip failed: expected %t, got %t", test.value, result)
			}
		})
	}
}

func TestIntPtr(t *testing.T) {
	tests := []int{
		0,
		1,
		-1,
		42,
		-42,
		1000000,
		-1000000,
	}

	for _, test := range tests {
		t.Run(strconv.Itoa(test), func(t *testing.T) {
			ptr := IntPtr(test)
			if ptr == nil {
				t.Error("expected non-nil pointer")
				return
			}
			if *ptr != test {
				t.Errorf("expected %d, got %d", test, *ptr)
			}
		})
	}
}

func TestIntValue(t *testing.T) {
	// Test with nil pointer
	result := IntValue(nil)
	if result != 0 {
		t.Errorf("expected 0 for nil pointer, got %d", result)
	}

	// Test with valid pointers
	tests := []int{
		0,
		1,
		-1,
		42,
		-42,
		1000000,
		-1000000,
	}

	for _, test := range tests {
		t.Run(strconv.Itoa(test), func(t *testing.T) {
			ptr := &test
			result := IntValue(ptr)
			if result != test {
				t.Errorf("expected %d, got %d", test, result)
			}
		})
	}
}

func TestIntPtrValueRoundTrip(t *testing.T) {
	tests := []int{
		0,
		1,
		-1,
		42,
		-42,
		1000000,
		-1000000,
	}

	for _, test := range tests {
		t.Run(strconv.Itoa(test), func(t *testing.T) {
			// Convert to pointer and back
			ptr := IntPtr(test)
			result := IntValue(ptr)
			if result != test {
				t.Errorf("round trip failed: expected %d, got %d", test, result)
			}
		})
	}
}

func TestPointerIndependence(t *testing.T) {
	// Test that pointers are independent
	original := "original"
	ptr1 := StringPtr(original)
	ptr2 := StringPtr(original)

	// Pointers should be different
	if ptr1 == ptr2 {
		t.Error("expected different pointer addresses")
	}

	// But values should be the same
	if *ptr1 != *ptr2 {
		t.Errorf("expected same values: %q vs %q", *ptr1, *ptr2)
	}

	// Modifying one shouldn't affect the other
	*ptr1 = "modified"
	if *ptr2 == "modified" {
		t.Error("modifying one pointer affected the other")
	}
}

func TestTimePtr(t *testing.T) {
	tests := []time.Time{
		{}, // Zero time
		time.Now(),
		time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2023, 12, 31, 23, 59, 59, 999999999, time.UTC),
	}

	for _, test := range tests {
		t.Run(test.String(), func(t *testing.T) {
			ptr := TimePtr(test)
			if ptr == nil {
				t.Error("expected non-nil pointer")
				return
			}
			if !ptr.Equal(test) {
				t.Errorf("expected %v, got %v", test, *ptr)
			}
		})
	}
}

func TestTimeValue(t *testing.T) {
	// Test with nil pointer
	result := TimeValue(nil)
	if !result.IsZero() {
		t.Errorf("expected zero time for nil pointer, got %v", result)
	}

	// Test with valid pointers
	tests := []time.Time{
		{}, // Zero time
		time.Now(),
		time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2023, 12, 31, 23, 59, 59, 999999999, time.UTC),
	}

	for _, test := range tests {
		t.Run(test.String(), func(t *testing.T) {
			ptr := &test
			result := TimeValue(ptr)
			if !result.Equal(test) {
				t.Errorf("expected %v, got %v", test, result)
			}
		})
	}
}

func TestTimePtrValueRoundTrip(t *testing.T) {
	tests := []time.Time{
		{}, // Zero time
		time.Now(),
		time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2023, 12, 31, 23, 59, 59, 999999999, time.UTC),
	}

	for _, test := range tests {
		t.Run(test.String(), func(t *testing.T) {
			// Convert to pointer and back
			ptr := TimePtr(test)
			result := TimeValue(ptr)
			if !result.Equal(test) {
				t.Errorf("round trip failed: expected %v, got %v", test, result)
			}
		})
	}
}

func TestNilSafety(t *testing.T) {
	// Test that all Value functions handle nil safely
	stringResult := StringValue(nil)
	if stringResult != "" {
		t.Errorf("StringValue(nil) should return empty string, got %q", stringResult)
	}

	boolResult := BoolValue(nil)
	if boolResult != false {
		t.Errorf("BoolValue(nil) should return false, got %t", boolResult)
	}

	intResult := IntValue(nil)
	if intResult != 0 {
		t.Errorf("IntValue(nil) should return 0, got %d", intResult)
	}

	timeResult := TimeValue(nil)
	if !timeResult.IsZero() {
		t.Errorf("TimeValue(nil) should return zero time, got %v", timeResult)
	}
}
