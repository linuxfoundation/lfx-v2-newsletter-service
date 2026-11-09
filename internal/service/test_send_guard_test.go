// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service/render/declarative"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

func guardOrchestrator(email *fakeEmailDispatcher, userEmail *fakeUserEmailReader) *SendOrchestrator {
	repo := newFakeRepo()
	unsub := NewUnsubscribeService(repo, []byte("k"), "https://api.example")
	return newTestOrchestratorWithUserEmail(repo, &fakeCommitteeClient{}, email, unsub, &fakeUserMetadataReader{name: "Tester"}, userEmail)
}

func guardInput(to, tokenEmail string) TestSendInput {
	return TestSendInput{ProjectUID: "p1", Subject: "Hi", BodyHTML: "<p>hi</p>", ToEmail: to, Principal: "jdoe", PrincipalEmail: tokenEmail}
}

// Deployed tokens carry no email claim, so the guard has to work from the
// auth-service primary email alone.
func TestTestSendGuardFallsBackToPrimaryEmail(t *testing.T) {
	cases := []struct {
		name       string
		userEmail  *fakeUserEmailReader
		to         string
		tokenEmail string
		wantErr    error
		wantSends  int
	}{
		{name: "primary email matches", userEmail: &fakeUserEmailReader{email: "me@example.com"}, to: "Me@Example.com", wantSends: 1},
		{name: "reply-to domain filter does not apply", userEmail: &fakeUserEmailReader{email: "me@gmail.com"}, to: "me@gmail.com", wantSends: 1},
		{name: "primary email differs", userEmail: &fakeUserEmailReader{email: "me@example.com"}, to: "other@example.com", wantErr: domain.ErrForbidden},
		{name: "no primary email", userEmail: &fakeUserEmailReader{}, to: "me@example.com", wantErr: domain.ErrForbidden},
		{name: "user not found", userEmail: &fakeUserEmailReader{err: pkgerrors.NewNotFound("user not found")}, to: "me@example.com", wantErr: domain.ErrForbidden},
		{name: "token email wins over primary email", userEmail: &fakeUserEmailReader{email: "primary@example.com"}, to: "token@example.com", tokenEmail: "token@example.com", wantSends: 1},
		{name: "token email refuses the primary email", userEmail: &fakeUserEmailReader{email: "primary@example.com"}, to: "primary@example.com", tokenEmail: "token@example.com", wantErr: domain.ErrForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			email := &fakeEmailDispatcher{}
			err := guardOrchestrator(email, tc.userEmail).TestSend(context.Background(), guardInput(tc.to, tc.tokenEmail))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if len(email.sends) != tc.wantSends {
				t.Fatalf("sends = %d, want %d", len(email.sends), tc.wantSends)
			}
		})
	}
}

func TestTestSendGuardReportsAnAuthServiceOutageAsUnavailable(t *testing.T) {
	email := &fakeEmailDispatcher{}
	orch := guardOrchestrator(email, &fakeUserEmailReader{err: errors.New("nats: timeout")})
	err := orch.TestSend(context.Background(), guardInput("me@example.com", ""))
	var unavailable pkgerrors.ServiceUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want ServiceUnavailable", err)
	}
	if len(email.sends) != 0 {
		t.Fatalf("sends = %d, want 0", len(email.sends))
	}
}

func brokenLayoutInput() TestSendInput {
	in := guardInput("me@example.com", "me@example.com")
	in.BodyHTML = ""
	in.BodyLayout = &declarative.Layout{Blocks: []declarative.Block{{BlockType: "does_not_exist", Content: map[string]any{"text": "x"}}}}
	return in
}

func TestTestSendLayoutThatFailsToRenderDoesNotUseTheAllowance(t *testing.T) {
	ctx := context.Background()
	orch := guardOrchestrator(&fakeEmailDispatcher{}, &fakeUserEmailReader{})
	for i := 0; i < testSendLimit; i++ {
		if err := orch.TestSend(ctx, brokenLayoutInput()); !errors.Is(err, domain.ErrUnprocessable) {
			t.Fatalf("broken layout %d: got %v, want ErrUnprocessable", i+1, err)
		}
	}
	if err := orch.TestSend(ctx, guardInput("me@example.com", "me@example.com")); err != nil {
		t.Fatalf("valid send after failed renders: %v", err)
	}
}

// A broken layout would return ErrUnprocessable if the render ran, so
// ErrRateLimited proves the refusal comes first.
func TestTestSendOverTheLimitStopsBeforeRendering(t *testing.T) {
	ctx := context.Background()
	orch := guardOrchestrator(&fakeEmailDispatcher{}, &fakeUserEmailReader{})
	for i := 0; i < testSendLimit; i++ {
		if err := orch.TestSend(ctx, guardInput("me@example.com", "me@example.com")); err != nil {
			t.Fatalf("send %d: %v", i+1, err)
		}
	}
	if err := orch.TestSend(ctx, brokenLayoutInput()); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("got %v, want ErrRateLimited", err)
	}
}

func TestWindowLimiterDropsIdleKeys(t *testing.T) {
	l := newWindowLimiter(2, time.Hour)
	start := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	l.allow("idle", start)
	l.allow("active", start.Add(61*time.Minute))
	if _, ok := l.events["idle"]; ok {
		t.Fatal("idle key was not removed")
	}
	if _, ok := l.events["active"]; !ok {
		t.Fatal("active key was removed")
	}
}

func TestWindowLimiterExhaustedRecordsNothing(t *testing.T) {
	l := newWindowLimiter(1, time.Hour)
	start := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	if l.exhausted("u", start) {
		t.Fatal("exhausted before any event")
	}
	if !l.allow("u", start) {
		t.Fatal("first event refused, so exhausted recorded one")
	}
	if !l.exhausted("u", start.Add(time.Minute)) {
		t.Fatal("not exhausted at the limit")
	}
	if l.exhausted("u", start.Add(61*time.Minute)) {
		t.Fatal("still exhausted after the window")
	}
}
