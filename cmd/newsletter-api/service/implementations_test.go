// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type sweepRecorder struct {
	mu            sync.Mutex
	recoverTTLs   []time.Duration
	settleCalls   int
	audienceTTLs  []time.Duration
	audienceErr   error
	recoverResult int64
	purgeCalls    int
	// failStaleBlocks makes FailStale run until its deadline passes.
	failStaleBlocks bool
	// laterStepCtxErrs records the context error each later step saw.
	laterStepCtxErrs []error
}

func (s *sweepRecorder) PurgeSentMembers(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeCalls++
	return 0, nil
}

func (s *sweepRecorder) RecoverStuckSending(ctx context.Context, olderThan time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.laterStepCtxErrs = append(s.laterStepCtxErrs, ctx.Err())
	s.recoverTTLs = append(s.recoverTTLs, olderThan)
	return s.recoverResult, nil
}

func (s *sweepRecorder) SettleDueScheduled(ctx context.Context, _ time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.laterStepCtxErrs = append(s.laterStepCtxErrs, ctx.Err())
	s.settleCalls++
	return 0, nil
}

func (s *sweepRecorder) FailStale(ctx context.Context, olderThan time.Duration) (int, error) {
	s.mu.Lock()
	s.audienceTTLs = append(s.audienceTTLs, olderThan)
	blocks := s.failStaleBlocks
	s.mu.Unlock()
	if blocks {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	return 0, s.audienceErr
}

func (s *sweepRecorder) snapshot() (recoverTTLs, audienceTTLs []time.Duration, settle int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.recoverTTLs...), append([]time.Duration(nil), s.audienceTTLs...), s.settleCalls
}

func waitForFirstSweep(t *testing.T, rec *sweepRecorder) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, settle := rec.snapshot(); settle > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the first sweep did not finish")
}

func TestRecoverySweep_FailsStaleAudiencesWithTheirOwnTTL(t *testing.T) {
	rec := &sweepRecorder{}
	startStuckSendRecovery(rec, rec, 45*time.Minute, 40*time.Minute)
	stop := recoveryStop
	defer close(stop)
	waitForFirstSweep(t, rec)

	recoverTTLs, audienceTTLs, _ := rec.snapshot()
	if len(audienceTTLs) == 0 || audienceTTLs[0] != 40*time.Minute {
		t.Errorf("FailStale ttls = %v, want [40m]", audienceTTLs)
	}
	if len(recoverTTLs) == 0 || recoverTTLs[0] != 45*time.Minute {
		t.Errorf("RecoverStuckSending ttls = %v, want [45m]", recoverTTLs)
	}
}

func TestRecoverySweep_AudienceFailureDoesNotSkipSendRecovery(t *testing.T) {
	rec := &sweepRecorder{audienceErr: errors.New("db down")}
	startStuckSendRecovery(rec, rec, 45*time.Minute, 40*time.Minute)
	stop := recoveryStop
	defer close(stop)
	waitForFirstSweep(t, rec)

	recoverTTLs, _, settle := rec.snapshot()
	if len(recoverTTLs) == 0 || settle == 0 {
		t.Errorf("send recovery ran %d times and settlement %d times, want both at least once", len(recoverTTLs), settle)
	}
}

func TestRecoverySweep_AudienceTimeoutLeavesLaterStepsTheirOwnDeadline(t *testing.T) {
	prev := stuckSendSweepTimeout
	stuckSendSweepTimeout = 50 * time.Millisecond
	t.Cleanup(func() { stuckSendSweepTimeout = prev })

	rec := &sweepRecorder{failStaleBlocks: true}
	startStuckSendRecovery(rec, rec, 45*time.Minute, 40*time.Minute)
	stop := recoveryStop
	defer close(stop)
	waitForFirstSweep(t, rec)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.laterStepCtxErrs) < 2 {
		t.Fatalf("later steps ran %d times, want recovery and settlement", len(rec.laterStepCtxErrs))
	}
	for i, err := range rec.laterStepCtxErrs[:2] {
		if err != nil {
			t.Errorf("step %d after the audience timeout got an expired context: %v", i, err)
		}
	}
}
