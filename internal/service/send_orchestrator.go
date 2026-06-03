// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service/render"
)

// defaultSendConcurrency caps in-flight email-service requests during fan-out.
const defaultSendConcurrency = 5

// defaultFromAddress is the SMTP envelope From used when the orchestrator is
// constructed without an explicit FromAddress (e.g. tests). Production wiring
// always sets one through SendOrchestratorConfig.
const defaultFromAddress = "newsletter@lfx.linuxfoundation.org"

// fromDisplayNameSuffix is appended to the project name to build the From
// display name, yielding e.g. "Kubernetes Newsletter".
const fromDisplayNameSuffix = " Newsletter"

// SendOrchestrator coordinates recipient resolution, email-chrome rendering,
// per-recipient fan-out to lfx-v2-email-service, and the draft → sent state
// transition. It owns the email-service integration; the UI no longer talks
// to email-service directly.
type SendOrchestrator struct {
	repo          port.NewsletterRepository
	committee     port.CommitteeClient
	project       port.ProjectMetadataClient
	email         port.EmailDispatcher
	userMetadata  port.UserMetadataReader
	unsub         *UnsubscribeService
	concurrency   int
	fanoutEnabled bool
	fromAddress   string
}

// SendOrchestratorConfig configures a SendOrchestrator.
type SendOrchestratorConfig struct {
	Repo         port.NewsletterRepository
	Committee    port.CommitteeClient
	Project      port.ProjectMetadataClient
	Email        port.EmailDispatcher
	UserMetadata port.UserMetadataReader
	Unsubscribe  *UnsubscribeService
	Concurrency  int
	// FanoutEnabled is the feature toggle for the per-recipient send loop.
	// Defaults to true; flip false in environments where we want to validate
	// the recipient-resolution path without sending real mail.
	FanoutEnabled bool
	// FromAddress is the SMTP envelope From applied to every outbound email.
	// Empty falls back to defaultFromAddress.
	FromAddress string
}

// NewSendOrchestrator wires a SendOrchestrator.
func NewSendOrchestrator(cfg SendOrchestratorConfig) *SendOrchestrator {
	c := cfg.Concurrency
	if c <= 0 {
		c = defaultSendConcurrency
	}
	from := strings.TrimSpace(cfg.FromAddress)
	if from == "" {
		from = defaultFromAddress
	}
	return &SendOrchestrator{
		repo:          cfg.Repo,
		committee:     cfg.Committee,
		project:       cfg.Project,
		email:         cfg.Email,
		userMetadata:  cfg.UserMetadata,
		unsub:         cfg.Unsubscribe,
		concurrency:   c,
		fanoutEnabled: cfg.FanoutEnabled,
		fromAddress:   from,
	}
}

// SendNewsletterInput is the typed input for SendNewsletter.
//
// Principal is the validated JWT `sub`/`principal` claim of the user triggering
// the send (read by the handler from request context). The orchestrator uses it
// to look up the sender's display name from auth-service. Empty principal is
// only valid in dev mode (auth disabled) and falls back to project-name chrome.
type SendNewsletterInput struct {
	ProjectUID      string
	NewsletterID    uuid.UUID
	ExpectedVersion int64
	Principal       string
}

// SendFailure describes a single per-recipient failure surfaced from the
// fan-out loop.
type SendFailure struct {
	Email string
	Error string
}

// SendResult is the typed result returned by SendNewsletter.
type SendResult struct {
	Newsletter      *model.Newsletter
	GroupID         string
	TotalRecipients int
	Sent            int
	Failed          int
	Failures        []SendFailure
}

// SendNewsletter resolves the draft, mints group_id, renders the email envelope,
// fans out per-recipient sends to email-service, and transitions the draft to
// status=sent.
func (o *SendOrchestrator) SendNewsletter(ctx context.Context, in SendNewsletterInput) (*SendResult, error) {
	if err := validateProjectUID(in.ProjectUID); err != nil {
		return nil, err
	}

	draft, err := o.repo.Get(ctx, in.NewsletterID)
	if err != nil {
		return nil, err
	}
	if draft.ProjectUID != in.ProjectUID {
		return nil, domain.ErrNotFound
	}
	if draft.Status == model.StatusSent {
		return nil, domain.ErrAlreadySent
	}
	if in.ExpectedVersion != 0 && draft.Version != in.ExpectedVersion {
		return nil, domain.ErrVersionMismatch
	}

	recipients, err := o.resolveRecipients(ctx, draft.ProjectUID, draft.CommitteeUIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve recipients: %w", err)
	}

	projectName, _ := o.project.Name(ctx, draft.ProjectUID)
	if projectName == "" {
		projectName = "Project"
	}
	senderName, err := o.resolveSenderName(ctx, in.Principal)
	if err != nil {
		return nil, err
	}
	fromDisplayName := sanitizeFromDisplayName(senderName)
	if fromDisplayName == "" {
		fromDisplayName = projectName + fromDisplayNameSuffix
	}

	chrome := render.Chrome{
		Subject:                 draft.Subject,
		BodyHTML:                draft.BodyHTML,
		DisplayName:             projectName,
		IncludeComplianceFooter: true,
		EDName:                  fallbackString(senderName, "Executive Director"),
		EDReplyEmail:            draft.EDReplyEmail,
	}
	if o.unsub.Enabled() {
		chrome.UnsubscribeURL = UnsubscribeURLPlaceholder
	}
	htmlBody := render.EmailHTML(chrome)
	textBody := render.EmailText(chrome)

	groupID := uuid.NewString()

	envelope := emailEnvelope{
		Subject:         draft.Subject,
		HTML:            htmlBody,
		Text:            textBody,
		From:            o.fromAddress,
		FromDisplayName: fromDisplayName,
		ReplyTo:         draft.EDReplyEmail,
		GroupID:         groupID,
	}
	sent, failed, failures := o.fanOut(ctx, draft.ProjectUID, recipients, envelope)

	// Only flip the draft to `sent` when at least one recipient was delivered
	// to. If every send failed (email-service unreachable, all recipients
	// rejected, etc.) the row stays a draft so the operator can retry without
	// emails ever having gone out. Without this gate, a fully-failed send is
	// permanently indistinguishable from a successful one — no retry path.
	if sent == 0 && len(recipients) > 0 {
		slog.WarnContext(ctx, "newsletter send failed: no recipients delivered, leaving as draft",
			"newsletter_id", draft.ID,
			"project_uid", draft.ProjectUID,
			"group_id", groupID,
			"total_recipients", len(recipients),
			"failed", failed,
		)
		return nil, fmt.Errorf("send failed: 0 of %d recipients delivered", len(recipients))
	}

	updated, markErr := o.repo.MarkSent(ctx, draft.ID, time.Now().UTC(), len(recipients), groupID, draft.Version)
	if markErr != nil {
		return nil, fmt.Errorf("mark sent: %w", markErr)
	}

	slog.InfoContext(ctx, "newsletter sent",
		"newsletter_id", draft.ID,
		"project_uid", draft.ProjectUID,
		"group_id", groupID,
		"total_recipients", len(recipients),
		"sent", sent,
		"failed", failed,
	)

	return &SendResult{
		Newsletter:      updated,
		GroupID:         groupID,
		TotalRecipients: len(recipients),
		Sent:            sent,
		Failed:          failed,
		Failures:        failures,
	}, nil
}

// TestSendInput is the typed input for TestSend.
//
// Principal is the validated JWT principal of the user triggering the test
// send. Same semantics as SendNewsletterInput.Principal — see its doc comment.
type TestSendInput struct {
	ProjectUID   string
	Subject      string
	BodyHTML     string
	ToEmail      string
	EDReplyEmail string
	Principal    string
}

// TestSend dispatches a single test email — no persistence, no analytics, no
// compliance footer.
func (o *SendOrchestrator) TestSend(ctx context.Context, in TestSendInput) error {
	if err := validateProjectUID(in.ProjectUID); err != nil {
		return err
	}
	if err := validateSubject(in.Subject); err != nil {
		return err
	}
	if err := validateBodyHTML(in.BodyHTML); err != nil {
		return err
	}
	if strings.TrimSpace(in.EDReplyEmail) != "" {
		if err := validateEDReplyEmail(in.EDReplyEmail); err != nil {
			return err
		}
	}
	if _, err := mail.ParseAddress(strings.TrimSpace(in.ToEmail)); err != nil {
		return fmt.Errorf("%w: to_email is not a valid email: %v", domain.ErrInvalidRequest, err)
	}

	projectName, _ := o.project.Name(ctx, in.ProjectUID)
	if projectName == "" {
		projectName = "Project"
	}
	senderName, err := o.resolveSenderName(ctx, in.Principal)
	if err != nil {
		return err
	}
	fromDisplayName := sanitizeFromDisplayName(senderName)
	if fromDisplayName == "" {
		fromDisplayName = projectName + fromDisplayNameSuffix
	}

	chrome := render.Chrome{
		Subject:                 in.Subject,
		BodyHTML:                in.BodyHTML,
		DisplayName:             projectName,
		IncludeComplianceFooter: false,
	}
	htmlBody := render.EmailHTML(chrome)
	textBody := render.EmailText(chrome)

	if !o.fanoutEnabled {
		slog.InfoContext(ctx, "test-send: fanout disabled, accepted without dispatch",
			"to_email", in.ToEmail,
			"project_uid", in.ProjectUID,
		)
		return nil
	}
	if _, dispatchErr := o.email.SendEmail(ctx, port.SendEmailInput{
		To:              strings.TrimSpace(in.ToEmail),
		Subject:         in.Subject,
		HTML:            htmlBody,
		Text:            textBody,
		From:            o.fromAddress,
		FromDisplayName: fromDisplayName,
		ReplyTo:         strings.TrimSpace(in.EDReplyEmail),
	}); dispatchErr != nil {
		return fmt.Errorf("dispatch test-send: %w", dispatchErr)
	}
	slog.InfoContext(ctx, "test-send dispatched",
		"to_email", in.ToEmail,
		"project_uid", in.ProjectUID,
	)
	return nil
}

// RecipientCount resolves recipients and returns the unique count.
func (o *SendOrchestrator) RecipientCount(ctx context.Context, projectUID string, committeeUIDs []string) (int, error) {
	if err := validateProjectUID(projectUID); err != nil {
		return 0, err
	}
	if err := validateCommitteeUIDs(committeeUIDs); err != nil {
		return 0, err
	}
	recipients, err := o.resolveRecipients(ctx, projectUID, committeeUIDs)
	if err != nil {
		return 0, err
	}
	return len(recipients), nil
}

// Recipients resolves recipients and returns the unique list.
func (o *SendOrchestrator) Recipients(ctx context.Context, projectUID string, committeeUIDs []string) ([]model.CommitteeMember, error) {
	if err := validateProjectUID(projectUID); err != nil {
		return nil, err
	}
	if err := validateCommitteeUIDs(committeeUIDs); err != nil {
		return nil, err
	}
	return o.resolveRecipients(ctx, projectUID, committeeUIDs)
}

// resolveRecipients fans out to the committee client across committees, dedupes
// by lowercased email, filters obviously bad addresses, and drops any address
// that has unsubscribed from this project. The errgroup cancels in-flight
// goroutines as soon as one returns an error so a transient failure from one
// committee doesn't keep the remaining lookups running.
func (o *SendOrchestrator) resolveRecipients(ctx context.Context, projectUID string, committeeUIDs []string) ([]model.CommitteeMember, error) {
	results := make([][]model.CommitteeMember, len(committeeUIDs))

	g, gctx := errgroup.WithContext(ctx)
	for i, uid := range committeeUIDs {
		idx, committeeUID := i, uid
		g.Go(func() error {
			members, err := o.committee.ListMembers(gctx, committeeUID)
			if err != nil {
				return err
			}
			results[idx] = members
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	excluded, err := o.listUnsubscribed(ctx, projectUID)
	if err != nil {
		return nil, fmt.Errorf("list unsubscribes: %w", err)
	}

	seen := make(map[string]struct{})
	out := make([]model.CommitteeMember, 0)
	skipped := 0
	for _, members := range results {
		for _, m := range members {
			email := strings.ToLower(strings.TrimSpace(m.Email))
			if email == "" || !strings.Contains(email, "@") {
				continue
			}
			if _, ok := seen[email]; ok {
				continue
			}
			seen[email] = struct{}{}
			if _, unsub := excluded[email]; unsub {
				skipped++
				continue
			}
			out = append(out, model.CommitteeMember{
				Email:     email,
				FirstName: strings.TrimSpace(m.FirstName),
			})
		}
	}
	if skipped > 0 {
		slog.InfoContext(ctx, "resolve recipients: excluded unsubscribed addresses",
			"project_uid", projectUID,
			"excluded", skipped,
			"remaining", len(out),
		)
	}
	return out, nil
}

// listUnsubscribed returns the set of unsubscribed emails for the project, or
// an empty set when no unsubscribe service is wired (tests, legacy config).
func (o *SendOrchestrator) listUnsubscribed(ctx context.Context, projectUID string) (map[string]struct{}, error) {
	if o.unsub == nil || o.unsub.repo == nil {
		return map[string]struct{}{}, nil
	}
	return o.unsub.repo.ListUnsubscribedEmails(ctx, projectUID)
}

// emailEnvelope bundles the per-send fields shared across every recipient in a
// fan-out. Bundling them keeps fanOut's signature small as we add personalization
// fields (from, from_display_name, reply_to) to the per-recipient send.
type emailEnvelope struct {
	Subject         string
	HTML            string
	Text            string
	From            string
	FromDisplayName string
	ReplyTo         string
	GroupID         string
}

// fanOut dispatches per-recipient send_email requests to email-service with
// bounded concurrency. The fan-out never returns an error — per-recipient
// failures are captured and surfaced in the result so the caller can decide
// how to react. A nil EmailDispatcher (or FanoutEnabled=false) short-circuits
// to "all sent, none failed" for dev/test environments.
func (o *SendOrchestrator) fanOut(ctx context.Context, projectUID string, recipients []model.CommitteeMember, env emailEnvelope) (sent, failed int, failures []SendFailure) {
	if len(recipients) == 0 {
		return 0, 0, nil
	}
	if !o.fanoutEnabled {
		slog.InfoContext(ctx, "send fanout disabled, marking all as sent without dispatch",
			"total_recipients", len(recipients),
			"group_id", env.GroupID,
		)
		return len(recipients), 0, nil
	}

	sem := make(chan struct{}, o.concurrency)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, r := range recipients {
		recipient := r
		// Respect ctx cancellation when acquiring a worker slot. A naked
		// `sem <- struct{}{}` would block forever (or until a slot frees) even
		// after the caller cancelled — and then spin up a goroutine per
		// remaining recipient that immediately fails into `failures` with the
		// cancelled context. Selecting on ctx.Done() lets us bail early.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			mu.Lock()
			failed++
			failures = append(failures, SendFailure{Email: recipient.Email, Error: ctx.Err().Error()})
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			recipientHTML, recipientText := env.HTML, env.Text
			if o.unsub.Enabled() {
				url := o.unsub.BuildURL(projectUID, recipient.Email)
				// Substitution runs over the full rendered body, including the
				// author-supplied draft. The collision risk is low (a literal
				// UnsubscribeURLPlaceholder in the draft would just be replaced
				// with a working per-recipient link), but worth noting if we
				// ever expose %%-delimited placeholders to authors.
				recipientHTML = strings.ReplaceAll(env.HTML, UnsubscribeURLPlaceholder, url)
				recipientText = strings.ReplaceAll(env.Text, UnsubscribeURLPlaceholder, url)
			}
			// Honour the nil-dispatcher contract documented above. Production
			// wiring always provides one, but tests and misconfigured local
			// envs should never panic on an unauthenticated send path.
			if o.email == nil {
				mu.Lock()
				sent++
				mu.Unlock()
				return
			}
			_, err := o.email.SendEmail(ctx, port.SendEmailInput{
				To:              recipient.Email,
				Subject:         env.Subject,
				HTML:            recipientHTML,
				Text:            recipientText,
				From:            env.From,
				FromDisplayName: env.FromDisplayName,
				ReplyTo:         env.ReplyTo,
				GroupID:         env.GroupID,
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				failures = append(failures, SendFailure{Email: recipient.Email, Error: err.Error()})
				slog.WarnContext(ctx, "send fanout: recipient failed",
					"recipient", redactEmail(recipient.Email),
					"group_id", env.GroupID,
					"error", err.Error(),
				)
				return
			}
			sent++
		}()
	}
	wg.Wait()
	return sent, failed, failures
}

// resolveSenderName looks up the sender's display name from auth-service using
// the validated JWT principal. Empty principal short-circuits to "" so dev
// mode (REQUIRE_USER_AUTH=false) keeps working — the caller then falls back to
// the project-name chrome. A non-empty principal that fails the lookup
// (auth-service down, user not found, no display name available) bubbles the
// error so the handler returns 5xx and the send is refused rather than
// emitting an unattributed envelope.
func (o *SendOrchestrator) resolveSenderName(ctx context.Context, principal string) (string, error) {
	if strings.TrimSpace(principal) == "" {
		return "", nil
	}
	if o.userMetadata == nil {
		return "", nil
	}
	name, err := o.userMetadata.Name(ctx, principal)
	if err != nil {
		return "", fmt.Errorf("resolve sender name: %w", err)
	}
	return name, nil
}

func fallbackString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// sanitizeFromDisplayName trims surrounding whitespace and strips CR/LF and
// other control characters that would let an attacker inject extra email
// headers. Returns "" when nothing usable remains so the caller can apply its
// fallback.
func sanitizeFromDisplayName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	b := strings.Builder{}
	b.Grow(len(s))
	for _, r := range s {
		if r == '\r' || r == '\n' || r == '\t' || (r >= 0x00 && r < 0x20) || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// redactEmail masks the local part of an email for safe logging.
func redactEmail(email string) string {
	at := strings.Index(email, "@")
	if at <= 0 {
		return "***"
	}
	return email[:1] + "***" + email[at:]
}
