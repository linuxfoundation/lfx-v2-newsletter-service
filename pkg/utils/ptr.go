// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package utils

import "time"

// StringPtr converts a string to a pointer to a string.
func StringPtr(s string) *string {
	return &s
}

// StringValue safely dereferences a string pointer, returning empty string if nil.
func StringValue(s *string) string {
	if s != nil {
		return *s
	}
	return ""
}

// StringPtrOmitEmpty returns a pointer to s if s is non-empty, otherwise nil.
func StringPtrOmitEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// BoolPtr converts a bool to a pointer to a bool.
func BoolPtr(b bool) *bool {
	return &b
}

// BoolValue safely dereferences a bool pointer, returning false if nil.
func BoolValue(b *bool) bool {
	if b != nil {
		return *b
	}
	return false
}

// BoolPtrOmitFalse returns a pointer to b if b is true, otherwise nil.
func BoolPtrOmitFalse(b bool) *bool {
	if !b {
		return nil
	}
	return &b
}

// IntPtr converts an int to a pointer to an int.
func IntPtr(i int) *int {
	return &i
}

// IntValue safely dereferences an int pointer, returning 0 if nil.
func IntValue(i *int) int {
	if i != nil {
		return *i
	}
	return 0
}

// IntPtrOmitZero returns a pointer to i if i is non-zero, otherwise nil.
func IntPtrOmitZero(i int) *int {
	if i == 0 {
		return nil
	}
	return &i
}

// Int64PtrOmitZero returns a pointer to i if i is non-zero, otherwise nil.
func Int64PtrOmitZero(i int64) *int64 {
	if i == 0 {
		return nil
	}
	return &i
}

// TimePtr converts a time.Time to a pointer to a time.Time.
func TimePtr(t time.Time) *time.Time {
	return &t
}

// TimeValue safely dereferences a time.Time pointer, returning zero time if nil.
func TimeValue(t *time.Time) time.Time {
	if t != nil {
		return *t
	}
	return time.Time{}
}
