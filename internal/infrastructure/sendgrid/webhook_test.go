// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package sendgrid

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// nowTS returns the current Unix time as the string the SendGrid signed webhook
// sends in its timestamp header, so ServeHTTP's freshness check passes.
func nowTS() string {
	return strconv.FormatInt(time.Now().Unix(), 10)
}

func newTestVerifier(t *testing.T) (*Verifier, *ecdsa.PrivateKey) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	v, err := NewVerifier(base64.StdEncoding.EncodeToString(der))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v, priv
}

func signPayload(t *testing.T, priv *ecdsa.PrivateKey, timestamp string, payload []byte) string {
	t.Helper()
	h := sha256.New()
	_, _ = h.Write([]byte(timestamp))
	_, _ = h.Write(payload)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, h.Sum(nil))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

func TestVerifier_ValidTamperedAndWrongTimestamp(t *testing.T) {
	v, priv := newTestVerifier(t)
	ts := "1700000000"
	body := []byte(`[{"event":"delivered"}]`)
	sig := signPayload(t, priv, ts, body)

	if !v.Verify(body, sig, ts) {
		t.Errorf("a valid signature should verify")
	}
	if v.Verify([]byte(`[{"event":"delivereD"}]`), sig, ts) {
		t.Errorf("a tampered payload must not verify")
	}
	if v.Verify(body, sig, "1700000001") {
		t.Errorf("a mismatched timestamp must not verify")
	}
	if v.Verify(body, "not-base64-sig!!", ts) {
		t.Errorf("a malformed signature must not verify")
	}
}

func TestWebhook_AppliesVerifiedEvents(t *testing.T) {
	v, priv := newTestVerifier(t)
	store := &fakeStore{}
	wh := NewWebhook(v, store)

	body := []byte(`[
	  {"event":"delivered","email":"a@x.io","timestamp":1700000000,"sg_event_id":"e1","email_id":"m1","group_id":"g"},
	  {"event":"open","email":"a@x.io","timestamp":1700000100,"sg_event_id":"e2","email_id":"m1","group_id":"g"},
	  {"event":"click","email":"a@x.io","timestamp":1700000150,"sg_event_id":"e6","email_id":"m1","group_id":"g","url":"https://example.com/content"},
	  {"event":"bounce","email":"b@x.io","timestamp":1700000200,"sg_event_id":"e3","email_id":"m2","group_id":"g"},
	  {"event":"processed","email":"c@x.io","timestamp":1700000300,"sg_event_id":"e4","email_id":"m3","group_id":"g"},
	  {"event":"open","email":"d@x.io","timestamp":1700000400,"sg_event_id":"e5","group_id":"g"}
	]`)
	ts := nowTS()
	req := httptest.NewRequest(http.MethodPost, "/newsletters/sendgrid/events", bytes.NewReader(body))
	req.Header.Set(signatureHeader, signPayload(t, priv, ts, body))
	req.Header.Set(timestampHeader, ts)
	rec := httptest.NewRecorder()

	wh.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if len(store.delivered) != 1 || store.delivered[0] != "m1" {
		t.Errorf("delivered = %v, want [m1]", store.delivered)
	}
	if len(store.failed) != 1 || store.failed[0] != "m2" {
		t.Errorf("failed (bounce) = %v, want [m2]", store.failed)
	}
	// The open with an email_id is applied; the one without (e5) is skipped, and
	// the "processed" event is not tracked.
	if len(store.opens) != 1 || store.opens[0] != "e2|m1" {
		t.Errorf("opens = %v, want [e2|m1]", store.opens)
	}
	if len(store.clicks) != 1 || store.clicks[0] != "e6|m1|https://example.com/content" {
		t.Errorf("clicks = %v, want [e6|m1|https://example.com/content]", store.clicks)
	}
}

func TestWebhook_SkipsIncompleteTrackedEvents(t *testing.T) {
	v, priv := newTestVerifier(t)
	store := &fakeStore{}
	wh := NewWebhook(v, store)

	// A delivered/failed event with an empty group_id must not reach the store
	// (its self-heal would create a blank-group row); an open with an empty
	// sg_event_id must not reach the store (it would collide on the blank PK). A
	// complete event in the same batch still applies.
	body := []byte(`[
	  {"event":"delivered","email":"a@x.io","timestamp":1700000000,"sg_event_id":"e1","email_id":"m1","group_id":""},
	  {"event":"bounce","email":"b@x.io","timestamp":1700000100,"sg_event_id":"e2","email_id":"m2","group_id":""},
	  {"event":"open","email":"c@x.io","timestamp":1700000200,"sg_event_id":"","email_id":"m3","group_id":"g"},
	  {"event":"click","email":"e@x.io","timestamp":1700000250,"sg_event_id":"","email_id":"m5","group_id":"g","url":"https://example.com"},
	  {"event":"click","email":"f@x.io","timestamp":1700000260,"sg_event_id":"e6","email_id":"m6","group_id":"","url":"https://example.com"},
	  {"event":"delivered","email":"d@x.io","timestamp":1700000300,"sg_event_id":"e4","email_id":"m4","group_id":"g"}
	]`)
	ts := nowTS()
	req := httptest.NewRequest(http.MethodPost, "/newsletters/sendgrid/events", bytes.NewReader(body))
	req.Header.Set(signatureHeader, signPayload(t, priv, ts, body))
	req.Header.Set(timestampHeader, ts)
	rec := httptest.NewRecorder()

	wh.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	// Only the complete delivered (m4) applies; the empty-group_id delivered/bounce
	// and the empty-sg_event_id open are skipped.
	if len(store.delivered) != 1 || store.delivered[0] != "m4" {
		t.Errorf("delivered = %v, want [m4]", store.delivered)
	}
	if len(store.failed) != 0 {
		t.Errorf("failed = %v, want [] (empty group_id must be skipped)", store.failed)
	}
	if len(store.opens) != 0 {
		t.Errorf("opens = %v, want [] (empty sg_event_id must be skipped)", store.opens)
	}
	// The click missing sg_event_id and the click missing group_id are both
	// skipped, same reasoning as opens/delivered above.
	if len(store.clicks) != 0 {
		t.Errorf("clicks = %v, want [] (empty sg_event_id/group_id must be skipped)", store.clicks)
	}
}

func TestWebhook_InvalidSignatureIs401(t *testing.T) {
	v, _ := newTestVerifier(t)
	wh := NewWebhook(v, &fakeStore{})
	body := []byte(`[{"event":"delivered","email_id":"m1"}]`)
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
	req.Header.Set(signatureHeader, base64.StdEncoding.EncodeToString([]byte("wrong")))
	req.Header.Set(timestampHeader, "123")
	rec := httptest.NewRecorder()

	wh.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestWebhook_StaleTimestampIs401(t *testing.T) {
	v, priv := newTestVerifier(t)
	store := &fakeStore{}
	wh := NewWebhook(v, store)
	body := []byte(`[{"event":"delivered","email_id":"m1","group_id":"g","timestamp":1700000000}]`)
	// Correctly signed, but the timestamp is an hour old: the signature verifies
	// yet the freshness check must reject it (replay protection).
	ts := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
	req.Header.Set(signatureHeader, signPayload(t, priv, ts, body))
	req.Header.Set(timestampHeader, ts)
	rec := httptest.NewRecorder()

	wh.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a stale timestamp", rec.Code)
	}
	if len(store.delivered) != 0 {
		t.Errorf("no events should apply for a stale timestamp; got delivered=%v", store.delivered)
	}
}

func TestWebhook_StoreErrorRequestsRedelivery(t *testing.T) {
	v, priv := newTestVerifier(t)
	store := &fakeStore{applyErr: errors.New("db down")}
	wh := NewWebhook(v, store)
	body := []byte(`[{"event":"delivered","email_id":"m1","group_id":"g","timestamp":1700000000}]`)
	ts := nowTS()
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(body))
	req.Header.Set(signatureHeader, signPayload(t, priv, ts, body))
	req.Header.Set(timestampHeader, ts)
	rec := httptest.NewRecorder()

	wh.ServeHTTP(rec, req)

	// A store failure returns non-2xx so SendGrid redelivers the batch
	// (application is idempotent, so reapply is safe).
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
