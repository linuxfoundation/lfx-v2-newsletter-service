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
	"strconv"
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

	// webhookTimestampTolerance bounds how far the signed timestamp may drift
	// from now. The signature alone does not prevent replay of a captured
	// (body, signature, timestamp) triple on this public route, so we also
	// reject a stale timestamp. Wide enough to absorb clock skew and delivery
	// latency.
	webhookTimestampTolerance = 10 * time.Minute
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

// freshTimestamp reports whether the SendGrid signed-webhook timestamp (Unix
// seconds) is within webhookTimestampTolerance of now. A malformed timestamp is
// treated as not fresh.
func freshTimestamp(ts string, now time.Time) bool {
	secs, err := strconv.ParseInt(strings.TrimSpace(ts), 10, 64)
	if err != nil {
		return false
	}
	delta := now.Sub(time.Unix(secs, 0))
	if delta < 0 {
		delta = -delta
	}
	return delta <= webhookTimestampTolerance
}

// event is one entry in a SendGrid event-webhook batch. email_id / group_id are
// the custom_args set at send time, echoed back at the top level of each event.
// URL is only present on click events.
type event struct {
	Email     string `json:"email"`
	Event     string `json:"event"`
	Timestamp int64  `json:"timestamp"`
	SGEventID string `json:"sg_event_id"`
	EmailID   string `json:"email_id"`
	GroupID   string `json:"group_id"`
	URL       string `json:"url"`
}

// maxClickURLLen bounds how much of a click event's url we persist. SendGrid's
// click-tracking redirect URLs are short, but this is a defensive cap against a
// pathologically long author-authored link rather than a limit we expect to hit.
const maxClickURLLen = 2048

// trimURLToMaxLen trims url to at most maxClickURLLen bytes, respecting UTF-8
// rune boundaries. If trimming cuts through a multi-byte UTF-8 sequence, the
// partial rune is removed to avoid invalid UTF-8.
func trimURLToMaxLen(url string) string {
	if len(url) <= maxClickURLLen {
		return url
	}
	// Trim to maxClickURLLen bytes, then walk back to find a valid UTF-8 boundary.
	trimmed := url[:maxClickURLLen]
	// Walk backwards from the end: if the last byte is a continuation byte
	// (10xxxxxx), keep walking back until we find a non-continuation byte
	// or reach the start. If that byte is a valid rune start (0xxxxxxx or 11xxxxxx),
	// keep it and the bytes before it. Otherwise, the rune was incomplete, so trim
	// those bytes as well.
	for len(trimmed) > 0 && (trimmed[len(trimmed)-1]&0xc0) == 0x80 {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return trimmed
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
	timestamp := r.Header.Get(timestampHeader)
	if !wh.verifier.Verify(body, r.Header.Get(signatureHeader), timestamp) {
		slog.WarnContext(ctx, "sendgrid webhook: signature verification failed")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	// The signature is authentic, but a captured triple could be replayed on
	// this public route; reject a timestamp outside the tolerance window.
	if !freshTimestamp(timestamp, time.Now()) {
		slog.WarnContext(ctx, "sendgrid webhook: signed timestamp is stale or invalid, rejecting")
		http.Error(w, "stale timestamp", http.StatusUnauthorized)
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

// apply maps one SendGrid event to the engagement store. Events of a type we
// don't track are skipped, as are events missing the identity a tracked event
// must carry.
func (wh *Webhook) apply(ctx context.Context, ev event) error {
	// A tracked event carries both custom_args (email_id + group_id) set at send
	// time. Require both: the store's Apply* methods self-heal by upserting from
	// these values, so an empty group_id would create an engagement row under a
	// blank group that no newsletter references and analytics can never read back.
	if ev.EmailID == "" || ev.GroupID == "" {
		slog.DebugContext(ctx, "sendgrid webhook: event missing email_id/group_id custom_arg, skipping",
			"event", ev.Event, "has_email_id", ev.EmailID != "", "has_group_id", ev.GroupID != "")
		return nil
	}
	at := time.Unix(ev.Timestamp, 0).UTC()
	switch ev.Event {
	case "delivered":
		return wh.store.ApplyDelivered(ctx, ev.EmailID, ev.GroupID, ev.Email, at)
	case "open":
		// sg_event_id is the open-event primary key and the dedup key. An empty
		// value would collide across every open on the blank key (the first would
		// insert, the rest would be swallowed by ON CONFLICT DO NOTHING), so skip
		// an open that lacks it rather than mis-record it.
		if ev.SGEventID == "" {
			slog.DebugContext(ctx, "sendgrid webhook: open event without sg_event_id, skipping", "email_id", ev.EmailID)
			return nil
		}
		return wh.store.ApplyOpen(ctx, ev.SGEventID, ev.EmailID, ev.GroupID, ev.Email, at)
	case "click":
		// sg_event_id is the dedup key, same reasoning as the open case.
		if ev.SGEventID == "" {
			slog.DebugContext(ctx, "sendgrid webhook: click event without sg_event_id, skipping", "email_id", ev.EmailID)
			return nil
		}
		url := trimURLToMaxLen(ev.URL)
		return wh.store.ApplyClick(ctx, ev.SGEventID, ev.EmailID, ev.GroupID, ev.Email, url, at)
	case "bounce", "dropped", "spamreport", "blocked":
		return wh.store.ApplyFailed(ctx, ev.EmailID, ev.GroupID, ev.Email, at)
	default:
		// processed / deferred / unsubscribe / group_* — not tracked here.
		return nil
	}
}
