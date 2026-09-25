// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
)

func TestUnsubscribeTokenRoundTrip(t *testing.T) {
	svc := NewUnsubscribeService(nil, []byte("test-secret"), "https://api.example/newsletter")

	url := svc.BuildURL("proj-1", "Alice@Example.com")
	if !strings.HasPrefix(url, "https://api.example/newsletter/newsletters/unsubscribe?t=") {
		t.Fatalf("unexpected url: %s", url)
	}
	token := url[strings.Index(url, "?t=")+3:]

	gotProject, gotEmail, err := svc.VerifyToken(token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if gotProject != "proj-1" {
		t.Errorf("project = %q, want proj-1", gotProject)
	}
	if gotEmail != "alice@example.com" {
		t.Errorf("email = %q, want lowercased alice@example.com", gotEmail)
	}
}

func TestUnsubscribeTokenTampered(t *testing.T) {
	svc := NewUnsubscribeService(nil, []byte("test-secret"), "https://api.example")
	good := svc.buildToken("proj-1", "alice@example.com")

	// Decode, swap email, re-encode with original MAC.
	raw, _ := base64.RawURLEncoding.DecodeString(good)
	parts := strings.Split(string(raw), "\n")
	tampered := base64.RawURLEncoding.EncodeToString([]byte(parts[0] + "\n" + "mallory@example.com" + "\n" + parts[2]))

	if _, _, err := svc.VerifyToken(tampered); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("tampered token: got %v, want ErrInvalidRequest", err)
	}
}

func TestUnsubscribeTokenWrongSecret(t *testing.T) {
	signer := NewUnsubscribeService(nil, []byte("secret-a"), "https://api.example")
	verifier := NewUnsubscribeService(nil, []byte("secret-b"), "https://api.example")

	token := signer.buildToken("proj-1", "alice@example.com")
	if _, _, err := verifier.VerifyToken(token); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("wrong secret: got %v, want ErrInvalidRequest", err)
	}
}

func TestUnsubscribeTokenMalformed(t *testing.T) {
	svc := NewUnsubscribeService(nil, []byte("test-secret"), "https://api.example")
	for _, in := range []string{"", "not-base64!!!", base64.RawURLEncoding.EncodeToString([]byte("only-two\nfields"))} {
		if _, _, err := svc.VerifyToken(in); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Errorf("input %q: got %v, want ErrInvalidRequest", in, err)
		}
	}
}

func TestUnsubscribeEnabled(t *testing.T) {
	if NewUnsubscribeService(nil, nil, "https://x").Enabled() {
		t.Error("no secret should be disabled")
	}
	if NewUnsubscribeService(nil, []byte("k"), "").Enabled() {
		t.Error("no baseURL should be disabled")
	}
	var nilSvc *UnsubscribeService
	if nilSvc.Enabled() {
		t.Error("nil receiver should be disabled")
	}
	if !NewUnsubscribeService(nil, []byte("k"), "https://x").Enabled() {
		t.Error("configured service should be enabled")
	}
}
