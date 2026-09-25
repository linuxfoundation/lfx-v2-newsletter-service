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
	clicks     []string // "sgEventID|emailID|url"
	deleted    []string // groupIDs passed to RevertGroup
	applyErr   error    // when set, the Apply* methods return it
	engagement *port.EmailEngagement
	byEmailID  *port.EmailRecipientRecord
	detail     *port.GroupEngagementDetail
}

func (f *fakeStore) RevertGroup(_ context.Context, groupID string) error {
	f.deleted = append(f.deleted, groupID)
	return nil
}
func (f *fakeStore) RecordSent(_ context.Context, emailID, groupID, to string, sentAt time.Time) error {
	f.sent = append(f.sent, recordSentArgs{emailID, groupID, to, sentAt})
	return nil
}
func (f *fakeStore) ApplyDelivered(_ context.Context, emailID, _, _ string, _ time.Time) error {
	f.delivered = append(f.delivered, emailID)
	return f.applyErr
}
func (f *fakeStore) ApplyOpen(_ context.Context, sgEventID, emailID, _, _ string, _ time.Time) error {
	f.opens = append(f.opens, sgEventID+"|"+emailID)
	return f.applyErr
}
func (f *fakeStore) ApplyClick(_ context.Context, sgEventID, emailID, _, _, url string, _ time.Time) error {
	f.clicks = append(f.clicks, sgEventID+"|"+emailID+"|"+url)
	return f.applyErr
}
func (f *fakeStore) ApplyFailed(_ context.Context, emailID, _, _ string, _ time.Time) error {
	f.failed = append(f.failed, emailID)
	return f.applyErr
}
func (f *fakeStore) Engagement(context.Context, string) (*port.EmailEngagement, error) {
	return f.engagement, nil
}
func (f *fakeStore) RecipientByEmailID(context.Context, string) (*port.EmailRecipientRecord, error) {
	return f.byEmailID, nil
}
func (f *fakeStore) RecipientRecordsByGroupID(context.Context, string) ([]port.EmailRecipientRecord, error) {
	return nil, nil
}
func (f *fakeStore) GroupEngagementDetail(context.Context, string) (*port.GroupEngagementDetail, error) {
	return f.detail, nil
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

func TestNewDispatcher_DefaultFromMustBeAuthenticated(t *testing.T) {
	// A non-empty authenticated-domains list that excludes DefaultFrom's domain
	// must fail at construction, not silently at the first send.
	if _, err := NewDispatcher(Config{
		APIKey:               "k",
		DefaultFrom:          "newsletter@example.com",
		AuthenticatedDomains: []string{"lfx.aaif.io"},
	}); err == nil {
		t.Errorf("expected an error when DefaultFrom domain is not in AuthenticatedDomains")
	}
	// DefaultFrom on an authenticated domain constructs.
	if _, err := NewDispatcher(Config{
		APIKey:               "k",
		DefaultFrom:          "newsletter@lfx.aaif.io",
		AuthenticatedDomains: []string{"lfx.aaif.io"},
	}); err != nil {
		t.Errorf("DefaultFrom on an authenticated domain: unexpected error: %v", err)
	}
	// An empty list permits any domain (dev/test).
	if _, err := NewDispatcher(Config{APIKey: "k", DefaultFrom: "newsletter@example.com"}); err != nil {
		t.Errorf("empty AuthenticatedDomains should permit any DefaultFrom: %v", err)
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

func TestSendEmail_UntrackedWhenNoGroupID(t *testing.T) {
	// A test-send omits GroupID. The dispatcher must send the mail but carry no
	// tracking custom_args, record no engagement, and return an empty handle, so
	// the documented no-persistence / no-analytics contract holds.
	store := &fakeStore{}
	var gotBody mailSendRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
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
	handle, err := d.SendEmail(context.Background(), port.SendEmailInput{
		To: "a@example.com", Subject: "s", Text: "t", // no GroupID
	})
	if err != nil {
		t.Fatalf("SendEmail: %v", err)
	}
	if handle != "" {
		t.Errorf("untracked send handle = %q, want empty", handle)
	}
	if len(store.sent) != 0 {
		t.Errorf("untracked send must record no engagement, got %d RecordSent calls", len(store.sent))
	}
	if len(gotBody.CustomArgs) != 0 {
		t.Errorf("untracked send must carry no custom_args, got %v", gotBody.CustomArgs)
	}
}

func TestReadsDelegateToStore(t *testing.T) {
	store := &fakeStore{
		engagement: &port.EmailEngagement{GroupID: "g", TotalSent: 3, Delivered: 2, Opened: 1},
		byEmailID:  &port.EmailRecipientRecord{EmailID: "e", Delivered: true},
		detail:     &port.GroupEngagementDetail{UniqueOpens: 2, FailedRecipients: []string{"x@y"}},
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
	if detail, err := d.GroupEngagementDetail(context.Background(), "g"); err != nil || detail == nil || detail.UniqueOpens != 2 {
		t.Errorf("GroupEngagementDetail = %+v, %v; want UniqueOpens=2", detail, err)
	}
}

func TestSendEmail_AuthenticatedDomainValidation(t *testing.T) {
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posted = true
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	d, err := NewDispatcher(Config{
		APIKey: "k", DefaultFrom: "newsletter@lfx.aaif.io",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
		AuthenticatedDomains: []string{"lfx.aaif.io"},
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}

	// From on the authenticated domain: allowed.
	if _, err := d.SendEmail(context.Background(), port.SendEmailInput{
		To: "a@x.io", Subject: "s", Text: "t", From: "ed@lfx.aaif.io",
	}); err != nil {
		t.Errorf("authenticated From should be allowed, got: %v", err)
	}
	// From on a subdomain of an authenticated domain: REJECTED. Allowlisting
	// lfx.aaif.io must not implicitly authorize mail.lfx.aaif.io, which may not be
	// domain-authenticated in SendGrid — From matching is exact.
	posted = false
	if _, err := d.SendEmail(context.Background(), port.SendEmailInput{
		To: "a@x.io", Subject: "s", Text: "t", From: "ed@mail.lfx.aaif.io",
	}); err == nil {
		t.Errorf("subdomain From must be rejected (exact-match only)")
	}
	if posted {
		t.Errorf("a subdomain From must not reach SendGrid")
	}
	// From on an unauthenticated domain: rejected before any POST.
	posted = false
	if _, err := d.SendEmail(context.Background(), port.SendEmailInput{
		To: "a@x.io", Subject: "s", Text: "t", From: "ed@evil.example",
	}); err == nil {
		t.Errorf("unauthenticated From must be rejected")
	}
	if posted {
		t.Errorf("an unauthenticated From must not reach SendGrid")
	}
}

func TestSendEmail_EmptyAuthenticatedDomainsPermitsAny(t *testing.T) {
	d := newTestDispatcher(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	// No AuthenticatedDomains configured -> any From is permitted.
	if _, err := d.SendEmail(context.Background(), port.SendEmailInput{
		To: "a@x.io", Subject: "s", Text: "t", From: "ed@anything.example",
	}); err != nil {
		t.Errorf("empty allowlist should permit any From, got: %v", err)
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
	if _, err := d.GroupEngagementDetail(context.Background(), "g"); err == nil {
		t.Errorf("GroupEngagementDetail should report not-wired until the webhook store lands")
	}
}

func TestSendEmail_ReplyToAllowlist(t *testing.T) {
	var gotBody mailSendRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	d, err := NewDispatcher(Config{
		APIKey: "k", DefaultFrom: "newsletter@lfx.aaif.io",
		BaseURL: srv.URL, HTTPClient: srv.Client(),
		ReplyToAllowedDomains: []string{"linuxfoundation.org"},
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}

	// A reply-to outside the allowlist is dropped, not forwarded to SendGrid.
	if _, err := d.SendEmail(context.Background(), port.SendEmailInput{
		To: "a@x.io", Text: "hi", ReplyTo: "someone@evil.example",
	}); err != nil {
		t.Fatalf("SendEmail: %v", err)
	}
	if gotBody.ReplyTo != nil {
		t.Errorf("reply_to = %+v, want nil (dropped, outside allowlist)", gotBody.ReplyTo)
	}

	// An allowed reply-to (including a subdomain) is kept.
	if _, err := d.SendEmail(context.Background(), port.SendEmailInput{
		To: "a@x.io", Text: "hi", ReplyTo: "ed@lists.linuxfoundation.org",
	}); err != nil {
		t.Fatalf("SendEmail: %v", err)
	}
	if gotBody.ReplyTo == nil || gotBody.ReplyTo.Email != "ed@lists.linuxfoundation.org" {
		t.Errorf("reply_to = %+v, want kept", gotBody.ReplyTo)
	}
}

func TestSendEmail_RecordsFailureOnlyOnDefinitiveRejection(t *testing.T) {
	// A definitive rejection (SendGrid did not accept the message, no webhook
	// follows) is persisted; an ambiguous 5xx is not, or a later delivered webhook
	// would contradict it. Definitiveness is decoupled from the outward error
	// type: a client-input 4xx (Validation, detail surfaced), a provider auth
	// 401/403 (redacted ServiceUnavailable), and a rate-limit 429 (no retry path)
	// are all definitive.
	cases := []struct {
		name         string
		status       int
		wantFailed   int
		redactedAuth bool
	}{
		{"definitive 400 records failure and surfaces detail", http.StatusBadRequest, 1, false},
		{"provider 401 records failure, redacted", http.StatusUnauthorized, 1, true},
		{"provider 403 records failure, redacted", http.StatusForbidden, 1, true},
		{"rate-limit 429 records failure (no retry path)", http.StatusTooManyRequests, 1, false},
		{"ambiguous 503 does not record", http.StatusServiceUnavailable, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"errors":[{"message":"upstream-secret-detail"}]}`))
			}))
			t.Cleanup(srv.Close)
			d, err := NewDispatcher(Config{
				APIKey: "k", DefaultFrom: "newsletter@lfx.aaif.io",
				BaseURL: srv.URL, HTTPClient: srv.Client(), Store: store,
			})
			if err != nil {
				t.Fatalf("NewDispatcher: %v", err)
			}
			_, sendErr := d.SendEmail(context.Background(), port.SendEmailInput{To: "a@x.io", Text: "hi", GroupID: "g"})
			if sendErr == nil {
				t.Fatalf("expected an error from a %d send", tc.status)
			}
			if len(store.failed) != tc.wantFailed {
				t.Errorf("failed recorded = %d, want %d", len(store.failed), tc.wantFailed)
			}
			if len(store.sent) != 0 {
				t.Errorf("a failed send must not be recorded as sent, got %d", len(store.sent))
			}
			// A provider auth/config failure must not surface SendGrid's body as if
			// it were the caller's input; a client-input 400 should surface detail.
			if tc.redactedAuth {
				if strings.Contains(sendErr.Error(), "upstream-secret-detail") {
					t.Errorf("provider auth error must not surface the upstream body, got: %v", sendErr)
				}
				if !strings.Contains(sendErr.Error(), "authorization or configuration") {
					t.Errorf("provider auth error should be the redacted class, got: %v", sendErr)
				}
			}
			if tc.status == http.StatusBadRequest && !strings.Contains(sendErr.Error(), "upstream-secret-detail") {
				t.Errorf("a client-input 400 should surface SendGrid detail, got: %v", sendErr)
			}
		})
	}
}

func TestDispatcher_PurgeEngagement(t *testing.T) {
	store := &fakeStore{}
	d, err := NewDispatcher(Config{APIKey: "k", DefaultFrom: "f@lfx.aaif.io", Store: store})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if err := d.PurgeEngagement(context.Background(), "grp-1"); err != nil {
		t.Fatalf("PurgeEngagement: %v", err)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "grp-1" {
		t.Errorf("deleted = %v, want [grp-1]", store.deleted)
	}
	// A store-less dispatcher is a no-op.
	d2, _ := NewDispatcher(Config{APIKey: "k", DefaultFrom: "f@lfx.aaif.io"})
	if err := d2.PurgeEngagement(context.Background(), "x"); err != nil {
		t.Errorf("store-less PurgeEngagement should be a no-op, got %v", err)
	}
}

func TestSendEmail_ScheduledSendAt(t *testing.T) {
	// Test that send_at and batch_id are properly serialized when scheduling a send.
	var gotBody mailSendRequest
	d := newTestDispatcher(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusAccepted)
	})

	sendAtTime := time.Now().UTC().Add(24 * time.Hour)
	emailID, err := d.SendEmail(context.Background(), port.SendEmailInput{
		To:      "alice@example.com",
		Subject: "Scheduled",
		Text:    "Hi",
		GroupID: "grp-456",
		SendAt:  &sendAtTime,
		BatchID: "batch-789",
	})
	if err != nil {
		t.Fatalf("SendEmail with scheduled send_at: %v", err)
	}
	if emailID == "" {
		t.Errorf("expected a non-empty email_id for scheduled send")
	}
	if gotBody.SendAt == nil {
		t.Errorf("send_at should be set in the request")
	}
	if *gotBody.SendAt != sendAtTime.Unix() {
		t.Errorf("send_at = %d, want %d", *gotBody.SendAt, sendAtTime.Unix())
	}
	if gotBody.BatchID != "batch-789" {
		t.Errorf("batch_id = %q, want batch-789", gotBody.BatchID)
	}
}

func TestCreateBatchID_Success(t *testing.T) {
	d := newTestDispatcher(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != mailBatchPath {
			t.Errorf("path = %q, want %q", r.URL.Path, mailBatchPath)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"batch_id":"SG.batch-test-123"}`))
	})

	batchID, err := d.CreateBatchID(context.Background())
	if err != nil {
		t.Fatalf("CreateBatchID: %v", err)
	}
	if batchID != "SG.batch-test-123" {
		t.Errorf("batch_id = %q, want SG.batch-test-123", batchID)
	}
}

func TestCreateBatchID_ErrorHandling(t *testing.T) {
	cases := []struct {
		name            string
		status          int
		responseBody    string
		wantErrContains string
	}{
		{"500 error", http.StatusInternalServerError, `{"errors":[{"message":"server error"}]}`, "returned 500"},
		{"malformed response", http.StatusCreated, `{invalid json}`, "unparseable"},
		{"missing batch_id", http.StatusCreated, `{}`, "unparseable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDispatcher(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.responseBody))
			})

			_, err := d.CreateBatchID(context.Background())
			if err == nil {
				t.Fatalf("expected an error for status %d", tc.status)
			}
			if !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Errorf("error = %v, should contain %q", err, tc.wantErrContains)
			}
		})
	}
}

func TestCancelScheduledBatch_Success(t *testing.T) {
	var gotBody scheduledSendRequest
	d := newTestDispatcher(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		if r.URL.Path != scheduledSendsPath {
			t.Errorf("path = %q, want %q", r.URL.Path, scheduledSendsPath)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		w.WriteHeader(http.StatusCreated)
	})

	err := d.CancelScheduledBatch(context.Background(), "batch-789")
	if err != nil {
		t.Fatalf("CancelScheduledBatch: %v", err)
	}
	if gotBody.BatchID != "batch-789" {
		t.Errorf("batch_id = %q, want batch-789", gotBody.BatchID)
	}
	if gotBody.Status != "cancel" {
		t.Errorf("status = %q, want cancel", gotBody.Status)
	}
}

func TestCancelScheduledBatch_AcceptsAlternateSuccessStatuses(t *testing.T) {
	// SendGrid's documented response for this endpoint is 201, but 200 and 204
	// have also been observed for the same successful cancellation.
	for _, status := range []int{http.StatusOK, http.StatusNoContent} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			d := newTestDispatcher(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			})
			if err := d.CancelScheduledBatch(context.Background(), "batch-789"); err != nil {
				t.Fatalf("CancelScheduledBatch with status %d: %v", status, err)
			}
		})
	}
}

func TestCancelScheduledBatch_Idempotent(t *testing.T) {
	// Re-cancelling an already-cancelled batch is treated as success
	// (the SendGrid API returns a BadRequest with "already exists in the cancel").
	d := newTestDispatcher(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"message":"Batch already exists in the cancel queue"}]}`))
	})

	err := d.CancelScheduledBatch(context.Background(), "batch-789")
	if err != nil {
		t.Fatalf("re-cancel of already-cancelled batch should be idempotent, got: %v", err)
	}
}

func TestCancelScheduledBatch_ErrorHandling(t *testing.T) {
	cases := []struct {
		name            string
		status          int
		responseBody    string
		wantErrContains string
	}{
		{"500 error", http.StatusInternalServerError, `{"errors":[{"message":"server error"}]}`, "returned 500"},
		{"bad request (not already cancelled)", http.StatusBadRequest, `{"errors":[{"message":"Batch not found"}]}`, "Batch not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDispatcher(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.responseBody))
			})

			err := d.CancelScheduledBatch(context.Background(), "batch-789")
			if err == nil {
				t.Fatalf("expected an error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Errorf("error = %v, should contain %q", err, tc.wantErrContains)
			}
		})
	}
}
