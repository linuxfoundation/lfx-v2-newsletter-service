// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
)

// TestHandlerRoutes_Unsubscribe covers the two-stage, intentionally
// unauthenticated unsubscribe routes: both GET (read-only confirmation) and the
// state-mutating POST must reach their handler WITHOUT a bearer — authorization
// is the HMAC-signed token, not a session — even while RequireUserAuth is on for
// the rest of the API. A normal API route without a bearer stays 401, proving
// the non-401s below are these routes bypassing withAuth, not a disabled layer.
func TestHandlerRoutes_Unsubscribe(t *testing.T) {
	// A minimal unsubscribe service is enough: an invalid token fails HMAC
	// verification before any repository call, so a bad-token request yields 400
	// (reached the handler) rather than panicking on the nil repo.
	unsub := service.NewUnsubscribeService(nil, []byte("test-secret"), "https://host")
	srv := New(Config{RequireUserAuth: true, Unsubscribe: unsub}).Routes()

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(method, "/newsletters/unsubscribe?t=bad", nil))
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s /newsletters/unsubscribe returned 401 — the route must bypass JWT auth", method)
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s /newsletters/unsubscribe: status = %d, want 400 (reached the token-verifying handler)", method, rec.Code)
		}
	}

	// Contrast: a normal authenticated route without a bearer is rejected by withAuth.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/projects/p/newsletters", strings.NewReader("{}")))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("authenticated route without a bearer: status = %d, want 401", rec.Code)
	}
}

// TestHandlerRoutes_SendGridWebhook covers the conditional, anonymous SendGrid
// event-webhook route: it must be absent when the webhook handler is not
// configured, and when configured it must reach that handler without JWT auth
// (the handler self-authenticates via its ECDSA signature check), even while
// RequireUserAuth is on for the rest of the API.
func TestHandlerRoutes_SendGridWebhook(t *testing.T) {
	const webhookPath = "/newsletters/sendgrid/events"

	t.Run("absent when unconfigured", func(t *testing.T) {
		// RequireUserAuth on, but no SendGridWebhook handler wired.
		srv := New(Config{RequireUserAuth: true}).Routes()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, webhookPath, strings.NewReader("[]")))
		if rec.Code != http.StatusNotFound {
			t.Errorf("unconfigured webhook route: status = %d, want 404 (route must not be registered)", rec.Code)
		}
	})

	t.Run("configured route is anonymous", func(t *testing.T) {
		reached := false
		stub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusNoContent)
		})
		// RequireUserAuth on to prove the webhook route bypasses withAuth.
		srv := New(Config{RequireUserAuth: true, SendGridWebhook: stub}).Routes()

		// No Authorization header: the webhook handler must still be reached.
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, webhookPath, strings.NewReader("[]")))
		if !reached {
			t.Error("configured webhook handler was not reached for an unauthenticated POST")
		}
		if rec.Code != http.StatusNoContent {
			t.Errorf("webhook status = %d, want 204 (reached the signature-verifying handler)", rec.Code)
		}

		// Contrast: a normal API route with no bearer is rejected by withAuth, so
		// the 204 above is genuinely the webhook route skipping JWT auth, not a
		// disabled auth layer.
		rec2 := httptest.NewRecorder()
		srv.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/projects/p/newsletters", strings.NewReader("{}")))
		if rec2.Code != http.StatusUnauthorized {
			t.Errorf("authenticated route without a bearer: status = %d, want 401", rec2.Code)
		}
	})
}
