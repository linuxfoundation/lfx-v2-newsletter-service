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
	// nextSweep is when allow next drops keys with no event left in the window,
	// so memory tracks recent callers and not every caller since the pod started.
	nextSweep time.Time
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
	if !now.Before(l.nextSweep) {
		l.sweep(cutoff)
		l.nextSweep = now.Add(l.window)
	}
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

// exhausted reports whether key has no allowance left at now. It records
// nothing, so a caller can check before doing work that allow will charge for.
func (l *windowLimiter) exhausted(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	count := 0
	for _, t := range l.events[key] {
		if t.After(cutoff) {
			count++
		}
	}
	return count >= l.limit
}

// sweep deletes keys whose newest event is at or before cutoff. Events are
// appended in time order, so the last one is the newest. The caller holds mu.
func (l *windowLimiter) sweep(cutoff time.Time) {
	for key, events := range l.events {
		if len(events) == 0 || !events[len(events)-1].After(cutoff) {
			delete(l.events, key)
		}
	}
}
