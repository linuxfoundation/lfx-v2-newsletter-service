// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"maps"
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
