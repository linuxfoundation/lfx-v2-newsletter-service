// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"sync"
	"time"
)

// Test sends allowed per principal per window. The LFX Self-Serve BFF applies
// the same per-user cap (lfx-self-serve#5095); this is the service-side guard.
const (
	testSendLimit  = 20
	testSendWindow = time.Hour
)

// windowLimiter allows up to limit events per key in a sliding window. It is in
// memory and per pod, so the cluster-wide ceiling is limit times pods.
type windowLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	events map[string][]time.Time
}

func newWindowLimiter(limit int, window time.Duration) *windowLimiter {
	return &windowLimiter{limit: limit, window: window, events: make(map[string][]time.Time)}
}

// allow records an event for key at now and reports whether it is within the
// limit. A refused event is not recorded.
func (l *windowLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	kept := l.events[key][:0]
	for _, t := range l.events[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.events[key] = kept
		return false
	}
	l.events[key] = append(kept, now)
	return true
}
