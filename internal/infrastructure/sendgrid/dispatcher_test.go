// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package sendgrid

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

func newTestDispatcher(t *testing.T, handler http.HandlerFunc) *Dispatcher {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	d, err := NewDispatcher(Config{
		APIKey:          "SG.test",
		DefaultFrom:     "newsletter@lfx.aaif.io",
		DefaultFromName: "AAIF Newsletter",
		BaseURL:         srv.URL,
		HTTPClient:      srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	return d
}

func TestSendEmail_Success(t *testing.T) {
	var gotPath, gotMethod, gotAuth, gotCT string
	var gotBody mailSendRequest
	d := newTestDispatcher(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusAccepted)
	})

	emailID, err := d.SendEmail(context.Background(), port.SendEmailInput{
		To:              "alice@example.com",
		Subject:         "Hello",
		HTML:            "<p>Hi</p>",
		Text:            "Hi",
		From:            "ed@lfx.aaif.io",
		FromDisplayName: "AAIF ED",
		ReplyTo:         "reply@lfx.aaif.io",
		GroupID:         "grp-123",
	})
	if err != nil {
		t.Fatalf("SendEmail: %v", err)
	}
	if emailID == "" {
		t.Errorf("expected a non-empty email_id tracking handle")
	}
	if gotMethod != http.MethodPost || gotPath != mailSendPath {
		t.Errorf("request = %s %s, want POST %s", gotMethod, gotPath, mailSendPath)
	}
	if gotAuth != "Bearer SG.test" {
		t.Errorf("Authorization = %q, want Bearer SG.test", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
	if len(gotBody.Personalizations) != 1 || len(gotBody.Personalizations[0].To) != 1 ||
		gotBody.Personalizations[0].To[0].Email != "alice@example.com" {
		t.Errorf("personalization recipient wrong: %+v", gotBody.Personalizations)
	}
	if gotBody.From.Email != "ed@lfx.aaif.io" || gotBody.From.Name != "AAIF ED" {
		t.Errorf("from = %+v, want ed@lfx.aaif.io / AAIF ED", gotBody.From)
	}
	if gotBody.ReplyTo == nil || gotBody.ReplyTo.Email != "reply@lfx.aaif.io" {
		t.Errorf("reply_to = %+v, want reply@lfx.aaif.io", gotBody.ReplyTo)
	}
	if gotBody.Subject != "Hello" {
		t.Errorf("subject = %q, want Hello", gotBody.Subject)
	}
	// SendGrid requires text/plain ordered before text/html.
	if len(gotBody.Content) != 2 || gotBody.Content[0].Type != "text/plain" || gotBody.Content[1].Type != "text/html" {
		t.Errorf("content order wrong: %+v", gotBody.Content)
	}
	if gotBody.CustomArgs[customArgGroupID] != "grp-123" {
		t.Errorf("custom_args group_id = %q, want grp-123", gotBody.CustomArgs[customArgGroupID])
	}
	// The returned handle must equal the email_id carried in custom_args — this is
	// the webhook back-map contract.
	if gotBody.CustomArgs[customArgEmailID] != emailID {
		t.Errorf("custom_args email_id = %q, want the returned handle %q", gotBody.CustomArgs[customArgEmailID], emailID)
	}
	if gotBody.MailSettings != nil {
		t.Errorf("mail_settings should be absent when SandboxMode is false")
	}
}

func TestSendEmail_DefaultFromApplied(t *testing.T) {
	var gotBody mailSendRequest
	d := newTestDispatcher(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusAccepted)
	})

	// No From and no ReplyTo, HTML-only body.
	if _, err := d.SendEmail(context.Background(), port.SendEmailInput{
		To: "a@example.com", Subject: "s", HTML: "<p>x</p>",
	}); err != nil {
		t.Fatalf("SendEmail: %v", err)
	}
	if gotBody.From.Email != "newsletter@lfx.aaif.io" || gotBody.From.Name != "AAIF Newsletter" {
		t.Errorf("default from not applied: %+v", gotBody.From)
	}
	if len(gotBody.Content) != 1 || gotBody.Content[0].Type != "text/html" {
		t.Errorf("expected a single text/html content, got %+v", gotBody.Content)
	}
	if gotBody.ReplyTo != nil {
		t.Errorf("reply_to should be omitted when empty, got %+v", gotBody.ReplyTo)
	}
}

func TestSendEmail_APIErrorSurfacesMessage(t *testing.T) {
	d := newTestDispatcher(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"message":"The from address does not match a verified Sender Identity.","field":"from"}]}`))
	})
	_, err := d.SendEmail(context.Background(), port.SendEmailInput{To: "a@example.com", Subject: "s", HTML: "<p>x</p>"})
	if err == nil {
		t.Fatalf("expected an error on a 400 response")
	}
	if !strings.Contains(err.Error(), "verified Sender Identity") {
		t.Errorf("error should surface the SendGrid message, got: %v", err)
	}
}

func TestSendEmail_Validation(t *testing.T) {
	d, err := NewDispatcher(Config{APIKey: "k", DefaultFrom: "f@lfx.aaif.io"})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := d.SendEmail(context.Background(), port.SendEmailInput{Subject: "s", Text: "t"}); err == nil {
		t.Errorf("expected a validation error for an empty To")
	}
	if _, err := d.SendEmail(context.Background(), port.SendEmailInput{To: "a@example.com", Subject: "s"}); err == nil {
		t.Errorf("expected a validation error for an empty body")
	}
}

func TestSendEmail_SandboxMode(t *testing.T) {
	var gotBody mailSendRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	d, err := NewDispatcher(Config{
		APIKey: "k", DefaultFrom: "f@lfx.aaif.io",
		BaseURL: srv.URL, HTTPClient: srv.Client(), SandboxMode: true,
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := d.SendEmail(context.Background(), port.SendEmailInput{To: "a@example.com", Subject: "s", Text: "t"}); err != nil {
		t.Fatalf("SendEmail: %v", err)
	}
	if gotBody.MailSettings == nil || gotBody.MailSettings.SandboxMode == nil || !gotBody.MailSettings.SandboxMode.Enable {
		t.Errorf("sandbox mode not set: %+v", gotBody.MailSettings)
	}
}

func TestNewDispatcher_RequiredConfig(t *testing.T) {
	if _, err := NewDispatcher(Config{DefaultFrom: "f@lfx.aaif.io"}); err == nil {
		t.Errorf("expected an error when APIKey is missing")
	}
	if _, err := NewDispatcher(Config{APIKey: "k"}); err == nil {
		t.Errorf("expected an error when DefaultFrom is missing")
	}
}

func TestEngagementReadsNotWired(t *testing.T) {
	d, err := NewDispatcher(Config{APIKey: "k", DefaultFrom: "f@lfx.aaif.io"})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := d.GetEngagement(context.Background(), "g"); err == nil {
		t.Errorf("GetEngagement should report not-wired until the webhook store lands")
	}
	if _, err := d.GetStatusByEmailID(context.Background(), "e"); err == nil {
		t.Errorf("GetStatusByEmailID should report not-wired until the webhook store lands")
	}
	if _, err := d.GetStatusByGroupID(context.Background(), "g"); err == nil {
		t.Errorf("GetStatusByGroupID should report not-wired until the webhook store lands")
	}
}
