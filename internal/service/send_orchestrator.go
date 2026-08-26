// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
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
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service/render/declarative"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// defaultSendConcurrency caps in-flight email-service requests during fan-out.
const defaultSendConcurrency = 5

// defaultSendJobTimeout bounds the detached background fan-out. Generous by
// design: at the default concurrency of 5 and ~1s per email-service
// round-trip, 30 minutes covers ~9000 recipients.
const defaultSendJobTimeout = 30 * time.Minute

// persistTimeout bounds the terminal MarkSent / RevertSending write issued by
// the background job. Deliberately independent of the job context, which may
// already be exhausted when the fan-out consumed the full job timeout.
const persistTimeout = 30 * time.Second

// defaultFromAddress is the SMTP envelope From used when the orchestrator is
// constructed without an explicit FromAddress (e.g. tests). Production wiring
// always sets one through SendOrchestratorConfig.
const defaultFromAddress = "newsletter@lfx.linuxfoundation.org"

// defaultScheduleMaxHorizon mirrors SendGrid Mail Send's send_at limit
// (LFXV2-2685), used when the orchestrator is constructed without an
// explicit ScheduleMaxHorizon (e.g. tests).
const defaultScheduleMaxHorizon = 72 * time.Hour

// defaultScheduleCancelBuffer is the fallback schedule-window setting used
// when the orchestrator is constructed without explicit config (e.g. tests).
// Production wiring always sets this through SendOrchestratorConfig from AppConfig.
// ScheduleMinLead is computed dynamically from SendJobTimeout to ensure the
// fan-out has time to complete before scheduled_at.
const (
	defaultScheduleCancelBuffer = 10 * time.Minute
	minScheduleCancelBuffer     = 10 * time.Minute
	// minScheduleMinLeadSafetyMargin is added to the SendJobTimeout to compute
	// the minimum schedule lead. The total (SendJobTimeout + margin) ensures
	// all per-recipient provider arming calls complete before scheduled_at.
	minScheduleMinLeadSafetyMargin = 5 * time.Minute
)

// defaultReplyToAllowedDomains mirrors lfx-v2-email-service's default
// SMTP_ALLOWED_REPLY_TO_DOMAINS, used when the orchestrator is constructed
// without an explicit ReplyToAllowedDomains (e.g. tests). Production wiring
// always sets this through SendOrchestratorConfig from AppConfig.
var defaultReplyToAllowedDomains = []string{"linuxfoundation.org"}

// fromDisplayNameSuffix is appended to the project name to build the From
// display name, yielding e.g. "Kubernetes Newsletter".
const fromDisplayNameSuffix = " Newsletter"

// myNewslettersPath is the Self-Serve route where a recipient can browse the
// newsletters sent to them. Appended to SelfServeBaseURL to build the footer's
// "My Newsletters" deep link.
const myNewslettersPath = "/newsletters/my"

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
	userEmail     port.UserEmailReader
	unsub         *UnsubscribeService
	concurrency   int
	fanoutEnabled bool
	fromAddress   string
	fromOverrides map[string]string
	// sendProvider is the value stamped onto newsletters.send_provider at the
	// sending transition, recording which provider dispatched the newsletter so
	// analytics reads engagement from the matching store. Set from the active
	// EMAIL_PROVIDER; empty falls back to model.SendProviderEmailService.
	sendProvider string
	// replyToAllowedDomains gates resolveSenderEmail's output: a resolved
	// address outside these domains (suffix-matched) is dropped in favor of
	// the draft.EDReplyEmail fallback, so we never hand email-service a
	// reply_to it will reject outright (see ReplyToAllowedDomains doc).
	replyToAllowedDomains []string
	// selfServeBaseURL is the normalized (trimmed, no trailing slash) base URL
	// of the LFX Self-Serve app. Empty omits the footer's "My Newsletters"
	// line entirely.
	selfServeBaseURL string
	jobTimeout       time.Duration
	// jobs tracks detached background send goroutines so graceful shutdown
	// (and tests) can wait for in-flight fan-outs via Drain.
	jobs sync.WaitGroup
	// scheduleMaxHorizon/scheduleMinLead/scheduleCancelBuffer configure the
	// arm-time and cancel-time validation windows for scheduled sends
	// (LFXV2-2685). See SendOrchestratorConfig for their meaning.
	scheduleMaxHorizon   time.Duration
	scheduleMinLead      time.Duration
	scheduleCancelBuffer time.Duration
	// sendGridScheduler is a cached reference to the SendGrid scheduler (if
	// available) used to cancel outstanding schedules even after a provider
	// switch. Nil if the current EMAIL_PROVIDER is not sendgrid (LFXV2-2685).
	sendGridScheduler port.ScheduledSender
	// sendGridDispatcher is a cached reference to the SendGrid dispatcher's
	// engagement purger, used to purge engagement records for scheduled sends
	// even after a provider switch. Nil if SendGrid credentials are not configured.
	sendGridDispatcher port.EngagementPurger
}

// SendOrchestratorConfig configures a SendOrchestrator.
type SendOrchestratorConfig struct {
	Repo         port.NewsletterRepository
	Committee    port.CommitteeClient
	Project      port.ProjectMetadataClient
	Email        port.EmailDispatcher
	UserMetadata port.UserMetadataReader
	// UserEmail resolves the sender's primary email by principal, used to
	// derive the send-time Reply-To address. Nil is tolerated (e.g. tests) —
	// resolveSenderEmail falls back to the draft's stored ed_reply_email.
	UserEmail   port.UserEmailReader
	Unsubscribe *UnsubscribeService
	Concurrency int
	// FanoutEnabled is the feature toggle for the per-recipient send loop.
	// Defaults to true; flip false in environments where we want to validate
	// the recipient-resolution path without sending real mail.
	FanoutEnabled bool
	// FromAddress is the SMTP envelope From applied to every outbound email.
	// Empty falls back to defaultFromAddress.
	FromAddress string
	// FromAddressOverrides maps a normalized (lowercased) project slug to the
	// From address used for that project, overriding FromAddress. Nil/empty
	// means every project uses FromAddress.
	FromAddressOverrides map[string]string
	// SendProvider is the active dispatch provider stamped onto every send
	// (newsletters.send_provider), so analytics can later resolve engagement
	// from the matching store. Empty falls back to model.SendProviderEmailService.
	SendProvider string
	// ReplyToAllowedDomains lists the domains a resolved sender email may use
	// as Reply-To; a resolved address outside this list falls back to
	// draft.EDReplyEmail (see resolveSenderEmail). Empty defaults to
	// defaultReplyToAllowedDomains ("linuxfoundation.org").
	ReplyToAllowedDomains []string
	// SelfServeBaseURL is the base URL of the LFX Self-Serve app, used to
	// build the compliance footer's "My Newsletters" deep link
	// (<base>/newsletters/my). Empty omits the footer line.
	SelfServeBaseURL string
	// SendJobTimeout bounds the detached background fan-out that runs after a
	// send is accepted. Zero falls back to defaultSendJobTimeout.
	//
	// Coupled invariant: any stuck-'sending' recovery sweep wired alongside
	// this orchestrator must use a TTL comfortably greater than this timeout
	// (AppConfigFromEnv enforces TTL >= SendJobTimeout + 5m), or the sweep can
	// settle a row 'sent' while its fan-out job is still running.
	SendJobTimeout time.Duration
	// ScheduleMaxHorizon caps how far in the future a schedule can be armed
	// (LFXV2-2685). Zero falls back to the 72h SendGrid Mail Send limit.
	ScheduleMaxHorizon time.Duration
	// ScheduleMinLead is the minimum lead time a newly-armed schedule must
	// clear before scheduled_at. Zero falls back to 30 minutes, and any value
	// below 30 minutes is capped to 30m to ensure all per-recipient provider
	// arming calls complete before scheduled release time (LFXV2-2685).
	ScheduleMinLead time.Duration
	// ScheduleCancelBuffer rejects a cancel-schedule request inside this
	// window before scheduled_at (SendGrid API requires minimum 10m). Zero
	// falls back to 10 minutes.
	ScheduleCancelBuffer time.Duration
	// SendGridScheduler is an optional reference to a SendGrid scheduler for
	// cancelling outstanding scheduled sends even after a provider switch. When
	// nil, scheduled sends cannot be cancelled if the provider has switched away
	// from sendgrid (LFXV2-2685). Set when EMAIL_PROVIDER is sendgrid.
	SendGridScheduler port.ScheduledSender
	// SendGridDispatcher is an optional reference to a SendGrid engagement purger
	// for purging engagement records after scheduled sends are cancelled. Used to
	// route purge calls by draft.SendProvider instead of the active dispatcher, so
	// engagements from SendGrid schedules are cleaned up even after a provider
	// rollback to email-service (LFXV2-2685). Set when SendGrid credentials are
	// configured.
	SendGridDispatcher port.EngagementPurger
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
	// Normalize override keys defensively so matching is case-insensitive even
	// if a caller wires the map without going through config parsing.
	overrides := make(map[string]string, len(cfg.FromAddressOverrides))
	for slug, addr := range cfg.FromAddressOverrides {
		slug = strings.ToLower(strings.TrimSpace(slug))
		addr = strings.TrimSpace(addr)
		if slug == "" || addr == "" {
			continue
		}
		overrides[slug] = addr
	}
	// Normalize the reply-to allowlist the same way; empty config falls back
	// to the email-service default rather than an empty (reject-everything)
	// list.
	var replyToDomains []string
	for _, domain := range cfg.ReplyToAllowedDomains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" {
			continue
		}
		replyToDomains = append(replyToDomains, domain)
	}
	if len(replyToDomains) == 0 {
		replyToDomains = defaultReplyToAllowedDomains
	}
	jobTimeout := cfg.SendJobTimeout
	if jobTimeout <= 0 {
		jobTimeout = defaultSendJobTimeout
	}
	sendProvider := strings.TrimSpace(cfg.SendProvider)
	if sendProvider == "" {
		sendProvider = model.SendProviderEmailService
	}
	scheduleMaxHorizon := cfg.ScheduleMaxHorizon
	if scheduleMaxHorizon <= 0 {
		scheduleMaxHorizon = defaultScheduleMaxHorizon
	}
	// Compute minimum schedule lead based on actual job timeout: the scheduler
	// must give the fan-out enough time to complete provider arming calls before
	// scheduled_at arrives. If config provides an explicit lead, use it; otherwise
	// derive from the job timeout + safety margin.
	minComputedLead := jobTimeout + minScheduleMinLeadSafetyMargin
	scheduleMinLead := cfg.ScheduleMinLead
	if scheduleMinLead <= 0 {
		scheduleMinLead = minComputedLead
	} else if scheduleMinLead < minComputedLead {
		scheduleMinLead = minComputedLead
	}
	scheduleCancelBuffer := cfg.ScheduleCancelBuffer
	if scheduleCancelBuffer <= 0 {
		scheduleCancelBuffer = defaultScheduleCancelBuffer
	}
	if scheduleCancelBuffer < minScheduleCancelBuffer {
		scheduleCancelBuffer = minScheduleCancelBuffer
	}
	return &SendOrchestrator{
		repo:                  cfg.Repo,
		committee:             cfg.Committee,
		project:               cfg.Project,
		email:                 cfg.Email,
		userMetadata:          cfg.UserMetadata,
		userEmail:             cfg.UserEmail,
		unsub:                 cfg.Unsubscribe,
		concurrency:           c,
		fanoutEnabled:         cfg.FanoutEnabled,
		fromAddress:           from,
		fromOverrides:         overrides,
		replyToAllowedDomains: replyToDomains,
		selfServeBaseURL:      strings.TrimRight(strings.TrimSpace(cfg.SelfServeBaseURL), "/"),
		jobTimeout:            jobTimeout,
		sendProvider:          sendProvider,
		scheduleMaxHorizon:    scheduleMaxHorizon,
		scheduleMinLead:       scheduleMinLead,
		scheduleCancelBuffer:  scheduleCancelBuffer,
		sendGridScheduler:     cfg.SendGridScheduler,
		sendGridDispatcher:    cfg.SendGridDispatcher,
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
	// Schedule arms a scheduled release instead of sending immediately
	// (LFXV2-2685), set only by the POST .../schedule handler. It is the sole
	// discriminator that lets SendNewsletter serve both routes: when false
	// (the plain POST .../send path), ScheduledAt below is never consulted —
	// not even a draft's own saved draft.ScheduledAt — so a draft carrying a
	// saved-but-unarmed schedule still sends immediately.
	Schedule bool
	// ScheduledAt optionally overrides the draft's saved scheduled_at when
	// Schedule is true. Nil (with Schedule=true) falls back to the draft's own
	// saved value; if neither is set the request is invalid. Ignored entirely
	// when Schedule is false.
	ScheduledAt *time.Time
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

// SendNewsletter accepts a send request: it validates the draft, resolves
// recipients, renders the email envelope, and atomically transitions the draft
// to status=sending (the cross-replica duplicate-send guard). The per-recipient
// fan-out and the terminal sending → sent transition then run in a background
// goroutine detached from the request context, so a client disconnect or proxy
// timeout can neither cancel the fan-out mid-flight nor orphan the status.
//
// The returned SendResult carries the sending-state newsletter with Sent=0 —
// callers observe completion by re-fetching the newsletter (status settles to
// sent, or back to draft when zero recipients could be delivered to). The
// zero-recipient edge case keeps the historical contract and is marked sent
// synchronously.
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
	if draft.Status == model.StatusSending {
		return nil, domain.ErrSendInProgress
	}
	if draft.Status == model.StatusScheduled {
		return nil, domain.ErrScheduled
	}
	if in.ExpectedVersion != 0 && draft.Version != in.ExpectedVersion {
		return nil, domain.ErrVersionMismatch
	}

	// Resolve the schedule BEFORE doing any recipient-resolution or rendering
	// work, but defer the window validation until immediately before CreateBatchID
	// so the validation accounts for time consumed by recipient resolution and
	// rendering (LFXV2-2685). Schedule is the sole discriminator: a plain send
	// (Schedule=false) never consults ScheduledAt — not in.ScheduledAt, and not
	// draft.ScheduledAt — so a draft carrying a saved-but-unarmed schedule still
	// sends immediately when POST .../send is called directly.
	var scheduledAt *time.Time
	var scheduler port.ScheduledSender
	if in.Schedule {
		effective := in.ScheduledAt
		if effective == nil {
			effective = draft.ScheduledAt
		}
		if effective == nil {
			return nil, fmt.Errorf("%w: scheduled_at is required (not set on the draft or in the request)", domain.ErrInvalidRequest)
		}
		var ok bool
		scheduler, ok = o.email.(port.ScheduledSender)
		if !ok {
			return nil, pkgerrors.NewServiceUnavailable("scheduling requires the SendGrid provider (EMAIL_PROVIDER=sendgrid)")
		}
		scheduledAt = effective
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
	fromDisplayName := senderName
	if fromDisplayName == "" {
		fromDisplayName = projectName + fromDisplayNameSuffix
	}
	// Reply-To tracks whoever is sending, not whoever last saved the draft:
	// the same principal drives both the From display name and the Reply-To
	// address, so a recipient's reply always reaches the actual sender.
	// draft.EDReplyEmail (the drafter's address, captured at save time) is
	// only a fallback for dev mode or a resolution failure.
	replyTo := fallbackString(o.resolveSenderEmail(ctx, in.Principal), draft.EDReplyEmail)

	// Layout-based newsletters carry the FULL emitter email (the wrapper +
	// blocks, MJML-compiled) in body_html, with per-recipient runtime fields as
	// %%…%% placeholder sentinels. The emitter owns the whole email, so the send
	// path must NOT re-wrap it in email_chrome — doing so would nest a complete
	// document inside the chrome envelope and double the header/footer. Legacy
	// (body_html-only) newsletters take the chrome path unchanged.
	//
	// COMPLIANCE: draft.BodyHTML was rendered at WRITE time against the
	// unsubscribe config in effect then. If that config has since flipped (e.g.
	// unsubscribe was enabled after the draft was saved), the persisted body
	// carries a STALE compliance footer — a draft saved while unsubscribe was
	// disabled would otherwise send with NO opt-out row after it's enabled, a
	// CAN-SPAM gap. For a layout newsletter we therefore RE-RENDER body_html from
	// the persisted BodyLayout using the SEND-TIME config (o.unsub.Enabled()), so
	// the footer always reflects the current deployment setting instead of the
	// write-time snapshot. The re-render binds replyTo (the resolved sender) into
	// the wrapper's reply row, matching the legacy chrome path below. Legacy
	// newsletters have no layout to re-render and dispatch draft.BodyHTML unchanged.
	isLayout := layoutPresent(draft.BodyLayout)
	sendBodyHTML := draft.BodyHTML
	// A legacy (non-layout) draft can be persisted with an empty body_html — the
	// block-editor→basic-editor escape hatch clears a layout with no replacement
	// body yet (see the BodyLayoutSet branch in NewsletterService.Update). Such a
	// draft is allowed to SAVE but must not SEND: dispatching it would ship a
	// header/footer-only email. The layout path always yields content below (the
	// re-render produces a body or refuses), so this guards only the legacy path,
	// before recipient dispatch and the draft → sending transition.
	if !isLayout {
		if err := validateBodyHTML(sendBodyHTML); err != nil {
			return nil, fmt.Errorf("newsletter send: %w", err)
		}
	}
	if isLayout {
		if rerendered, ok := o.reRenderLayoutBody(ctx, draft, replyTo); ok {
			sendBodyHTML = rerendered
		} else {
			// Re-render failed: refuse the send regardless of unsubscribe setting.
			// The persisted body_html may contain stale compliance footers (unsubscribe
			// enabled/disabled since write time) or stale sentinels that would be
			// emptied during fan-out. Rather than risk sending a non-compliant email,
			// refuse and let the draft revert to draft for retry once the layout is
			// renderable again.
			return nil, fmt.Errorf("newsletter send: body_layout re-render failed; refusing to dispatch a persisted body that may not reflect current deployment settings")
		}
	}

	htmlBody, textBody := o.renderBody(isLayout, bodyRenderInput{
		subject:     draft.Subject,
		bodyHTML:    sendBodyHTML,
		displayName: projectName,
		senderName:  senderName,
		// Reply-To tracks the resolved sender (main's change), not the drafter's
		// saved address — matching the envelope ReplyTo and the layout re-render.
		edReplyEmail: replyTo,
		compliance:   true,
	})
	// Send-scope sentinels (%%SENDER_NAME%% / %%PROJECT_NAME%% / %%SEND_DATE%%)
	// are emitted only by the layout wrapper. Restrict substitution to the layout
	// path so a legacy body_html that happens to contain a literal %%SENDER_NAME%%
	// (etc.) is never silently rewritten — the legacy path never reserves or
	// validates these sentinels.
	if isLayout {
		sendDate := formatSendDate(scheduledAt)
		htmlBody = substituteSendScope(htmlBody, senderName, projectName, sendDate, true)
		textBody = substituteSendScope(textBody, senderName, projectName, sendDate, false)
	}

	groupID := uuid.NewString()

	envelope := emailEnvelope{
		Subject:         draft.Subject,
		HTML:            htmlBody,
		Text:            textBody,
		From:            o.resolveFromAddress(ctx, draft.ProjectUID),
		FromDisplayName: fromDisplayName,
		ReplyTo:         replyTo,
		GroupID:         groupID,
		IsLayout:        isLayout,
	}

	// Mint the provider batch id before claiming 'sending' so scheduled_at and
	// batch_id can be persisted atomically with the draft → sending transition
	// (LFXV2-2685). Every recipient in the fan-out below carries this same
	// SendAt/BatchID so the provider releases them together.
	// Validate the schedule window immediately before arming so the check accounts
	// for time consumed by recipient resolution and rendering (LFXV2-2685).
	var batchID string
	if scheduledAt != nil {
		if err := o.validateScheduleWindow(*scheduledAt); err != nil {
			return nil, err
		}
		batchID, err = scheduler.CreateBatchID(ctx)
		if err != nil {
			return nil, fmt.Errorf("mint schedule batch id: %w", err)
		}
		envelope.SendAt = scheduledAt
		envelope.BatchID = batchID
	}

	// The duplicate-send guard: a single optimistically-locked UPDATE gated on
	// status='draft'. From here on no concurrent or repeated send request can
	// enter the fan-out for this newsletter.
	sending, err := o.repo.MarkSending(ctx, draft.ID, groupID, o.sendProvider, len(recipients), draft.Version, scheduledAt, batchID)
	if err != nil {
		return nil, fmt.Errorf("mark sending: %w", err)
	}

	// Zero recipients: nothing to fan out — settle to sent synchronously,
	// preserving the historical contract for empty audiences. The write must
	// not ride the request context: a client disconnect after MarkSending
	// would cancel it and strand the row in 'sending' until the recovery
	// sweep.
	if len(recipients) == 0 {
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
		defer cancel()
		updated, markErr := o.repo.MarkSent(persistCtx, sending.ID, time.Now().UTC(), sending.Version)
		if markErr != nil {
			return nil, fmt.Errorf("mark sent: %w", markErr)
		}
		return &SendResult{
			Newsletter:      updated,
			GroupID:         groupID,
			TotalRecipients: 0,
		}, nil
	}

	// Detach the fan-out from the request lifetime. WithoutCancel keeps the
	// context values (trace/log correlation) but severs cancellation, so a BFF
	// timeout or client disconnect cannot abort a partially-dispatched send.
	jobCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), o.jobTimeout)
	o.jobs.Add(1)
	go func() {
		defer o.jobs.Done()
		defer cancel()
		o.runSendJob(jobCtx, sending, recipients, envelope)
	}()

	slog.InfoContext(ctx, "newsletter send accepted",
		"newsletter_id", sending.ID,
		"project_uid", sending.ProjectUID,
		"group_id", groupID,
		"total_recipients", len(recipients),
	)

	return &SendResult{
		Newsletter:      sending,
		GroupID:         groupID,
		TotalRecipients: len(recipients),
	}, nil
}

// reRenderLayoutBody recompiles a layout newsletter's body_html from its
// persisted BodyLayout using the SEND-TIME unsubscribe configuration
// (o.unsub.Enabled()), so the compliance/unsubscribe footer reflects the
// deployment's CURRENT setting rather than the write-time snapshot baked into
// draft.BodyHTML. It returns the recompiled HTML and ok=true on success.
//
// On failure (ok=false) — an unparseable stored layout, a block type no longer
// present in the template set, or an MJML compile error — it logs a warning and
// returns ok=false. The caller REFUSES the send (returns an error) rather than
// fall back to persisted draft.BodyHTML, because the persisted body may contain
// stale compliance footers or sentinels that would be emptied during fan-out
// (unsubscribe setting may have changed since write time). The newsletter row
// reverts to draft for retry once the stored layout is renderable again.
func (o *SendOrchestrator) reRenderLayoutBody(ctx context.Context, draft *model.Newsletter, replyTo string) (string, bool) {
	var layout declarative.Layout
	if err := json.Unmarshal(draft.BodyLayout, &layout); err != nil {
		slog.WarnContext(ctx, "newsletter send: stored body_layout unparseable; caller refuses the send",
			"newsletter_id", draft.ID,
			"project_uid", draft.ProjectUID,
			"error", err,
		)
		return "", false
	}
	// renderLayout binds the send-time-known reply email (replyTo — the resolved
	// sender, not the drafter's saved address) and the current unsubscribe setting
	// into the wrapper footer, matching the legacy chrome path and the envelope
	// ReplyTo. StripHTMLForText later derives the text/plain part from this same
	// HTML, so the two stay consistent.
	html, _, err := renderLayout(ctx, &layout, draft.Subject, replyTo, sendUnsubFooterMode(o.unsub.Enabled()), o.selfServeBaseURL != "")
	if err != nil {
		slog.WarnContext(ctx, "newsletter send: body_layout re-render failed; caller refuses the send",
			"newsletter_id", draft.ID,
			"project_uid", draft.ProjectUID,
			"error", err,
		)
		return "", false
	}
	// Bound the re-rendered output to the same ceiling create/update enforce.
	// Embedded templates can change between write and send (the whole reason this
	// helper re-renders), so a layout that was under the cap at write time can
	// compile past it now — validating here keeps an oversized body off the wire,
	// and ok=false routes it through the same refuse/fallback policy as any other
	// re-render failure.
	if err := validateDerivedBodyHTML(html); err != nil {
		slog.WarnContext(ctx, "newsletter send: re-rendered body_layout exceeds the size cap; caller refuses the send",
			"newsletter_id", draft.ID,
			"project_uid", draft.ProjectUID,
			"error", err,
		)
		return "", false
	}
	return html, true
}

// runSendJob is the detached background half of SendNewsletter: fan out to
// every recipient, then settle the newsletter — sent when at least one
// recipient was delivered to, reverted to draft when none were.
func (o *SendOrchestrator) runSendJob(ctx context.Context, sending *model.Newsletter, recipients []model.CommitteeMember, envelope emailEnvelope) {
	sent, failed, unknown, failures := o.fanOut(ctx, sending.ProjectUID, recipients, envelope)

	// The terminal persistence write must not depend on the job context: a
	// fan-out that consumed the entire job timeout would otherwise leave the
	// row stuck in 'sending' until the recovery sweep picks it up.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()

	// Revert to draft ONLY when nothing was accepted AND nothing is ambiguous —
	// i.e. every recipient was definitively rejected, so a retry cannot duplicate
	// a message the provider already accepted. If any outcome is ambiguous (a
	// transport error or a 5xx where the provider MAY have accepted the message),
	// fall through and settle as sent instead of creating a retryable draft:
	// re-sending those recipients could duplicate. This mirrors crash recovery —
	// when it's unknown whether mail went out, settle to `sent` and let analytics
	// (via group_id) expose the real delivery rather than risk duplicates.
	if sent == 0 && unknown == 0 {
		slog.WarnContext(ctx, "newsletter send failed: all recipients definitively rejected, reverting to draft",
			"newsletter_id", sending.ID,
			"project_uid", sending.ProjectUID,
			"group_id", envelope.GroupID,
			"total_recipients", len(recipients),
			"failed", failed,
			"unknown", unknown,
			"first_failure", firstFailureError(failures),
		)
		// The revert can also fail because another actor already settled the
		// row (RevertSending gates on status='sending'), so the log must not
		// assert the resulting state.
		if err := o.repo.RevertSending(persistCtx, sending.ID); err != nil {
			slog.ErrorContext(ctx, "newsletter send: revert to draft failed; the recovery sweep settles any row still 'sending'",
				"newsletter_id", sending.ID,
				"error", err,
			)
		} else {
			// The revert cleared group_id (and batch_id, for a scheduled
			// attempt), so any provider-side state keyed to this fully-failed
			// send is now orphaned — best-effort cleanup, so a failure only
			// logs. Keyed off envelope.BatchID (set only when THIS request
			// armed a schedule), for the same reason the settlement branch
			// above is keyed off envelope.SendAt rather than a row column.
			if envelope.BatchID != "" {
				if scheduler, ok := o.email.(port.ScheduledSender); ok {
					if err := scheduler.CancelScheduledBatch(persistCtx, envelope.BatchID); err != nil {
						slog.WarnContext(persistCtx, "newsletter send: failed to cancel provider batch after revert",
							"newsletter_id", sending.ID,
							"batch_id", envelope.BatchID,
							"error", err,
						)
					}
				}
			}
			if purger, ok := o.email.(port.EngagementPurger); ok {
				if err := purger.PurgeEngagement(persistCtx, envelope.GroupID); err != nil {
					slog.WarnContext(persistCtx, "newsletter send: failed to purge provider engagement after revert",
						"newsletter_id", sending.ID,
						"group_id", envelope.GroupID,
						"error", err,
					)
				}
			}
		}
		return
	}

	// A scheduled fan-out settles to 'scheduled' — the provider is holding
	// these messages for release at ScheduledAt, not delivering them now
	// (LFXV2-2685). SettleDueScheduled flips it to 'sent' once that time
	// passes.
	//
	// Deliberately keyed off envelope.SendAt — set only when THIS request
	// armed a schedule (SendNewsletter's in.Schedule branch) — and not off
	// sending.ScheduledAt. A plain immediate send never clears scheduled_at
	// on a draft that has one saved-but-unarmed (MarkSending only touches
	// that column when it is itself asked to persist a schedule), so
	// sending.ScheduledAt can be non-nil purely as leftover saved intent.
	// Branching on that column would misfile an ordinary send as scheduled.
	if envelope.SendAt != nil {
		if _, err := o.repo.MarkScheduled(persistCtx, sending.ID, sending.Version); err != nil {
			if errors.Is(err, domain.ErrAlreadySent) || errors.Is(err, domain.ErrScheduled) {
				slog.WarnContext(ctx, "newsletter schedule: row was already settled by another actor",
					"newsletter_id", sending.ID,
					"group_id", envelope.GroupID,
					"sent", sent,
					"failed", failed,
				)
				return
			}
			slog.ErrorContext(ctx, "newsletter schedule: mark scheduled failed after fan-out; the recovery sweep settles any row still 'sending'",
				"newsletter_id", sending.ID,
				"group_id", envelope.GroupID,
				"sent", sent,
				"failed", failed,
				"error", err,
			)
			return
		}
		slog.InfoContext(ctx, "newsletter scheduled",
			"newsletter_id", sending.ID,
			"project_uid", sending.ProjectUID,
			"group_id", envelope.GroupID,
			"batch_id", envelope.BatchID,
			"scheduled_at", sending.ScheduledAt,
			"total_recipients", len(recipients),
			"sent", sent,
			"failed", failed,
		)
		return
	}

	if _, err := o.repo.MarkSent(persistCtx, sending.ID, time.Now().UTC(), sending.Version); err != nil {
		// ErrAlreadySent means another actor (normally the stuck-sending
		// recovery sweep, under a misconfigured TTL) settled the row first —
		// the newsletter IS sent, only this job lost the race to record it.
		if errors.Is(err, domain.ErrAlreadySent) {
			slog.WarnContext(ctx, "newsletter send: row was already settled 'sent' by another actor",
				"newsletter_id", sending.ID,
				"group_id", envelope.GroupID,
				"sent", sent,
				"failed", failed,
			)
			return
		}
		slog.ErrorContext(ctx, "newsletter send: mark sent failed after fan-out; the recovery sweep settles any row still 'sending'",
			"newsletter_id", sending.ID,
			"group_id", envelope.GroupID,
			"sent", sent,
			"failed", failed,
			"error", err,
		)
		return
	}

	slog.InfoContext(ctx, "newsletter sent",
		"newsletter_id", sending.ID,
		"project_uid", sending.ProjectUID,
		"group_id", envelope.GroupID,
		"total_recipients", len(recipients),
		"sent", sent,
		"failed", failed,
	)
}

// Drain blocks until every detached send job has finished, or the context is
// done. Returns true when fully drained. Called during graceful shutdown so
// in-flight fan-outs get a chance to settle before the process exits; a job
// outliving the shutdown window is recovered by the stuck-sending sweep on the
// next pod.
func (o *SendOrchestrator) Drain(ctx context.Context) bool {
	done := make(chan struct{})
	go func() {
		o.jobs.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// firstFailureError surfaces one representative fan-out error for logging.
func firstFailureError(failures []SendFailure) string {
	if len(failures) == 0 {
		return ""
	}
	return failures[0].Error
}

// validateScheduleWindow enforces the arm-time window (LFXV2-2685):
// scheduledAt must clear the minimum lead time and must not exceed the
// configured horizon (itself capped at SendGrid Mail Send's 72h send_at
// limit). This is deliberately stricter than the lenient future-only check
// applied at save time (CreateDraft/UpdateDraft) — a draft may save a time
// further out than this window; it just can't be armed until it falls inside
// it.
func (o *SendOrchestrator) validateScheduleWindow(scheduledAt time.Time) error {
	now := time.Now().UTC()
	minAt := now.Add(o.scheduleMinLead)
	maxAt := now.Add(o.scheduleMaxHorizon)
	if scheduledAt.Before(minAt) {
		return fmt.Errorf("%w: scheduled_at must be at least %s in the future", domain.ErrInvalidRequest, o.scheduleMinLead)
	}
	if scheduledAt.After(maxAt) {
		return fmt.Errorf("%w: scheduled_at must be no more than %s in the future", domain.ErrInvalidRequest, o.scheduleMaxHorizon)
	}
	return nil
}

// CancelScheduledInput is the typed input for CancelScheduled.
type CancelScheduledInput struct {
	ProjectUID      string
	NewsletterID    uuid.UUID
	ExpectedVersion int64
}

// CancelScheduled cancels an armed schedule and returns the newsletter to
// draft (LFXV2-2685). The provider batch is cancelled BEFORE our row is
// mutated, so a SendGrid failure leaves the row truthfully still 'scheduled'
// rather than reporting a cancellation that didn't actually take effect
// upstream.
func (o *SendOrchestrator) CancelScheduled(ctx context.Context, in CancelScheduledInput) (*model.Newsletter, error) {
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
	// Allow cancellation of both scheduled (sent to provider, waiting) and sending
	// (fan-out in progress with an armed batch_id) rows. This permits race-safe
	// cancellation of in-flight batches before the recovery sweep runs (LFXV2-2685).
	if draft.Status != model.StatusScheduled && draft.Status != model.StatusSending {
		if draft.Status == model.StatusSent {
			return nil, domain.ErrAlreadySent
		}
		return nil, fmt.Errorf("%w: newsletter is not scheduled", domain.ErrInvalidRequest)
	}
	// A sending row with a batch_id can be cancelled, but a sending row WITHOUT one
	// (immediate send in progress) cannot — only the recovery sweep can deal with it.
	if draft.Status == model.StatusSending && draft.BatchID == nil {
		return nil, fmt.Errorf("%w: newsletter is not scheduled", domain.ErrInvalidRequest)
	}
	if in.ExpectedVersion != 0 && draft.Version != in.ExpectedVersion {
		return nil, domain.ErrVersionMismatch
	}
	if draft.ScheduledAt != nil && time.Now().UTC().After(draft.ScheduledAt.Add(-o.scheduleCancelBuffer)) {
		return nil, domain.ErrCancelWindowClosed
	}

	if draft.BatchID != nil {
		// Route cancellation by the provider that armed this schedule, not the
		// current EMAIL_PROVIDER. If the provider was switched after scheduling,
		// we must still support cancellation of outstanding SendGrid batches
		// (LFXV2-2685). Only SendGrid supports scheduled sends.
		var scheduler port.ScheduledSender
		switch draft.SendProvider {
		case model.SendProviderSendGrid:
			scheduler = o.sendGridScheduler
			if scheduler == nil {
				// The newsletter was originally sent via SendGrid and armed a batch,
				// but the provider has been switched away (EMAIL_PROVIDER is no longer
				// sendgrid) and we have no cached reference. The batch is still queued
				// at SendGrid and cannot be cancelled without provider credentials.
				return nil, pkgerrors.NewServiceUnavailable(
					"cancelling this schedule requires the SendGrid provider (EMAIL_PROVIDER=sendgrid); the provider appears to have been switched",
				)
			}
		default:
			// email-service does not support scheduled sends; a batch_id should never
			// exist on an email-service send. This is a data corruption issue.
			return nil, pkgerrors.NewUnexpected(
				fmt.Sprintf("newsletter has a batch_id but send_provider=%q (which does not support scheduling)", draft.SendProvider),
				nil,
			)
		}
		if err := scheduler.CancelScheduledBatch(ctx, *draft.BatchID); err != nil {
			return nil, fmt.Errorf("cancel provider batch: %w", err)
		}
	}

	// Use a fresh context with timeout independent of the request context. The
	// request context may already be exhausted from the provider cancellation
	// call above, but reverting to draft must persist reliably. Pass the batch_id
	// that was cancelled so the revert predicates on that same batch, preventing a
	// delayed duplicate cancel from reverting a newer send (LFXV2-2685).
	revertCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reverted, err := o.repo.RevertScheduled(revertCtx, draft.ID, draft.Version, draft.BatchID)
	if err != nil {
		return nil, fmt.Errorf("revert scheduled: %w", err)
	}

	if draft.GroupID != nil {
		// Route engagement purge by the provider that owned this send, not the
		// active dispatcher. This ensures SendGrid scheduled sends are properly
		// cleaned up even after a provider switch to email-service (LFXV2-2685).
		// Use an independent bounded context to ensure cleanup succeeds even if the
		// request context was cancelled by the client during provider cancellation.
		var purger port.EngagementPurger
		switch draft.SendProvider {
		case model.SendProviderSendGrid:
			purger = o.sendGridDispatcher
		default:
			purger, _ = o.email.(port.EngagementPurger)
		}
		if purger != nil {
			// Create an independent context for purge so it succeeds even if the
			// request context is exhausted or cancelled (e.g., client disconnect).
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := purger.PurgeEngagement(cleanupCtx, *draft.GroupID); err != nil {
				cleanupCancel()
				slog.WarnContext(context.Background(), "cancel schedule: failed to purge provider engagement after revert",
					"newsletter_id", draft.ID,
					"group_id", *draft.GroupID,
					"provider", draft.SendProvider,
					"error", err,
				)
			} else {
				cleanupCancel()
			}
		}
	}

	slog.InfoContext(ctx, "newsletter schedule cancelled",
		"newsletter_id", reverted.ID,
		"project_uid", reverted.ProjectUID,
	)
	return reverted, nil
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
	// IsLayout is DEPRECATED and ignored (see api.TestSendRequest.IsLayout).
	// BodyLayout is the sole layout trigger; this field has no effect and is
	// retained only for wire-compatibility with clients that still send it.
	IsLayout bool
	// BodyLayout, when set, is the structured layout to recompile server-side
	// for the test send. It is the sole layout trigger: the body is derived from
	// it with the opt-out row suppressed (no dangling unsubscribe link in a
	// non-recipient test; other footer elements like sender attribution and reply
	// row still render). Nil keeps the legacy BodyHTML path.
	BodyLayout *declarative.Layout
}

// TestSend dispatches a single test email — no persistence, no analytics.
//
// Legacy (body_html) path: the body renders the same compliance footer as a
// real send, including a working unsubscribe link minted for to_email (clicking
// it records a real project-scoped opt-out for that address) plus the My
// Newsletters deep link.
//
// Layout (body_layout) path: the server recompiles the layout with the opt-out
// row SUPPRESSED (a test mints no real token, so a layout preview carries no
// unsubscribe link); other footer elements like sender attribution and the
// reply row still render. The emitter body is dispatched verbatim, never
// re-wrapped in email_chrome.
func (o *SendOrchestrator) TestSend(ctx context.Context, in TestSendInput) error {
	if err := validateProjectUID(in.ProjectUID); err != nil {
		return err
	}
	if err := validateSubject(in.Subject); err != nil {
		return err
	}
	if strings.TrimSpace(in.EDReplyEmail) != "" {
		if err := validateEDReplyEmail(in.EDReplyEmail); err != nil {
			return err
		}
	}
	parsedTo, err := mail.ParseAddress(strings.TrimSpace(in.ToEmail))
	if err != nil {
		return fmt.Errorf("%w: to_email is not a valid email: %v", domain.ErrInvalidRequest, err)
	}
	// Canonical addr-spec: mail.ParseAddress accepts display-name forms such
	// as "Tester <tester@example.com>", but the unsubscribe token must sign
	// the bare address recipient resolution compares opt-outs against, and
	// the dispatch To must carry the same canonical value.
	toEmail := parsedTo.Address

	// Resolve sender email and name early so layout rendering gets the correct
	// reply-to address for the footer, matching the real-send behavior where the
	// footer reflects the resolved sender, not the drafter's saved EDReplyEmail.
	projectName, _ := o.project.Name(ctx, in.ProjectUID)
	if projectName == "" {
		projectName = "Project"
	}
	senderName, err := o.resolveSenderName(ctx, in.Principal)
	if err != nil {
		return err
	}
	// Mirror SendNewsletter's Reply-To resolution so a test-send previews the
	// same envelope a real send would produce, not the drafter's stored
	// EDReplyEmail unconditionally.
	replyTo := fallbackString(o.resolveSenderEmail(ctx, in.Principal), strings.TrimSpace(in.EDReplyEmail))

	// body_layout is the SOLE layout trigger for a test send. in.IsLayout is
	// deprecated and ignored here (a precompiled is_layout body_html is no longer
	// dispatched verbatim — that path could leave a dangling
	// <a href="">Unsubscribe</a> once its sentinel resolved empty); layout
	// clients send body_layout instead.
	isLayout := in.BodyLayout != nil

	fromDisplayName := senderName
	if fromDisplayName == "" {
		fromDisplayName = projectName + fromDisplayNameSuffix
	}

	var htmlBody, textBody string
	// The List-Unsubscribe header URL for this test recipient. Set only on the
	// legacy path, where a real opt-out link is minted for to_email; the layout
	// test path mints no token, so it stays empty and no header is sent.
	var testListUnsubURL string
	if isLayout {
		// Layout test send: recompile server-side with the opt-out row SUPPRESSED
		// (unsubFooterSuppressed drops it via the wrapper's if= guard) — a test
		// mints no real token, so a preview carries no opt-out link. The emitter
		// owns the whole email; it is dispatched verbatim, never re-wrapped in
		// email_chrome (mirrors the real-send layout branch).
		// manageEnabled=false: a test send mints no per-recipient context and
		// substituteTestPlaceholders zeroes the manage sentinel, so binding it
		// would only leave an empty href. The wrapper renders the label unlinked.
		derived, _, rerr := renderLayout(ctx, in.BodyLayout, in.Subject, replyTo, unsubFooterSuppressed, false)
		if rerr != nil {
			return rerr
		}
		if err := validateDerivedBodyHTML(derived); err != nil {
			return err
		}
		htmlBody, textBody = o.renderBody(true, bodyRenderInput{
			subject:     in.Subject,
			bodyHTML:    derived,
			displayName: projectName,
		})
	} else {
		// Legacy (body_html) path: preview the SAME compliance footer a real send
		// produces, including a working unsubscribe link minted for to_email.
		// This is main's newer test-send behavior and must win over the older
		// footer-less preview.
		if err := validateBodyHTML(in.BodyHTML); err != nil {
			return err
		}
		chrome := render.Chrome{
			Subject:                 in.Subject,
			BodyHTML:                in.BodyHTML,
			DisplayName:             projectName,
			IncludeComplianceFooter: true,
			EDName:                  fallbackString(senderName, "Executive Director"),
			EDReplyEmail:            replyTo,
		}
		if o.unsub.Enabled() {
			// Single recipient: mint the real link directly instead of the
			// placeholder+ReplaceAll pattern the fan-out uses. BuildURL output has
			// no HTML-escapable characters, so the footer is byte-identical to a
			// real send's post-substitution body.
			chrome.UnsubscribeURL = o.unsub.BuildURL(in.ProjectUID, toEmail)
			testListUnsubURL = chrome.UnsubscribeURL
		}
		if o.selfServeBaseURL != "" {
			chrome.MyNewslettersURL = o.selfServeBaseURL + myNewslettersPath
		}
		htmlBody = render.EmailHTML(chrome)
		textBody = render.EmailText(chrome)
	}

	// Bind the sender/project scope sentinels the layout wrapper emits. Restrict
	// to the layout path (mirroring the real send): the legacy chrome body carries
	// none of these sentinels, and gating avoids silently rewriting a literal
	// %%SENDER_NAME%% (etc.) an author may have put in body_html. The real
	// unsubscribe URL minted above is not a placeholder, so the
	// substituteTestPlaceholders pass at dispatch leaves it intact. A test send is
	// immediate, so the header date is today.
	if isLayout {
		testSendDate := formatSendDate(nil)
		htmlBody = substituteSendScope(htmlBody, senderName, projectName, testSendDate, true)
		textBody = substituteSendScope(textBody, senderName, projectName, testSendDate, false)
	}

	if !o.fanoutEnabled {
		slog.InfoContext(ctx, "test-send: fanout disabled, accepted without dispatch",
			"to_email", toEmail,
			"project_uid", in.ProjectUID,
		)
		return nil
	}
	// Resolve the runtime sentinels for this test recipient. A test send never
	// mints a real unsubscribe token (see substituteTestPlaceholders); the
	// wrapper's opt-out rows drop out. Only the layout path carries these
	// sentinels — the legacy chrome body already holds a real (minted) unsubscribe
	// URL and no %%…%% tokens, so it is dispatched as-is to avoid rewriting any
	// literal sentinel an author placed in body_html.
	dispatchHTML, dispatchText := htmlBody, textBody
	if isLayout {
		dispatchHTML = o.substituteTestPlaceholders(htmlBody)
		dispatchText = o.substituteTestPlaceholders(textBody)
	}
	sendInput := port.SendEmailInput{
		To:              toEmail,
		Subject:         in.Subject,
		HTML:            dispatchHTML,
		Text:            dispatchText,
		From:            o.resolveFromAddress(ctx, in.ProjectUID),
		FromDisplayName: fromDisplayName,
		ReplyTo:         replyTo,
	}
	if testListUnsubURL != "" {
		sendInput.ListUnsubscribeURL = testListUnsubURL
		sendInput.ListUnsubscribePost = true
	}
	if _, dispatchErr := o.email.SendEmail(ctx, sendInput); dispatchErr != nil {
		return fmt.Errorf("dispatch test-send: %w", dispatchErr)
	}
	slog.InfoContext(ctx, "test-send dispatched",
		"to_email", toEmail,
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
	// SendAt/BatchID arm a scheduled release (LFXV2-2685): when SendAt is
	// non-nil, every recipient in the fan-out carries the same SendAt/BatchID
	// so the provider holds and releases them together. Nil/empty for an
	// immediate send.
	SendAt  *time.Time
	BatchID string
	// IsLayout marks a layout-wrapper email (vs a legacy body_html email). The
	// layout-only URL sentinels (%%MANAGE_SUBSCRIPTIONS_URL%% / %%VIEW_ONLINE_URL%%)
	// are substituted per recipient only when this is true, so a legacy body that
	// happens to contain those literals is never rewritten.
	IsLayout bool
}

// fanOut dispatches per-recipient send_email requests to email-service with
// bounded concurrency. The fan-out never returns an error — per-recipient
// failures are captured and surfaced in the result so the caller can decide
// how to react. A nil EmailDispatcher (or FanoutEnabled=false) short-circuits
// to "all sent, none failed" for dev/test environments.
func (o *SendOrchestrator) fanOut(ctx context.Context, projectUID string, recipients []model.CommitteeMember, env emailEnvelope) (sent, failed, unknown int, failures []SendFailure) {
	if len(recipients) == 0 {
		return 0, 0, 0, nil
	}
	if !o.fanoutEnabled {
		slog.InfoContext(ctx, "send fanout disabled, marking all as sent without dispatch",
			"total_recipients", len(recipients),
			"group_id", env.GroupID,
		)
		return len(recipients), 0, 0, nil
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
			// Substitute per-recipient runtime placeholders over the rendered
			// body (and its text counterpart). The VIEW_ONLINE / MANAGE sentinels
			// are layout-only, so env.IsLayout gates them: the legacy path
			// substitutes ONLY the unsubscribe sentinel, leaving any literal
			// %%…%% an author placed in body_html untouched. substitutePlaceholders
			// also embeds the per-recipient unsubscribe URL (o.unsub.BuildURL).
			recipientHTML := o.substitutePlaceholders(env.HTML, projectUID, recipient.Email, true, env.IsLayout)
			recipientText := o.substitutePlaceholders(env.Text, projectUID, recipient.Email, false, env.IsLayout)
			// List-Unsubscribe header (RFC 8058): the raw per-recipient opt-out URL,
			// the same one substitutePlaceholders embeds in the body — built once
			// more here because the header needs the unescaped URL, not the
			// HTML-escaped body form.
			var listUnsubscribeURL string
			if o.unsub.Enabled() {
				listUnsubscribeURL = o.unsub.BuildURL(projectUID, recipient.Email)
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
			sendInput := port.SendEmailInput{
				To:              recipient.Email,
				Subject:         env.Subject,
				HTML:            recipientHTML,
				Text:            recipientText,
				From:            env.From,
				FromDisplayName: env.FromDisplayName,
				ReplyTo:         env.ReplyTo,
				GroupID:         env.GroupID,
				SendAt:          env.SendAt,
				BatchID:         env.BatchID,
			}
			if listUnsubscribeURL != "" {
				sendInput.ListUnsubscribeURL = listUnsubscribeURL
				sendInput.ListUnsubscribePost = true
			}
			_, err := o.email.SendEmail(ctx, sendInput)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				// An ambiguous outcome (the provider may have accepted the message)
				// is counted separately so an all-ambiguous fan-out does NOT trip
				// the retryable all-failed revert, which could duplicate a send the
				// provider already accepted. A definitive failure is safe to retry.
				if errors.Is(err, port.ErrAmbiguousSend) {
					unknown++
				} else {
					failed++
				}
				failures = append(failures, SendFailure{Email: recipient.Email, Error: err.Error()})
				slog.WarnContext(ctx, "send fanout: recipient failed",
					"recipient", redactEmail(recipient.Email),
					"group_id", env.GroupID,
					"ambiguous", errors.Is(err, port.ErrAmbiguousSend),
					"error", err.Error(),
				)
				return
			}
			sent++
		}()
	}
	wg.Wait()
	return sent, failed, unknown, failures
}

// resolveSenderName looks up the sender's display name from auth-service using
// the validated JWT principal, then validates it with net/mail.ParseAddress
// so the stdlib's RFC 5322 parser — not a hand-rolled character allowlist —
// decides what's safe to render in the From header. Empty principal short-
// circuits to "" so dev mode (REQUIRE_USER_AUTH=false) keeps working — the
// caller then falls back to the project-name chrome.
//
// All other failure modes are classified as internal (5xx) and bubble up so
// the send is refused rather than emitting a malformed or unattributed
// envelope. Specifically:
//   - non-empty principal with no UserMetadata wired (prod misconfiguration);
//   - auth-service unreachable / user not found / no display name (the typed
//     error from the client is intentionally re-wrapped as Unexpected so it
//     doesn't surface to the API consumer as 404/400 — they asked to send a
//     newsletter, not to look up a user);
//   - resolved name fails to parse as an RFC 5322 display-name phrase
//     (raw CR/LF, NUL, etc.).
func (o *SendOrchestrator) resolveSenderName(ctx context.Context, principal string) (string, error) {
	if strings.TrimSpace(principal) == "" {
		return "", nil
	}
	if o.userMetadata == nil {
		return "", pkgerrors.NewUnexpected("sender name resolution is required but UserMetadata client is not configured")
	}
	// Auth-service errors are folded into the message string instead of wrapped:
	// the client returns typed pkgerrors.NotFound / Validation that would, via
	// errors.As in the HTTP classifier, still surface as 404/400 even though the
	// API consumer didn't ask for that user — they asked to send a newsletter.
	name, err := o.userMetadata.Name(ctx, principal)
	if err != nil {
		return "", pkgerrors.NewUnexpected(fmt.Sprintf("resolve sender name from auth-service: %v", err))
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", nil
	}
	parsed, err := mail.ParseAddress(trimmed + " <noreply@invalid.localhost>")
	if err != nil {
		return "", pkgerrors.NewUnexpected(fmt.Sprintf("invalid sender display name from auth-service: %v", err))
	}
	return parsed.Name, nil
}

// resolveSenderEmail looks up the sender's primary email from auth-service
// using the validated JWT principal, so Reply-To can track whoever is
// actually sending rather than the drafter recorded in
// draft.EDReplyEmail. Unlike resolveSenderName, failure here is
// non-fatal: an empty principal (dev mode), an unconfigured UserEmail
// client, an auth-service error, or an unparseable address all fall back
// to "" so the caller substitutes draft.EDReplyEmail — a newsletter should
// still send even if the sender's own email can't be resolved.
func (o *SendOrchestrator) resolveSenderEmail(ctx context.Context, principal string) string {
	if strings.TrimSpace(principal) == "" {
		return ""
	}
	if o.userEmail == nil {
		return ""
	}
	email, err := o.userEmail.PrimaryEmail(ctx, principal)
	if err != nil {
		slog.WarnContext(ctx, "reply-to: sender email resolution failed, falling back to stored ed_reply_email",
			"error", err,
		)
		return ""
	}
	trimmed := strings.TrimSpace(email)
	if trimmed == "" {
		return ""
	}
	if _, err := mail.ParseAddress(trimmed); err != nil {
		slog.WarnContext(ctx, "reply-to: sender email from auth-service failed RFC 5322 validation, falling back to stored ed_reply_email",
			"error", err,
		)
		return ""
	}
	if !domainAllowed(trimmed, o.replyToAllowedDomains) {
		slog.WarnContext(ctx, "reply-to: sender email domain not in allowed reply-to domains, falling back to stored ed_reply_email",
			"domain", domainOf(trimmed),
		)
		return ""
	}
	return trimmed
}

// domainOf returns the lowercased domain portion of an email address, or ""
// if the address doesn't parse or has no "@". This must derive the domain
// EXACTLY the way lfx-v2-email-service's send_email_handler.go does: parse
// via net/mail, then split the re-serialized Address on the FIRST "@" via
// strings.SplitN(_, "@", 2) — not the original input, and not the last "@".
// A quoted local-part containing "@" (e.g. `"a@b"@example.com`) re-serializes
// to a@b@example.com, and the peer treats everything after the first "@"
// ("b@example.com") as the domain, not "example.com". Splitting differently
// here would let this guard approve a Reply-To that email-service still
// rejects, failing the entire fan-out downstream.
func domainOf(email string) string {
	parsed, err := mail.ParseAddress(email)
	if err != nil {
		return ""
	}
	parts := strings.SplitN(parsed.Address, "@", 2)
	if len(parts) != 2 {
		return ""
	}
	return strings.ToLower(parts[1])
}

// domainAllowed reports whether email's domain is in allowed, either exactly
// or as a subdomain (suffix match), mirroring lfx-v2-email-service's
// SMTP_ALLOWED_REPLY_TO_DOMAINS matching. See domainOf for why the domain
// must be derived from the peer's exact parse-then-split behavior.
func domainAllowed(email string, allowed []string) bool {
	domain := domainOf(email)
	if domain == "" {
		return false
	}
	for _, a := range allowed {
		if domain == a || strings.HasSuffix(domain, "."+a) {
			return true
		}
	}
	return false
}

// resolveFromAddress returns the project-specific From override keyed by the
// project slug, falling back to the deployment default when no override is
// configured or the slug isn't mapped. A slug-lookup failure is non-fatal: we
// log and use the default rather than block the send.
func (o *SendOrchestrator) resolveFromAddress(ctx context.Context, projectUID string) string {
	if len(o.fromOverrides) == 0 {
		return o.fromAddress
	}
	slug, err := o.project.Slug(ctx, projectUID)
	if err != nil {
		slog.WarnContext(ctx, "from-address override: project slug lookup failed, using default From",
			"project_uid", projectUID,
			"error", err,
		)
		return o.fromAddress
	}
	if override, ok := o.fromOverrides[strings.ToLower(strings.TrimSpace(slug))]; ok {
		return override
	}
	return o.fromAddress
}

// layoutPresent reports whether a newsletter's stored BodyLayout selects the
// layout-based send path. A nil or whitespace-only raw message (the bun
// `nullzero` column maps an absent layout to nil) means the legacy
// body_html-only path.
func layoutPresent(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) > 0
}

// bodyRenderInput carries the fields renderBody needs to produce the email
// HTML/text for a single send, independent of whether the body is layout-based
// or legacy chrome.
type bodyRenderInput struct {
	subject      string
	bodyHTML     string
	displayName  string
	senderName   string
	edReplyEmail string
	compliance   bool
}

// renderBody returns the HTML and text bodies for a send.
//
//   - LAYOUT path (isLayout true): the emitter already produced the full email
//     (the wrapper + blocks). Use bodyHTML verbatim as the HTML and derive
//     the text counterpart from it via stripHTML. The email_chrome envelope is
//     deliberately NOT applied — see the call sites and the COMPLIANCE note in
//     the package/PR for what the wrapper does and does not carry.
//   - LEGACY path (isLayout false): wrap bodyHTML in the email_chrome envelope
//     exactly as before, including the compliance footer when requested.
//
// In both paths the per-recipient %%…%% placeholders are left intact here and
// substituted later per recipient (substitutePlaceholders).
func (o *SendOrchestrator) renderBody(isLayout bool, in bodyRenderInput) (htmlBody, textBody string) {
	if isLayout {
		htmlBody = in.bodyHTML
		textBody = render.StripHTMLForText(in.bodyHTML)
		return htmlBody, textBody
	}

	chrome := render.Chrome{
		Subject:                 in.subject,
		BodyHTML:                in.bodyHTML,
		DisplayName:             in.displayName,
		IncludeComplianceFooter: in.compliance,
	}
	if in.compliance {
		chrome.EDName = fallbackString(in.senderName, "Executive Director")
		chrome.EDReplyEmail = in.edReplyEmail
		if o.unsub.Enabled() {
			chrome.UnsubscribeURL = UnsubscribeURLPlaceholder
		}
		// Preserve main's "My Newsletters" compliance-footer deep link on the
		// legacy chrome path (the send site now routes through renderBody). The
		// layout path returns its pre-rendered body_html and carries its own
		// footer, so this only applies to legacy (html-only) sends.
		if o.selfServeBaseURL != "" {
			chrome.MyNewslettersURL = o.selfServeBaseURL + myNewslettersPath
		}
	}
	return render.EmailHTML(chrome), render.EmailText(chrome)
}

// substitutePlaceholders binds the per-recipient runtime placeholders in a
// rendered body for a single recipient. isLayout gates the layout-only URL
// sentinels: a legacy body_html email substitutes ONLY the unsubscribe sentinel
// (the sole sentinel the legacy chrome emits), so a literal
// %%MANAGE_SUBSCRIPTIONS_URL%% / %%VIEW_ONLINE_URL%% an author put in body_html
// is never rewritten. The layout path substitutes all three.
//
//   - %%UNSUBSCRIBE_URL%%          → the recipient's signed unsubscribe link
//     (HTML-escaped; it lands in an href attribute).
//   - %%MANAGE_SUBSCRIPTIONS_URL%% → the Self-Serve "My Newsletters" archive
//     link (o.selfServeBaseURL + myNewslettersPath), HTML-escaped for its href.
//     This is a read-only archive, NEVER the one-click unsubscribe URL (that URL
//     opts out on GET). When no base URL is configured it resolves empty and the
//     wrapper renders the label without a link.
//   - %%VIEW_ONLINE_URL%%          → the hosted "view online" link. No hosted
//     web-version exists yet, so render-on-write leaves edition.view_online_link
//     empty and the wrapper's `if=` guard drops the View Online row at RENDER
//     time — the rendered body therefore never contains this sentinel, and the
//     substitution below is a harmless no-op kept for when the surface ships.
//
// When the unsubscribe service is not configured (Enabled() false) the
// render-on-write step renders the reply-based opt-out fallback copy instead
// of a link, so unsubURL stays empty; replacing any leftover sentinels with
// empty strings here is a defensive backstop so a misconfigured environment
// never ships visible sentinels to recipients.
func (o *SendOrchestrator) substitutePlaceholders(body, projectUID, email string, escapeHTML, isLayout bool) string {
	unsubURL := ""
	if o.unsub.Enabled() {
		unsubURL = o.unsub.BuildURL(projectUID, email)
	}
	// The unsubscribe sentinel lands in an href in the HTML part, so escape it
	// there; the plain-text part carries raw URLs (HTML entities would be wrong in
	// text/plain — see the textBody call sites).
	if escapeHTML {
		unsubURL = html.EscapeString(unsubURL)
	}
	if !isLayout {
		// Legacy chrome body: substitute ONLY the service-owned unsubscribe
		// sentinel. The layout-only URL sentinels are never emitted here, so
		// skipping them avoids rewriting a literal %%…%% an author placed in
		// body_html.
		return strings.NewReplacer(UnsubscribeURLPlaceholder, unsubURL).Replace(body)
	}
	// The "My Newsletters" archive link (read-only, never the unsubscribe URL —
	// see the function comment). Empty when no Self-Serve base URL is configured,
	// so the wrapper renders the label unlinked.
	manageURL := ""
	if o.selfServeBaseURL != "" {
		manageURL = o.selfServeBaseURL + myNewslettersPath
	}
	if escapeHTML {
		manageURL = html.EscapeString(manageURL)
	}
	replacer := strings.NewReplacer(
		UnsubscribeURLPlaceholder, unsubURL,
		ManageSubscriptionsURLPlaceholder, manageURL,
		ViewOnlineURLPlaceholder, "",
	)
	return replacer.Replace(body)
}

// substituteTestPlaceholders binds the per-recipient sentinels for a TEST
// send. A test send must not mint a real signed unsubscribe token: BodyHTML
// and ToEmail are both caller-supplied, so producing a valid opt-out link for
// an arbitrary address would let a writer harvest tokens. The unsubscribe and
// manage rows resolve to empty (the layout wrapper drops them), and view-online
// is empty as in the real path.
func (o *SendOrchestrator) substituteTestPlaceholders(body string) string {
	return strings.NewReplacer(
		UnsubscribeURLPlaceholder, "",
		ManageSubscriptionsURLPlaceholder, "",
		ViewOnlineURLPlaceholder, "",
	).Replace(body)
}

// substituteSendScope binds the send-scoped (not per-recipient) sentinels: the
// resolved sender display name and project name for the wrapper's compliance
// footer ("Sent by X on behalf of Y"). Runs once per send, before the
// per-recipient fan-out. Values are HTML-escaped: they land in already-compiled
// HTML, and the sender name (auth-service profile) and project name
// (project-service) are external data — the legacy chrome path escapes the
// same values.
func substituteSendScope(body, senderName, projectName, sendDate string, escapeHTML bool) string {
	sender := fallbackString(senderName, "Executive Director")
	project := projectName
	// Escape for the HTML part (values land in already-compiled HTML); the
	// plain-text part keeps them raw so a name like "R&D <Team>" is not turned
	// into HTML entities in text/plain. sendDate is a controlled format string
	// (formatSendDate) with no HTML-significant characters, so it is not escaped.
	if escapeHTML {
		sender = html.EscapeString(sender)
		project = html.EscapeString(project)
	}
	return strings.NewReplacer(
		SenderNamePlaceholder, sender,
		ProjectNamePlaceholder, project,
		SendDatePlaceholder, sendDate,
	).Replace(body)
}

// formatSendDate renders the header edition date ("January 2, 2006"). For a
// scheduled send it is the scheduled day; for an immediate send it is now. UTC
// keeps the date stable regardless of server timezone.
func formatSendDate(scheduledAt *time.Time) string {
	t := time.Now().UTC()
	if scheduledAt != nil {
		t = scheduledAt.UTC()
	}
	return t.Format("January 2, 2006")
}

func fallbackString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// redactEmail masks the local part of an email for safe logging.
func redactEmail(email string) string {
	at := strings.Index(email, "@")
	if at <= 0 {
		return "***"
	}
	return email[:1] + "***" + email[at:]
}
