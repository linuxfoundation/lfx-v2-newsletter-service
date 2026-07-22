// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package sendgrid

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	// SendGrid signed-event-webhook headers. The signed message is the
	// timestamp string concatenated with the raw request body.
	signatureHeader = "X-Twilio-Email-Event-Webhook-Signature"
	timestampHeader = "X-Twilio-Email-Event-Webhook-Timestamp"

	// maxWebhookBytes bounds a single webhook POST. A SendGrid batch tops out
	// around a few hundred KB; 5 MB is comfortable headroom without being an
	// unbounded-read DoS.
	maxWebhookBytes = 5 << 20
)

// Verifier checks SendGrid Signed Event Webhook signatures with the ECDSA
// public key from the webhook settings (base64-encoded PKIX DER).
type Verifier struct {
	pub *ecdsa.PublicKey
}

// NewVerifier parses the base64 PKIX ECDSA public key SendGrid exposes for the
// signed event webhook.
func NewVerifier(publicKeyB64 string) (*Verifier, error) {
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyB64))
	if err != nil {
		return nil, fmt.Errorf("sendgrid webhook: decode public key: %w", err)
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("sendgrid webhook: parse public key: %w", err)
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("sendgrid webhook: public key is not ECDSA")
	}
	return &Verifier{pub: ec}, nil
}

// Verify reports whether signatureB64 is a valid ECDSA signature over
// (timestamp + payload) for the configured public key. Any malformed input
// returns false rather than erroring — an unverifiable request is simply rejected.
func (v *Verifier) Verify(payload []byte, signatureB64, timestamp string) bool {
	if v == nil || v.pub == nil || signatureB64 == "" || timestamp == "" {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return false
	}
	h := sha256.New()
	_, _ = h.Write([]byte(timestamp))
	_, _ = h.Write(payload)
	return ecdsa.VerifyASN1(v.pub, h.Sum(nil), sig)
}

// event is one entry in a SendGrid event-webhook batch. email_id / group_id are
// the custom_args set at send time, echoed back at the top level of each event.
type event struct {
	Email     string `json:"email"`
	Event     string `json:"event"`
	Timestamp int64  `json:"timestamp"`
	SGEventID string `json:"sg_event_id"`
	EmailID   string `json:"email_id"`
	GroupID   string `json:"group_id"`
}

// Webhook is the HTTP handler for SendGrid's event webhook. It verifies the
// signature, parses the batch, and applies delivered / open / failure events to
// the engagement store. Application is idempotent (the store dedups opens by
// sg_event_id and delivered/failed are first-event-wins), so SendGrid retrying
// a whole batch on a non-2xx response is safe.
type Webhook struct {
	verifier *Verifier
	store    EngagementStore
}

// NewWebhook wires a Webhook. Both the verifier and store are required.
func NewWebhook(verifier *Verifier, store EngagementStore) *Webhook {
	return &Webhook{verifier: verifier, store: store}
}

// ServeHTTP implements http.Handler. It is registered on a public (no user
// auth) route; the ECDSA signature is the authenticity gate.
func (wh *Webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBytes))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	if !wh.verifier.Verify(body, r.Header.Get(signatureHeader), r.Header.Get(timestampHeader)) {
		slog.WarnContext(ctx, "sendgrid webhook: signature verification failed")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var events []event
	if err := json.Unmarshal(body, &events); err != nil {
		http.Error(w, "malformed event payload", http.StatusBadRequest)
		return
	}

	var applyErr error
	for _, ev := range events {
		if err := wh.apply(ctx, ev); err != nil {
			// Remember the first store error but keep processing the batch; a
			// non-2xx below asks SendGrid to redeliver, and application is
			// idempotent so the reapply is safe.
			if applyErr == nil {
				applyErr = err
			}
		}
	}
	if applyErr != nil {
		slog.ErrorContext(ctx, "sendgrid webhook: one or more events failed to apply; requesting redelivery", "error", applyErr)
		http.Error(w, "event apply failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apply maps one SendGrid event to the engagement store. Events without our
// email_id custom_arg, or of a type we don't track, are skipped.
func (wh *Webhook) apply(ctx context.Context, ev event) error {
	if ev.EmailID == "" {
		slog.DebugContext(ctx, "sendgrid webhook: event without email_id custom_arg, skipping", "event", ev.Event)
		return nil
	}
	at := time.Unix(ev.Timestamp, 0).UTC()
	switch ev.Event {
	case "delivered":
		return wh.store.ApplyDelivered(ctx, ev.EmailID, at)
	case "open":
		return wh.store.ApplyOpen(ctx, ev.SGEventID, ev.EmailID, at)
	case "bounce", "dropped", "spamreport", "blocked":
		return wh.store.ApplyFailed(ctx, ev.EmailID, at)
	default:
		// processed / deferred / click / unsubscribe / group_* — not tracked here.
		return nil
	}
}
