// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package sendgrid implements port.EmailDispatcher against SendGrid's v3
// mail/send API. It is a parallel provider to the NATS/email-service (SES)
// dispatcher: per the 07-02 architecture decision the newsletter service
// brokers SendGrid directly for newsletter/marketing sends, while
// lfx-v2-email-service stays SES for transactional email (LFXV2-2388).
//
// This slice implements SendEmail only. Engagement reads (GetEngagement,
// GetStatusByEmailID, GetStatusByGroupID) are served from a newsletter-service
// store populated by the SendGrid event webhook, which lands in a follow-up
// slice; until then those methods return errReadNotWired and this provider must
// not be wired as the active dispatcher for analytics reads.
package sendgrid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

const (
	// defaultBaseURL is the SendGrid v3 API origin. Overridable via Config.BaseURL
	// (tests point it at an httptest server).
	defaultBaseURL = "https://api.sendgrid.com"

	// mailSendPath is the v3 send endpoint. A successful send returns 202 with an
	// empty body; the per-recipient tracking handle is our own email_id carried in
	// custom_args, which the event webhook echoes back for back-mapping.
	mailSendPath = "/v3/mail/send"

	// defaultTimeout bounds a single mail/send call. SendGrid accepts a send in
	// well under a second; this only guards against a hung connection.
	defaultTimeout = 30 * time.Second

	customArgEmailID = "email_id"
	customArgGroupID = "group_id"
)

// errReadNotWired marks the engagement-read methods as not yet implemented on
// the SendGrid provider. Engagement for SendGrid sends is populated by the
// event webhook into newsletter-service's own store (LFXV2-2388, follow-up
// slice); until that lands the SendGrid provider only implements SendEmail.
var errReadNotWired = errors.New("sendgrid: engagement reads require the event-webhook store (LFXV2-2388, not yet wired)")

// Config wires a Dispatcher.
type Config struct {
	// APIKey is the SendGrid API key (a subuser-scoped key in production). Required.
	APIKey string
	// DefaultFrom is the sender address used when a SendEmailInput carries no From.
	// SendGrid requires a from address on every send, so this must be set to a
	// verified/authenticated sender. Required.
	DefaultFrom string
	// DefaultFromName is the display name used when a SendEmailInput carries no
	// FromDisplayName. Optional.
	DefaultFromName string
	// BaseURL overrides the SendGrid API origin (defaults to defaultBaseURL).
	BaseURL string
	// HTTPClient overrides the HTTP client (defaults to one with defaultTimeout).
	HTTPClient *http.Client
	// SandboxMode, when true, sets mail_settings.sandbox_mode so SendGrid
	// validates the request without delivering. Useful against a test account.
	SandboxMode bool
	// Store persists per-recipient engagement. When nil the dispatcher is
	// send-only: it records nothing and the engagement reads return
	// errReadNotWired. When set, SendEmail records the send and the reads are
	// served from it (populated by the event webhook).
	Store EngagementStore
}

// Dispatcher implements port.EmailDispatcher over the SendGrid v3 mail/send API.
type Dispatcher struct {
	apiKey          string
	defaultFrom     string
	defaultFromName string
	baseURL         string
	httpClient      *http.Client
	sandboxMode     bool
	store           EngagementStore
}

// Compile-time assertion that Dispatcher satisfies the port.
var _ port.EmailDispatcher = (*Dispatcher)(nil)

// NewDispatcher wires a SendGrid Dispatcher. APIKey and DefaultFrom are required.
func NewDispatcher(cfg Config) (*Dispatcher, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, pkgerrors.NewValidation("sendgrid: APIKey is required")
	}
	if strings.TrimSpace(cfg.DefaultFrom) == "" {
		return nil, pkgerrors.NewValidation("sendgrid: DefaultFrom is required")
	}
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &Dispatcher{
		apiKey:          cfg.APIKey,
		defaultFrom:     cfg.DefaultFrom,
		defaultFromName: cfg.DefaultFromName,
		baseURL:         baseURL,
		httpClient:      httpClient,
		sandboxMode:     cfg.SandboxMode,
		store:           cfg.Store,
	}, nil
}

// SendEmail delivers one email via SendGrid mail/send. It mints an email_id and
// carries it plus the group_id in custom_args so the event webhook can map
// delivery/open/bounce events back to this send. The minted email_id is
// returned as the tracking handle.
func (d *Dispatcher) SendEmail(ctx context.Context, in port.SendEmailInput) (string, error) {
	if strings.TrimSpace(in.To) == "" {
		return "", pkgerrors.NewValidation("sendgrid: recipient (To) is required")
	}
	if strings.TrimSpace(in.HTML) == "" && strings.TrimSpace(in.Text) == "" {
		return "", pkgerrors.NewValidation("sendgrid: at least one of HTML or Text body is required")
	}

	emailID := uuid.NewString()
	groupID := in.GroupID
	if strings.TrimSpace(groupID) == "" {
		// Mirror email-service: mint a group_id when the caller omits one so
		// analytics can always aggregate by group.
		groupID = uuid.NewString()
	}

	from := address{Email: in.From, Name: in.FromDisplayName}
	if strings.TrimSpace(from.Email) == "" {
		from = address{Email: d.defaultFrom, Name: d.defaultFromName}
	}

	// SendGrid orders content text/plain before text/html. Include only the
	// non-empty parts; at least one is guaranteed by the guard above.
	var contents []content
	if strings.TrimSpace(in.Text) != "" {
		contents = append(contents, content{Type: "text/plain", Value: in.Text})
	}
	if strings.TrimSpace(in.HTML) != "" {
		contents = append(contents, content{Type: "text/html", Value: in.HTML})
	}

	reqBody := mailSendRequest{
		Personalizations: []personalization{{To: []address{{Email: in.To}}}},
		From:             from,
		Subject:          in.Subject,
		Content:          contents,
		CustomArgs: map[string]string{
			customArgEmailID: emailID,
			customArgGroupID: groupID,
		},
	}
	if strings.TrimSpace(in.ReplyTo) != "" {
		reqBody.ReplyTo = &address{Email: in.ReplyTo}
	}
	if d.sandboxMode {
		reqBody.MailSettings = &mailSettings{SandboxMode: &toggle{Enable: true}}
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", pkgerrors.NewUnexpected("sendgrid: marshal mail/send request", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+mailSendPath, bytes.NewReader(payload))
	if err != nil {
		return "", pkgerrors.NewUnexpected("sendgrid: build mail/send request", err)
	}
	req.Header.Set("Authorization", "Bearer "+d.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", pkgerrors.NewServiceUnavailable("sendgrid: mail/send request failed", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// A successful send is 202 Accepted with an empty body.
	if resp.StatusCode == http.StatusAccepted {
		d.recordSent(ctx, emailID, groupID, in.To)
		return emailID, nil
	}

	// SendGrid reports failures as { "errors": [ { "message", "field", "help" } ] }.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return "", pkgerrors.NewServiceUnavailable(
		fmt.Sprintf("sendgrid: mail/send returned %d", resp.StatusCode),
		errors.New(summarizeError(resp.StatusCode, body)),
	)
}

// recordSent persists the initial engagement row for an accepted send. It is
// best-effort: the email is already sent, so a store failure degrades tracking
// but must not fail the send. A no-op when no store is wired (send-only mode).
func (d *Dispatcher) recordSent(ctx context.Context, emailID, groupID, to string) {
	if d.store == nil {
		return
	}
	if err := d.store.RecordSent(ctx, emailID, groupID, to, time.Now().UTC()); err != nil {
		slog.WarnContext(ctx, "sendgrid: failed to record send for engagement tracking",
			"email_id", emailID, "error", err)
	}
}

// GetEngagement returns the per-group engagement rollup from the store. Returns
// errReadNotWired when the dispatcher is send-only (no store).
func (d *Dispatcher) GetEngagement(ctx context.Context, groupID string) (*port.EmailEngagement, error) {
	if d.store == nil {
		return nil, pkgerrors.NewUnexpected("sendgrid: GetEngagement unavailable", errReadNotWired)
	}
	return d.store.Engagement(ctx, groupID)
}

// GetStatusByEmailID returns one recipient's engagement record from the store.
func (d *Dispatcher) GetStatusByEmailID(ctx context.Context, emailID string) (*port.EmailRecipientRecord, error) {
	if d.store == nil {
		return nil, pkgerrors.NewUnexpected("sendgrid: GetStatusByEmailID unavailable", errReadNotWired)
	}
	return d.store.RecipientByEmailID(ctx, emailID)
}

// GetStatusByGroupID returns every recipient record for a group from the store.
func (d *Dispatcher) GetStatusByGroupID(ctx context.Context, groupID string) ([]port.EmailRecipientRecord, error) {
	if d.store == nil {
		return nil, pkgerrors.NewUnexpected("sendgrid: GetStatusByGroupID unavailable", errReadNotWired)
	}
	return d.store.RecipientsByGroupID(ctx, groupID)
}

// summarizeError renders SendGrid's error body into a single readable string,
// falling back to the raw (truncated) body when it isn't the expected shape.
func summarizeError(status int, body []byte) string {
	var parsed struct {
		Errors []struct {
			Message string `json:"message"`
			Field   string `json:"field"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && len(parsed.Errors) > 0 {
		msgs := make([]string, 0, len(parsed.Errors))
		for _, e := range parsed.Errors {
			if e.Field != "" {
				msgs = append(msgs, fmt.Sprintf("%s (%s)", e.Message, e.Field))
			} else {
				msgs = append(msgs, e.Message)
			}
		}
		return strings.Join(msgs, "; ")
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return fmt.Sprintf("status %d with empty body", status)
	}
	return trimmed
}

// ---- SendGrid v3 mail/send wire types --------------------------------------

type mailSendRequest struct {
	Personalizations []personalization `json:"personalizations"`
	From             address           `json:"from"`
	ReplyTo          *address          `json:"reply_to,omitempty"`
	Subject          string            `json:"subject"`
	Content          []content         `json:"content"`
	CustomArgs       map[string]string `json:"custom_args,omitempty"`
	MailSettings     *mailSettings     `json:"mail_settings,omitempty"`
}

type personalization struct {
	To []address `json:"to"`
}

type address struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type content struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type mailSettings struct {
	SandboxMode *toggle `json:"sandbox_mode,omitempty"`
}

type toggle struct {
	Enable bool `json:"enable"`
}
