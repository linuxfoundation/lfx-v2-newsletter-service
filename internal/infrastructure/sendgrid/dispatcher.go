// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package sendgrid implements port.EmailDispatcher against SendGrid's v3
// mail/send API. It is a parallel provider to the NATS/email-service (SES)
// dispatcher: per the 07-02 architecture decision the newsletter service
// brokers SendGrid directly for newsletter/marketing sends, while
// lfx-v2-email-service stays SES for transactional email (LFXV2-2388).
//
// SendEmail and SendBatch dispatch mail. Engagement reads (GetEngagement,
// GetStatusByEmailID, GetStatusByGroupID) are served from a newsletter-service
// store populated by the SendGrid event webhook (see webhook.go and the
// repository store). A dispatcher configured without a Store is send-only, and
// those read methods return errReadNotWired.
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

// errReadNotWired is returned by the engagement-read methods when the dispatcher
// was constructed without a Store (send-only mode). Engagement for SendGrid
// sends is populated by the event webhook into newsletter-service's own store;
// analytics reads it through a store-backed reader, not through this dispatcher.
var errReadNotWired = errors.New("sendgrid: engagement reads require a configured Store")

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
	// AuthenticatedDomains lists the domains authenticated for this dispatcher's
	// subuser. A send whose From domain is not one of these (or a subdomain of
	// one) is rejected before hitting SendGrid, since an unauthenticated From
	// wrecks DKIM/SPF/DMARC alignment. Empty permits any From (dev/test default).
	AuthenticatedDomains []string
	// ReplyToAllowedDomains lists the domains a Reply-To may use. Since this
	// provider bypasses email-service, it must enforce the same Reply-To policy
	// email-service does: a Reply-To outside these domains (or a subdomain) is
	// dropped from the send (reply then defaults to From) rather than forwarded.
	// Empty permits any Reply-To (dev/test default).
	ReplyToAllowedDomains []string
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

	authenticatedDomains  []string
	replyToAllowedDomains []string
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
		apiKey:                cfg.APIKey,
		defaultFrom:           cfg.DefaultFrom,
		defaultFromName:       cfg.DefaultFromName,
		baseURL:               baseURL,
		httpClient:            httpClient,
		sandboxMode:           cfg.SandboxMode,
		store:                 cfg.Store,
		authenticatedDomains:  normalizeDomains(cfg.AuthenticatedDomains),
		replyToAllowedDomains: normalizeDomains(cfg.ReplyToAllowedDomains),
	}, nil
}

// normalizeDomains lowercases and trims a domain allowlist, dropping blanks.
func normalizeDomains(domains []string) []string {
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// domainOf returns the lowercased domain part of an email address, or "".
func domainOf(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(email[at+1:]))
}

// domainAllowed reports whether email's domain equals a listed domain or is a
// subdomain of one. An empty list permits any address (dev/test), matching
// email-service's permissive default.
func domainAllowed(email string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	dom := domainOf(email)
	if dom == "" {
		return false
	}
	for _, a := range allowed {
		if dom == a || strings.HasSuffix(dom, "."+a) {
			return true
		}
	}
	return false
}

// fromDomainAllowed reports whether a From address may be used: its domain must
// be in the authenticated-domain allowlist (an unauthenticated From wrecks
// DKIM/SPF/DMARC alignment).
func (d *Dispatcher) fromDomainAllowed(email string) bool {
	return domainAllowed(email, d.authenticatedDomains)
}

// allowedReplyTo returns the Reply-To address to set on a send, or nil to omit
// it. A Reply-To whose domain is outside the allowlist is dropped (with a
// warning) rather than forwarded, mirroring the policy email-service enforces on
// the SES path — this provider bypasses email-service, so it must enforce it
// here. A blank Reply-To is simply omitted.
func (d *Dispatcher) allowedReplyTo(ctx context.Context, replyTo string) *address {
	replyTo = strings.TrimSpace(replyTo)
	if replyTo == "" {
		return nil
	}
	if !domainAllowed(replyTo, d.replyToAllowedDomains) {
		slog.WarnContext(ctx, "sendgrid: dropping reply_to outside the allowed domains",
			"reply_to_domain", domainOf(replyTo))
		return nil
	}
	return &address{Email: replyTo}
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
	if !d.fromDomainAllowed(from.Email) {
		return "", pkgerrors.NewValidation(fmt.Sprintf("sendgrid: From domain %q is not an authenticated sending domain", domainOf(from.Email)))
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
	reqBody.ReplyTo = d.allowedReplyTo(ctx, in.ReplyTo)
	if d.sandboxMode {
		reqBody.MailSettings = &mailSettings{SandboxMode: &toggle{Enable: true}}
	}

	if err := d.postMailSend(ctx, reqBody); err != nil {
		// Only persist a failure on a definitive rejection (4xx → Validation):
		// SendGrid did not accept the message, so no webhook will arrive, and
		// analytics would otherwise omit this recipient when a sibling succeeds
		// and the newsletter is marked sent. An ambiguous transport/5xx error is
		// NOT recorded — SendGrid may have accepted it, and recording failed would
		// contradict a later delivered webhook (and could double-count a retry).
		var rejected pkgerrors.Validation
		if errors.As(err, &rejected) {
			d.recordFailed(ctx, emailID, groupID, in.To)
		}
		return "", err
	}
	d.recordSent(ctx, emailID, groupID, in.To)
	return emailID, nil
}

// postMailSend marshals and POSTs a mail/send request, returning nil on the 202
// Accepted success and a classified error otherwise. Shared by the single-send
// SendEmail and the batched SendBatch.
func (d *Dispatcher) postMailSend(ctx context.Context, reqBody mailSendRequest) error {
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return pkgerrors.NewUnexpected("sendgrid: marshal mail/send request", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+mailSendPath, bytes.NewReader(payload))
	if err != nil {
		return pkgerrors.NewUnexpected("sendgrid: build mail/send request", err)
	}
	req.Header.Set("Authorization", "Bearer "+d.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return pkgerrors.NewServiceUnavailable("sendgrid: mail/send request failed", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// A successful real send is 202 Accepted; a sandbox-mode validation returns
	// 200 OK. Both have an empty body and mean SendGrid accepted the request.
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
		return nil
	}
	// SendGrid reports failures as { "errors": [ { "message", "field", "help" } ] }.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	// Distinguish a definitive rejection from an ambiguous outcome. A 4xx means
	// SendGrid did not accept the message (bad request, auth, rate limit), so no
	// delivery webhook will follow — callers may safely record the recipient as
	// failed. A 5xx (or the transport error above) is ambiguous: SendGrid may
	// have accepted it and failed to respond, so the caller must NOT record a
	// failure that a later delivered/bounce webhook would then contradict.
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return pkgerrors.NewValidation(fmt.Sprintf("sendgrid: mail/send rejected (%d): %s", resp.StatusCode, summarizeError(resp.StatusCode, body)))
	}
	return pkgerrors.NewServiceUnavailable(
		fmt.Sprintf("sendgrid: mail/send returned %d", resp.StatusCode),
		errors.New(summarizeError(resp.StatusCode, body)),
	)
}

// recordSent persists the initial engagement row for an accepted send. It is
// best-effort: the email is already sent, so a store failure degrades tracking
// but must not fail the send. A no-op when no store is wired (send-only mode).
// recordSent pre-creates the engagement row after an accepted send so TotalSent
// is accurate before any event arrives. It is best-effort on purpose: the send
// already succeeded, so failing here must not fail the send (that would risk a
// duplicate on retry). A failed record is recoverable — the store's Apply*
// methods upsert on email_id, so the first delivered/open/failed webhook event
// re-creates the row from the event's group_id / recipient.
func (d *Dispatcher) recordSent(ctx context.Context, emailID, groupID, to string) {
	if d.store == nil {
		return
	}
	if err := d.store.RecordSent(ctx, emailID, groupID, to, time.Now().UTC()); err != nil {
		slog.WarnContext(ctx, "sendgrid: failed to record send for engagement tracking (recoverable via the event webhook)",
			"email_id", emailID, "error", err)
	}
}

// PurgeEngagement deletes the engagement rows this dispatcher persisted for a
// group_id. The orchestrator calls it (via port.EngagementPurger) after a fully
// failed send reverts to draft and clears its group_id, so the recorded failures
// — which hold recipient emails — are not left orphaned under a dead group.
func (d *Dispatcher) PurgeEngagement(ctx context.Context, groupID string) error {
	if d.store == nil {
		return nil
	}
	return d.store.DeleteByGroupID(ctx, groupID)
}

// recordFailed persists a synchronous send failure as a failed engagement row
// (upsert on email_id, so it also creates the row) so analytics counts the
// recipient. Best-effort like recordSent: a store error is logged, not returned,
// since the send already failed and the caller returns that error.
func (d *Dispatcher) recordFailed(ctx context.Context, emailID, groupID, to string) {
	if d.store == nil {
		return
	}
	if err := d.store.ApplyFailed(ctx, emailID, groupID, to, time.Now().UTC()); err != nil {
		slog.WarnContext(ctx, "sendgrid: failed to record a synchronous send failure for engagement tracking",
			"email_id", emailID, "error", err)
	}
}

// recordSentBatch persists a whole accepted chunk's engagement rows in one
// statement (SendBatch's per-recipient equivalent of recordSent). Best-effort
// for the same reason: a failure is recoverable via the event webhook.
func (d *Dispatcher) recordSentBatch(ctx context.Context, rows []port.SentRow) {
	if d.store == nil || len(rows) == 0 {
		return
	}
	if err := d.store.RecordSentBatch(ctx, rows); err != nil {
		slog.WarnContext(ctx, "sendgrid: failed to bulk-record sends for engagement tracking (recoverable via the event webhook)",
			"count", len(rows), "error", err)
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
	BatchID          string            `json:"batch_id,omitempty"`
	MailSettings     *mailSettings     `json:"mail_settings,omitempty"`
}

type personalization struct {
	To []address `json:"to"`
	// SendAt is a Unix timestamp (<=72h out) for a scheduled per-recipient send;
	// 0 (omitted) sends immediately. CustomArgs carry the per-recipient email_id
	// + group_id in a batch; Substitutions fill merge tokens in subject/content.
	SendAt        int64             `json:"send_at,omitempty"`
	CustomArgs    map[string]string `json:"custom_args,omitempty"`
	Substitutions map[string]string `json:"substitutions,omitempty"`
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
