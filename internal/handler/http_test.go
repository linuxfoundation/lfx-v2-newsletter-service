// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandlerRoutes_Unsubscribe asserts the one-click unsubscribe route
// auth-boundary from Routes(): GET /newsletters/unsubscribe must reach its
// handler without a JWT token even when RequireUserAuth is on, and POST must
// not be registered (the two-stage variant was reverted and must stay absent).
func TestHandlerRoutes_Unsubscribe(t *testing.T) {
	const unsub = "/newsletters/unsubscribe"

	// RequireUserAuth on with no Unsubscribe service: the handler still runs
	// (returning 500 because unsub is nil) rather than being rejected by
	// withAuth (which would return 401). The status code difference is what
	// proves the route is anonymous.
	srv := New(Config{RequireUserAuth: true}).Routes()

	t.Run("GET is anonymous", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, unsub+"?t=dummy", nil))
		if rec.Code == http.StatusUnauthorized {
			t.Error("GET /newsletters/unsubscribe returned 401: route must be anonymous (no withAuth)")
		}
	})

	t.Run("POST is not registered", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, unsub, strings.NewReader("")))
		if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
			t.Errorf("POST /newsletters/unsubscribe: status = %d, want 404 or 405 (route must not be registered)", rec.Code)
		}
	})

	t.Run("normal route is protected", func(t *testing.T) {
		// Contrast: an authenticated route without a bearer is still 401,
		// proving the auth layer is actually on — the unsubscribe exemption
		// above is not from a disabled auth layer.
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/projects/p/newsletters", strings.NewReader("{}")))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("authenticated route without bearer: status = %d, want 401", rec.Code)
		}
	})
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
