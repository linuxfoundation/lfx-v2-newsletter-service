// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"testing"
)


func TestArchiveNewsletters_CSV_Parsing(t *testing.T) {
	tests := []struct {
		name          string
		queryParam    string
		expectedUIDs  []string
	}{
		{
			name:         "empty param",
			queryParam:   "",
			expectedUIDs: []string{},
		},
		{
			name:         "single UID",
			queryParam:   "committee-1",
			expectedUIDs: []string{"committee-1"},
		},
		{
			name:         "multiple UIDs",
			queryParam:   "committee-1,committee-2,committee-3",
			expectedUIDs: []string{"committee-1", "committee-2", "committee-3"},
		},
		{
			name:         "UIDs with spaces",
			queryParam:   " committee-1 , committee-2 ",
			expectedUIDs: []string{"committee-1", "committee-2"},
		},
		{
			name:         "empty elements in CSV (should be skipped)",
			queryParam:   "committee-1,,committee-2",
			expectedUIDs: []string{"committee-1", "committee-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uids := parseCommitteeUIDs(tt.queryParam)
			if len(uids) != len(tt.expectedUIDs) {
				t.Errorf("expected %d UIDs, got %d: %v", len(tt.expectedUIDs), len(uids), uids)
			}
			for i, expected := range tt.expectedUIDs {
				if i >= len(uids) || uids[i] != expected {
					t.Errorf("UID %d: expected %q, got %q", i, expected, uids[i])
				}
			}
		})
	}
}

