// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package utils

import (
	"testing"
)

// Test structs for testing FieldByTag
type testStruct struct {
	Name    string `json:"name" xml:"name"`
	Age     int    `json:"age" xml:"age"`
	Email   string `json:"email"`
	Active  bool   `json:"active"`
	private string `test:"private"`
	NoTags  string // field without tags
}

type nestedStruct struct {
	Inner testStruct `json:"inner"`
}

func TestFieldByTag_BasicUsage(t *testing.T) {
	obj := testStruct{
		Name:   "John Doe",
		Age:    30,
		Email:  "john@example.com",
		Active: true,
	}

	tests := []struct {
		name     string
		tagType  string
		tagValue string
		expected interface{}
		found    bool
	}{
		{
			name:     "json name tag",
			tagType:  "json",
			tagValue: "name",
			expected: "John Doe",
			found:    true,
		},
		{
			name:     "json age tag",
			tagType:  "json",
			tagValue: "age",
			expected: 30,
			found:    true,
		},
		{
			name:     "json email tag",
			tagType:  "json",
			tagValue: "email",
			expected: "john@example.com",
			found:    true,
		},
		{
			name:     "json active tag",
			tagType:  "json",
			tagValue: "active",
			expected: true,
			found:    true,
		},
		{
			name:     "xml name tag",
			tagType:  "xml",
			tagValue: "name",
			expected: "John Doe",
			found:    true,
		},
		{
			name:     "non-existent tag",
			tagType:  "json",
			tagValue: "nonexistent",
			expected: nil,
			found:    false,
		},
		{
			name:     "wrong tag type",
			tagType:  "yaml",
			tagValue: "name",
			expected: nil,
			found:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, found := FieldByTag(obj, tt.tagType, tt.tagValue)
			if found != tt.found {
				t.Errorf("expected found %t, got %t", tt.found, found)
			}
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestFieldByTag_WithPointer(t *testing.T) {
	obj := &testStruct{
		Name:   "Jane Doe",
		Age:    25,
		Email:  "jane@example.com",
		Active: false,
	}

	result, found := FieldByTag(obj, "json", "name")
	if !found {
		t.Error("expected to find field")
	}
	if result != "Jane Doe" {
		t.Errorf("expected 'Jane Doe', got %v", result)
	}
}

func TestFieldByTag_NilPointer(t *testing.T) {
	var obj *testStruct = nil

	result, found := FieldByTag(obj, "json", "name")
	if found {
		t.Error("expected not to find field with nil pointer")
	}
	if result != nil {
		t.Errorf("expected nil result, got %v", result)
	}
}

func TestFieldByTag_NilInterface(t *testing.T) {
	var obj interface{} = nil

	result, found := FieldByTag(obj, "json", "name")
	if found {
		t.Error("expected not to find field with nil interface")
	}
	if result != nil {
		t.Errorf("expected nil result, got %v", result)
	}
}

func TestFieldByTag_NonStruct(t *testing.T) {
	tests := []interface{}{
		"string",
		42,
		[]string{"slice"},
		map[string]string{"key": "value"},
		true,
	}

	for _, obj := range tests {
		t.Run("non-struct", func(t *testing.T) {
			result, found := FieldByTag(obj, "json", "field")
			if found {
				t.Error("expected not to find field in non-struct")
			}
			if result != nil {
				t.Errorf("expected nil result, got %v", result)
			}
		})
	}
}

func TestFieldByTag_UnexportedField(t *testing.T) {
	obj := testStruct{
		private: "secret",
	}

	result, found := FieldByTag(obj, "test", "private")
	if found {
		t.Error("expected not to find unexported field")
	}
	if result != nil {
		t.Errorf("expected nil result for unexported field, got %v", result)
	}
}

func TestFieldByTag_NoTags(t *testing.T) {
	obj := testStruct{
		NoTags: "value",
	}

	result, found := FieldByTag(obj, "json", "NoTags")
	if found {
		t.Error("expected not to find field without matching tag")
	}
	if result != nil {
		t.Errorf("expected nil result, got %v", result)
	}
}

func TestFieldByTag_EmptyTagValue(t *testing.T) {
	type emptyTagStruct struct {
		Field string `json:""`
	}

	obj := emptyTagStruct{Field: "value"}

	result, found := FieldByTag(obj, "json", "")
	if !found {
		t.Error("expected to find field with empty tag value")
	}
	if result != "value" {
		t.Errorf("expected 'value', got %v", result)
	}
}

func TestFieldByTag_DuplicateTags(t *testing.T) {
	type duplicateTagStruct struct {
		Field1 string `test:"duplicate"`
		Field2 string `test:"duplicate"`
	}

	obj := duplicateTagStruct{
		Field1: "first",
		Field2: "second",
	}

	// Should return the first matching field
	result, found := FieldByTag(obj, "test", "duplicate")
	if !found {
		t.Error("expected to find field with duplicate tag")
	}
	if result != "first" {
		t.Errorf("expected 'first' (first matching field), got %v", result)
	}
}

func TestFieldByTag_ComplexTypes(t *testing.T) {
	type complexStruct struct {
		Slice     []string       `json:"slice"`
		Map       map[string]int `json:"map"`
		Interface interface{}    `json:"interface"`
		Pointer   *string        `json:"pointer"`
	}

	str := "pointer value"
	obj := complexStruct{
		Slice:     []string{"a", "b", "c"},
		Map:       map[string]int{"key": 42},
		Interface: "interface value",
		Pointer:   &str,
	}

	tests := []struct {
		tagValue string
		expected interface{}
	}{
		{"slice", []string{"a", "b", "c"}},
		{"map", map[string]int{"key": 42}},
		{"interface", "interface value"},
		{"pointer", &str},
	}

	for _, tt := range tests {
		t.Run(tt.tagValue, func(t *testing.T) {
			result, found := FieldByTag(obj, "json", tt.tagValue)
			if !found {
				t.Error("expected to find field")
			}

			// For comparison, we need to handle different types appropriately
			switch expected := tt.expected.(type) {
			case []string:
				if resultSlice, ok := result.([]string); ok {
					if len(resultSlice) != len(expected) {
						t.Errorf("slice length mismatch: expected %d, got %d", len(expected), len(resultSlice))
					}
					for i, v := range expected {
						if resultSlice[i] != v {
							t.Errorf("slice element[%d]: expected %q, got %q", i, v, resultSlice[i])
						}
					}
				} else {
					t.Errorf("expected []string, got %T", result)
				}
			case map[string]int:
				if resultMap, ok := result.(map[string]int); ok {
					if len(resultMap) != len(expected) {
						t.Errorf("map length mismatch: expected %d, got %d", len(expected), len(resultMap))
					}
					for k, v := range expected {
						if resultMap[k] != v {
							t.Errorf("map[%s]: expected %d, got %d", k, v, resultMap[k])
						}
					}
				} else {
					t.Errorf("expected map[string]int, got %T", result)
				}
			case *string:
				if resultPtr, ok := result.(*string); ok {
					if *resultPtr != *expected {
						t.Errorf("pointer value: expected %q, got %q", *expected, *resultPtr)
					}
				} else {
					t.Errorf("expected *string, got %T", result)
				}
			default:
				if result != expected {
					t.Errorf("expected %v, got %v", expected, result)
				}
			}
		})
	}
}

func TestFieldByTag_NestedStruct(t *testing.T) {
	obj := nestedStruct{
		Inner: testStruct{
			Name: "nested",
			Age:  40,
		},
	}

	result, found := FieldByTag(obj, "json", "inner")
	if !found {
		t.Error("expected to find nested struct field")
	}

	if innerStruct, ok := result.(testStruct); ok {
		if innerStruct.Name != "nested" {
			t.Errorf("expected nested name 'nested', got %q", innerStruct.Name)
		}
		if innerStruct.Age != 40 {
			t.Errorf("expected nested age 40, got %d", innerStruct.Age)
		}
	} else {
		t.Errorf("expected testStruct, got %T", result)
	}
}
