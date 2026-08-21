// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package model

import (
	"testing"
	"time"
)

func TestPubliclyViewable(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	cases := []struct {
		name        string
		status      Status
		scheduledAt *time.Time
		want        bool
	}{
		{name: "draft", status: StatusDraft, want: false},
		{name: "draft with saved intent", status: StatusDraft, scheduledAt: &future, want: false},
		{name: "sending immediately", status: StatusSending, want: true},
		{name: "sending with past schedule", status: StatusSending, scheduledAt: &past, want: true},
		{name: "sending with future schedule", status: StatusSending, scheduledAt: &future, want: false},
		// A release time exactly equal to now has passed, so the batch is out.
		{name: "sending with schedule at now", status: StatusSending, scheduledAt: &now, want: true},
		{name: "scheduled for the future", status: StatusScheduled, scheduledAt: &future, want: false},
		{name: "scheduled release passed", status: StatusScheduled, scheduledAt: &past, want: true},
		// Defensive: MarkScheduled is only reached with a release time set.
		{name: "scheduled without a time", status: StatusScheduled, want: false},
		{name: "sent", status: StatusSent, want: true},
		{name: "sent with a stale schedule", status: StatusSent, scheduledAt: &future, want: true},
		{name: "unknown status", status: Status("bogus"), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := &Newsletter{Status: tc.status, ScheduledAt: tc.scheduledAt}
			if got := n.PubliclyViewable(now); got != tc.want {
				t.Errorf("PubliclyViewable() = %v, want %v", got, tc.want)
			}
		})
	}
}
