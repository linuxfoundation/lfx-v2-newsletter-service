// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package handler

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
)

// keyfuncSet names the interface so staticKeyfunc can embed it without its
// field clashing with the Keyfunc method.
type keyfuncSet = keyfunc.Keyfunc

// staticKeyfunc serves one RSA public key. Only Keyfunc is used by validate;
// the embedded interface leaves the other methods unimplemented.
type staticKeyfunc struct {
	keyfuncSet
	pub *rsa.PublicKey
}

func (s staticKeyfunc) Keyfunc(*jwt.Token) (any, error) { return s.pub, nil }

func signedToken(t *testing.T, key *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	claims["aud"] = "lfx-v2-newsletter-service"
	claims["exp"] = time.Now().Add(time.Hour).Unix()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func testValidator(t *testing.T) (*AuthValidator, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &AuthValidator{jwks: staticKeyfunc{pub: &key.PublicKey}, expectedAudience: "lfx-v2-newsletter-service"}, key
}

func TestWithAuthPutsTheEmailClaimOnTheContext(t *testing.T) {
	validator, key := testValidator(t)
	h := &Handler{auth: validator, requireUserAuth: true}
	cases := []struct {
		name      string
		claims    jwt.MapClaims
		wantUser  string
		wantEmail string
	}{
		{"principal and email", jwt.MapClaims{"principal": "jdoe", "email": " jdoe@example.com "}, "jdoe", "jdoe@example.com"},
		{"no email claim", jwt.MapClaims{"principal": "jdoe"}, "jdoe", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotUser, gotEmail string
			next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				gotUser, gotEmail = UserFromContext(r.Context()), UserEmailFromContext(r.Context())
			})
			req := httptest.NewRequest(http.MethodPost, "/projects/p1/newsletters/test-send", nil)
			req.Header.Set("Authorization", "Bearer "+signedToken(t, key, tc.claims))
			rec := httptest.NewRecorder()
			h.withAuth(next).ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if gotUser != tc.wantUser || gotEmail != tc.wantEmail {
				t.Fatalf("context = (%q, %q), want (%q, %q)", gotUser, gotEmail, tc.wantUser, tc.wantEmail)
			}
		})
	}
}

func TestClassifyErrorMapsTheTestSendGuards(t *testing.T) {
	cases := []struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		{fmt.Errorf("%w: not your address", domain.ErrForbidden), http.StatusForbidden, "forbidden"},
		{fmt.Errorf("%w: slow down", domain.ErrRateLimited), http.StatusTooManyRequests, "rate_limited"},
	}
	for _, tc := range cases {
		status, code := classifyError(tc.err)
		if status != tc.wantStatus || code != tc.wantCode {
			t.Fatalf("classifyError(%v) = (%d, %q), want (%d, %q)", tc.err, status, code, tc.wantStatus, tc.wantCode)
		}
	}
}
