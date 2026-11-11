// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
)

type stubProject struct{}

func (stubProject) Name(context.Context, string) (string, error) { return "Project", nil }
func (stubProject) Slug(context.Context, string) (string, error) { return "project", nil }

type stubUser struct {
	primary  string
	lookedUp []string
}

func (*stubUser) Name(context.Context, string) (string, error) { return "Tester", nil }
func (u *stubUser) PrimaryEmail(_ context.Context, principal string) (string, error) {
	u.lookedUp = append(u.lookedUp, principal)
	return u.primary, nil
}

// Runs a request through withAuth and the TestSend handler, so a token's email
// claim has to reach the guard for the claim cases to pass. The token with no
// email claim is the shape Heimdall issues today.
func TestTestSendHandlerAppliesTheOwnAddressGuard(t *testing.T) {
	validator, key := testValidator(t)
	cases := []struct {
		name       string
		claims     jwt.MapClaims
		primary    string
		to         string
		wantStatus int
	}{
		{"no email claim, primary email matches", jwt.MapClaims{"principal": "jdoe"}, "me@example.com", "me@example.com", http.StatusOK},
		{"no email claim, another address", jwt.MapClaims{"principal": "jdoe"}, "me@example.com", "other@example.com", http.StatusForbidden},
		{"no email claim, no primary email", jwt.MapClaims{"principal": "jdoe"}, "", "me@example.com", http.StatusForbidden},
		{"email claim is used over primary email", jwt.MapClaims{"principal": "jdoe", "email": "token@example.com"}, "primary@example.com", "token@example.com", http.StatusOK},
		{"email claim refuses primary email", jwt.MapClaims{"principal": "jdoe", "email": "token@example.com"}, "primary@example.com", "primary@example.com", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user := &stubUser{primary: tc.primary}
			// FanoutEnabled is left false, so an allowed send stops before dispatch.
			h := &Handler{auth: validator, requireUserAuth: true, send: service.NewSendOrchestrator(service.SendOrchestratorConfig{
				Project:      stubProject{},
				UserMetadata: user,
				UserEmail:    user,
			})}
			body := `{"subject":"Hi","body_html":"<p>hi</p>","to_email":"` + tc.to + `"}`
			req := httptest.NewRequest(http.MethodPost, "/projects/p1/newsletters/test-send", strings.NewReader(body))
			req.SetPathValue("project_uid", "p1")
			req.Header.Set("Authorization", "Bearer "+signedToken(t, key, tc.claims))
			rec := httptest.NewRecorder()
			h.withAuth(http.HandlerFunc(h.TestSend)).ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			for _, principal := range user.lookedUp {
				if principal != "jdoe" {
					t.Fatalf("looked up %q, want the token's principal", principal)
				}
			}
		})
	}
}
