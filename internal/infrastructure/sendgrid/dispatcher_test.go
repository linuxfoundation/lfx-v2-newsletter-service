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
	"time"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// recordSentArgs captures a RecordSent call for assertions.
type recordSentArgs struct {
	emailID, groupID, to string
	sentAt               time.Time
}

// fakeStore is an in-memory EngagementStore for dispatcher and webhook tests.
type fakeStore struct {
	sent       []recordSentArgs
	delivered  []string // emailIDs
	failed     []string // emailIDs
	opens      []string // "sgEventID|emailID"
	applyErr   error    // when set, the Apply* methods return it
	engagement *port.EmailEngagement
	byEmailID  *port.EmailRecipientRecord
	byGroupID  []port.EmailRecipientRecord
}

func (f *fakeStore) RecordSent(_ context.Context, emailID, groupID, to string, sentAt time.Time) error {
	f.sent = append(f.sent, recordSentArgs{emailID, groupID, to, sentAt})
	return nil
}
func (f *fakeStore) ApplyDelivered(_ context.Context, emailID string, _ time.Time) error {
	f.delivered = append(f.delivered, emailID)
	return f.applyErr
}
func (f *fakeStore) ApplyOpen(_ context.Context, sgEventID, emailID string, _ time.Time) error {
	f.opens = append(f.opens, sgEventID+"|"+emailID)
	return f.applyErr
}
func (f *fakeStore) ApplyFailed(_ context.Context, emailID string, _ time.Time) error {
	f.failed = append(f.failed, emailID)
	return f.applyErr
}
func (f *fakeStore) Engagement(context.Context, string) (*port.EmailEngagement, error) {
	return f.engagement, nil
}
func (f *fakeStore) RecipientByEmailID(context.Context, string) (*port.EmailRecipientRecord, error) {
	return f.byEmailID, nil
}
func (f *fakeStore) RecipientsByGroupID(context.Context, string) ([]port.EmailRecipientRecord, error) {
	return f.byGroupID, nil
}

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

func TestSendEmail_RecordsSentToStore(t *testing.T) {
	store := &fakeStore{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	d, err := NewDispatcher(Config{
		APIKey: "k", DefaultFrom: "f@lfx.aaif.io",
		BaseURL: srv.URL, HTTPClient: srv.Client(), Store: store,
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	emailID, err := d.SendEmail(context.Background(), port.SendEmailInput{
		To: "a@example.com", Subject: "s", Text: "t", GroupID: "grp-9",
	})
	if err != nil {
		t.Fatalf("SendEmail: %v", err)
	}
	if len(store.sent) != 1 {
		t.Fatalf("RecordSent calls = %d, want 1", len(store.sent))
	}
	got := store.sent[0]
	if got.emailID != emailID || got.groupID != "grp-9" || got.to != "a@example.com" {
		t.Errorf("RecordSent args = %+v, want emailID=%s group=grp-9 to=a@example.com", got, emailID)
	}
	if got.sentAt.IsZero() {
		t.Errorf("RecordSent sentAt should be set")
	}
}

func TestReadsDelegateToStore(t *testing.T) {
	store := &fakeStore{
		engagement: &port.EmailEngagement{GroupID: "g", TotalSent: 3, Delivered: 2, Opened: 1},
		byEmailID:  &port.EmailRecipientRecord{EmailID: "e", Delivered: true},
		byGroupID:  []port.EmailRecipientRecord{{EmailID: "e1"}, {EmailID: "e2"}},
	}
	d, err := NewDispatcher(Config{APIKey: "k", DefaultFrom: "f@lfx.aaif.io", Store: store})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if eng, err := d.GetEngagement(context.Background(), "g"); err != nil || eng == nil || eng.TotalSent != 3 {
		t.Errorf("GetEngagement = %+v, %v; want TotalSent=3", eng, err)
	}
	if rec, err := d.GetStatusByEmailID(context.Background(), "e"); err != nil || rec == nil || !rec.Delivered {
		t.Errorf("GetStatusByEmailID = %+v, %v; want Delivered record", rec, err)
	}
	if recs, err := d.GetStatusByGroupID(context.Background(), "g"); err != nil || len(recs) != 2 {
		t.Errorf("GetStatusByGroupID len = %d, %v; want 2", len(recs), err)
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
