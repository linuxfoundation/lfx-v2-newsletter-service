// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package service hosts the business logic for newsletter draft CRUD and send
// orchestration. The service layer depends only on the domain interfaces in
// internal/domain/port; concrete implementations are wired by cmd/newsletter-api.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service/render/declarative"
)

// recipientHashPattern matches the lowercase-hex SHA-256 token used in
// tracking URLs. Service-layer guard so RecordOpenWithHash never persists
// malformed values even if a future caller forgets to validate upstream.
var recipientHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

const (
	maxSubjectLength      = 200
	maxBodyHTMLLength     = 100_000
	maxCommitteesPerDraft = 50
)

// NewsletterService implements business logic for draft management.
type NewsletterService struct {
	repo port.NewsletterRepository
	// unsubEnabled mirrors UnsubscribeService.Enabled(). When false the
	// render-on-write step renders the reply-based opt-out fallback copy
	// (unsubFooterReplyFallback) instead of a link whose href would
	// substitute to an empty string at send time.
	unsubEnabled bool
}

// NewNewsletterService wires a NewsletterService over the given repository.
// unsubEnabled must reflect whether the unsubscribe service is configured
// (UnsubscribeService.Enabled()).
func NewNewsletterService(repo port.NewsletterRepository, unsubEnabled bool) *NewsletterService {
	return &NewsletterService{repo: repo, unsubEnabled: unsubEnabled}
}

// CreateDraftInput is the typed input for CreateDraft.
//
// BodyLayout is optional. When non-nil, the service renders it to HTML via the
// declarative emitter and stores both the layout and the derived BodyHTML; any
// BodyHTML supplied in the input is ignored. When nil, BodyHTML is taken as-is.
type CreateDraftInput struct {
	ProjectUID    string
	Subject       string
	BodyHTML      string
	BodyLayout    *declarative.Layout
	EDReplyEmail  string
	CommitteeUIDs []string
	CreatedBy     string
	// ScheduledAt is optional save-time intent — see validateScheduledAtSave.
	ScheduledAt *time.Time
}

// UpdateDraftInput is the typed input for UpdateDraft.
//
// BodyLayout is tri-state, carried by BodyLayoutSet: when BodyLayoutSet is
// false the request omitted body_layout (a layout newsletter keeps its stored
// layout); when true with a nil BodyLayout the request sent an explicit null
// (the layout is cleared and the newsletter becomes html-only); when true with
// a non-nil BodyLayout the layout is replaced and body_html re-derived.
type UpdateDraftInput struct {
	ID              uuid.UUID
	ExpectedVersion int64
	Subject         string
	BodyHTML        string
	BodyLayoutSet   bool
	BodyLayout      *declarative.Layout
	EDReplyEmail    string
	CommitteeUIDs   []string
	// ScheduledAt is full-replace like every other field here: nil clears a
	// previously-saved schedule.
	ScheduledAt *time.Time
}

// CreateDraft validates the input and inserts a new draft row.
func (s *NewsletterService) CreateDraft(ctx context.Context, in CreateDraftInput) (*model.Newsletter, error) {
	if err := validateProjectUID(in.ProjectUID); err != nil {
		return nil, err
	}
	if err := validateSubject(in.Subject); err != nil {
		return nil, err
	}
	if err := validateEDReplyEmail(in.EDReplyEmail); err != nil {
		return nil, err
	}
	if err := validateCommitteeUIDs(in.CommitteeUIDs); err != nil {
		return nil, err
	}
	if in.CreatedBy == "" {
		return nil, fmt.Errorf("%w: createdBy is required", domain.ErrInvalidRequest)
	}
	if err := validateScheduledAtSave(in.ScheduledAt); err != nil {
		return nil, err
	}

	// When a layout is supplied the emitter owns the whole email: body_html is
	// DERIVED from it and the request's body_html is ignored. Otherwise the
	// legacy body_html-only path applies and body_html must be valid as-is.
	bodyHTML := in.BodyHTML
	var bodyLayout json.RawMessage
	if in.BodyLayout != nil {
		html, raw, err := renderLayout(ctx, in.BodyLayout, in.Subject, in.EDReplyEmail, sendUnsubFooterMode(s.unsubEnabled), true)
		if err != nil {
			return nil, err
		}
		// Bound the DERIVED body_html to the same ceiling the legacy path enforces
		// — the layout input is only 1 MiB-capped, and MJML compilation expands it,
		// so an unbounded derived body could otherwise be persisted + emailed.
		if err := validateDerivedBodyHTML(html); err != nil {
			return nil, err
		}
		bodyHTML, bodyLayout = html, raw
	} else if err := validateBodyHTML(in.BodyHTML); err != nil {
		return nil, err
	}

	n := &model.Newsletter{
		ProjectUID:    in.ProjectUID,
		Subject:       strings.TrimSpace(in.Subject),
		BodyHTML:      bodyHTML,
		BodyLayout:    bodyLayout,
		EDReplyEmail:  strings.TrimSpace(in.EDReplyEmail),
		CommitteeUIDs: normalizeCommitteeUIDs(in.CommitteeUIDs),
		Status:        model.StatusDraft,
		CreatedBy:     in.CreatedBy,
		ScheduledAt:   in.ScheduledAt,
	}

	if err := s.repo.Create(ctx, n); err != nil {
		return nil, err
	}
	return n, nil
}

// GetNewsletter fetches a newsletter by id and verifies it belongs to the given
// project. A mismatch surfaces as ErrNotFound so the caller can't probe for
// other projects' newsletters.
func (s *NewsletterService) GetNewsletter(ctx context.Context, projectUID string, id uuid.UUID) (*model.Newsletter, error) {
	if err := validateProjectUID(projectUID); err != nil {
		return nil, err
	}
	n, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if n.ProjectUID != projectUID {
		return nil, domain.ErrNotFound
	}
	return n, nil
}

// GetNewsletterByID fetches a newsletter by id WITHOUT a project gate. Used
// internally by handlers that already enforce project ownership (e.g. the
// open-pixel handler, which validates project_uid from the URL against the
// stored newsletter).
func (s *NewsletterService) GetNewsletterByID(ctx context.Context, id uuid.UUID) (*model.Newsletter, error) {
	return s.repo.Get(ctx, id)
}

// ListNewslettersInput is the typed input for ListNewsletters.
type ListNewslettersInput struct {
	ProjectUID string
	Status     model.Status // optional; "" means both drafts and sent
	PageToken  string
}

// ListNewsletters returns a page of newsletters for the given project, ordered
// by most-recently-updated first.
//
// The public 'sent' filter also matches in-flight 'sending' rows so a
// newsletter whose fan-out is still running appears on the Sent tab instead of
// vanishing from both tabs while it settles.
func (s *NewsletterService) ListNewsletters(ctx context.Context, in ListNewslettersInput) (*port.ListPage, error) {
	if err := validateProjectUID(in.ProjectUID); err != nil {
		return nil, err
	}
	var statuses []model.Status
	switch in.Status {
	case "":
		// No filter — every state.
	case model.StatusDraft, model.StatusSending, model.StatusScheduled:
		statuses = []model.Status{in.Status}
	case model.StatusSent:
		statuses = []model.Status{model.StatusSent, model.StatusSending}
	default:
		return nil, fmt.Errorf("%w: status must be 'draft', 'sending', 'scheduled', or 'sent'", domain.ErrInvalidRequest)
	}
	return s.repo.ListAll(ctx, port.ListFilters{
		ProjectUID: in.ProjectUID,
		Statuses:   statuses,
		PageToken:  in.PageToken,
	})
}

// ListCommitteeNewslettersInput is the typed input for ListCommitteeNewsletters.
type ListCommitteeNewslettersInput struct {
	CommitteeUID string
	PageToken    string
}

// ListCommitteeNewsletters returns a page of sent newsletters whose audience
// includes the given committee, ordered by sent_at DESC. This is the
// member-facing read path: the gateway authorizes callers against
// `committee:{committee_uid}#member`, so only sent newsletters are ever
// returned — drafts and in-flight sends stay manager-only.
func (s *NewsletterService) ListCommitteeNewsletters(ctx context.Context, in ListCommitteeNewslettersInput) (*port.ListPage, error) {
	if strings.TrimSpace(in.CommitteeUID) == "" {
		return nil, fmt.Errorf("%w: committee_uid is required", domain.ErrInvalidRequest)
	}
	return s.repo.ListSentByCommittee(ctx, in.CommitteeUID, in.PageToken)
}

// Analytics returns aggregated engagement metrics for the given newsletter.
// Returns ErrNotFound if the newsletter doesn't exist or belongs to a different
// project than the one supplied.
func (s *NewsletterService) Analytics(ctx context.Context, projectUID string, id uuid.UUID) (*model.Analytics, error) {
	if err := validateProjectUID(projectUID); err != nil {
		return nil, err
	}
	n, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if n.ProjectUID != projectUID {
		return nil, domain.ErrNotFound
	}
	return s.repo.Analytics(ctx, id)
}

// RecordOpenWithHash records a single open event using an already-hashed
// recipient token (e.g. the hash carried in a tracking-pixel URL).
//
// Returns ErrNotFound only if the newsletter doesn't exist. An empty hash is
// treated as a silent no-op so a tracking pixel that lost its query string
// (e.g. via an email forward) still 200s with the GIF body instead of erroring.
func (s *NewsletterService) RecordOpenWithHash(ctx context.Context, newsletterID uuid.UUID, recipientHash string) error {
	// Verify the newsletter exists so we can return ErrNotFound for genuinely
	// bad IDs; we deliberately don't enforce status='sent' so test/preview
	// pipelines can light up the same tracking.
	if _, err := s.repo.Get(ctx, newsletterID); err != nil {
		return err
	}
	hash := strings.TrimSpace(recipientHash)
	if hash == "" {
		return nil
	}
	if !recipientHashPattern.MatchString(hash) {
		slog.WarnContext(ctx, "RecordOpenWithHash: discarding malformed recipient hash", "newsletter_id", newsletterID)
		return nil
	}
	return s.repo.RecordOpen(ctx, newsletterID, hash)
}

// HashRecipient lowercases and SHA-256-hashes an email address. Exposed so
// other layers (e.g. handler) can emit the same token-shape when constructing
// tracking pixel URLs.
func HashRecipient(email string) string {
	clean := strings.ToLower(strings.TrimSpace(email))
	if clean == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(clean))
	return hex.EncodeToString(sum[:])
}

// UpdateDraft mutates an existing draft, gated by optimistic locking and
// project ownership.
func (s *NewsletterService) UpdateDraft(ctx context.Context, projectUID string, in UpdateDraftInput) (*model.Newsletter, error) {
	if err := validateProjectUID(projectUID); err != nil {
		return nil, err
	}
	if err := validateSubject(in.Subject); err != nil {
		return nil, err
	}
	if err := validateEDReplyEmail(in.EDReplyEmail); err != nil {
		return nil, err
	}
	if err := validateCommitteeUIDs(in.CommitteeUIDs); err != nil {
		return nil, err
	}
	if err := validateScheduledAtSave(in.ScheduledAt); err != nil {
		return nil, err
	}

	existing, err := s.repo.Get(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if existing.ProjectUID != projectUID {
		return nil, domain.ErrNotFound
	}
	if existing.Status == model.StatusSent {
		return nil, domain.ErrAlreadySent
	}
	// Block edits while a send fan-out is running: a version bump mid-send
	// would otherwise race the terminal sending → sent transition (this is
	// exactly how the UI's autosave used to strand delivered newsletters in
	// draft).
	if existing.Status == model.StatusSending {
		return nil, domain.ErrSendInProgress
	}
	// A scheduled newsletter is already armed at SendGrid; it must be
	// cancelled back to draft (POST .../cancel-schedule) before it can be edited.
	if existing.Status == model.StatusScheduled {
		return nil, domain.ErrScheduled
	}

	// Resolve the body. A render failure returns before repo.Update, so nothing
	// is persisted.
	bodyHTML := in.BodyHTML
	var bodyLayout json.RawMessage
	switch {
	case in.BodyLayoutSet && in.BodyLayout != nil:
		// New / updated layout: the emitter owns the whole email; body_html is
		// DERIVED and the request's body_html is ignored.
		html, raw, renderErr := renderLayout(ctx, in.BodyLayout, in.Subject, in.EDReplyEmail, sendUnsubFooterMode(s.unsubEnabled), true)
		if renderErr != nil {
			return nil, renderErr
		}
		// Bound the DERIVED body_html to the same ceiling the legacy path
		// enforces, but as 422 (matching render-preview for the same output).
		if validateErr := validateDerivedBodyHTML(html); validateErr != nil {
			return nil, validateErr
		}
		bodyHTML, bodyLayout = html, raw
	case in.BodyLayoutSet:
		// EXPLICIT NULL: clear the stored layout and convert to an html-only
		// newsletter, taking body_html from the request. This is the documented
		// escape hatch back from the layout path (bodyLayout stays nil, which
		// the repository persists as SQL NULL).
		if validateErr := validateBodyHTML(in.BodyHTML); validateErr != nil {
			return nil, validateErr
		}
	case len(existing.BodyLayout) > 0:
		// The saved newsletter IS a layout newsletter and this update omits
		// body_layout — KEEP the stored layout rather than silently downgrading
		// to plain body_html (which would make the next send re-wrap the full
		// emitter document in email_chrome → a broken email). The request's
		// body_html is ignored on this branch; sending an explicit
		// "body_layout": null is the way to convert back to html-only.
		//
		// body_html is RE-DERIVED from the stored layout instead of copied:
		// render-on-write bakes request-scoped values (the reply-to mailto in
		// the wrapper footer) into the derived HTML, so an update that changes
		// ed_reply_email while omitting body_layout must not preserve a footer
		// pointing at the previous address.
		var stored declarative.Layout
		if err := json.Unmarshal(existing.BodyLayout, &stored); err != nil {
			return nil, fmt.Errorf("unmarshal stored body_layout: %w", err)
		}
		html, raw, renderErr := renderLayout(ctx, &stored, in.Subject, in.EDReplyEmail, sendUnsubFooterMode(s.unsubEnabled), true)
		if renderErr != nil {
			return nil, renderErr
		}
		if validateErr := validateDerivedBodyHTML(html); validateErr != nil {
			return nil, validateErr
		}
		bodyHTML, bodyLayout = html, raw
	default:
		// Legacy html-only newsletter: body_html must be valid as authored.
		if validateErr := validateBodyHTML(in.BodyHTML); validateErr != nil {
			return nil, validateErr
		}
	}

	existing.Subject = strings.TrimSpace(in.Subject)
	existing.BodyHTML = bodyHTML
	existing.BodyLayout = bodyLayout
	existing.EDReplyEmail = strings.TrimSpace(in.EDReplyEmail)
	existing.CommitteeUIDs = normalizeCommitteeUIDs(in.CommitteeUIDs)
	existing.ScheduledAt = in.ScheduledAt

	return s.repo.Update(ctx, existing, in.ExpectedVersion)
}

// DeleteDraft removes a draft by id, gated by project ownership. Drafts that
// are already sent cannot be deleted.
func (s *NewsletterService) DeleteDraft(ctx context.Context, projectUID string, id uuid.UUID) error {
	if err := validateProjectUID(projectUID); err != nil {
		return err
	}
	existing, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if existing.ProjectUID != projectUID {
		return domain.ErrNotFound
	}
	if existing.Status == model.StatusSent {
		return domain.ErrAlreadySent
	}
	if existing.Status == model.StatusSending {
		return domain.ErrSendInProgress
	}
	if existing.Status == model.StatusScheduled {
		return domain.ErrScheduled
	}
	return s.repo.Delete(ctx, id)
}

func validateProjectUID(projectUID string) error {
	if strings.TrimSpace(projectUID) == "" {
		return fmt.Errorf("%w: project_uid is required", domain.ErrInvalidRequest)
	}
	return nil
}

func validateSubject(subject string) error {
	trimmed := strings.TrimSpace(subject)
	if trimmed == "" {
		return fmt.Errorf("%w: subject is required", domain.ErrInvalidRequest)
	}
	if len(trimmed) > maxSubjectLength {
		return fmt.Errorf("%w: subject exceeds %d characters", domain.ErrInvalidRequest, maxSubjectLength)
	}
	return nil
}

// validateDerivedBodyHTML bounds the DERIVED (layout-rendered) body_html.
// Unlike validateBodyHTML this classifies an oversized document as
// ErrUnprocessable (422) with the same message render-preview uses: the
// request itself was well-formed and the request's body_html is ignored on
// the layout path, so a 400 blaming body_html would be misleading.
func validateDerivedBodyHTML(html string) error {
	if len(html) > maxBodyHTMLLength {
		return fmt.Errorf("%w: rendered HTML exceeds %d bytes", domain.ErrUnprocessable, maxBodyHTMLLength)
	}
	return nil
}

func validateBodyHTML(bodyHTML string) error {
	if strings.TrimSpace(bodyHTML) == "" {
		return fmt.Errorf("%w: body_html is required", domain.ErrInvalidRequest)
	}
	if len(bodyHTML) > maxBodyHTMLLength {
		return fmt.Errorf("%w: body_html exceeds %d characters", domain.ErrInvalidRequest, maxBodyHTMLLength)
	}
	return nil
}

func validateEDReplyEmail(email string) error {
	trimmed := strings.TrimSpace(email)
	if trimmed == "" {
		return fmt.Errorf("%w: ed_reply_email is required", domain.ErrInvalidRequest)
	}
	if _, err := mail.ParseAddress(trimmed); err != nil {
		return fmt.Errorf("%w: ed_reply_email is not a valid email: %v", domain.ErrInvalidRequest, err)
	}
	return nil
}

// validateScheduledAtSave applies the lenient save-time rule: only a future
// time is required. The full arm-time window (minimum lead, 72h horizon) is
// validated separately, at schedule time, by
// SendOrchestrator.validateScheduleWindow — an author drafting today for next
// week is legitimate; they simply cannot arm it yet.
func validateScheduledAtSave(scheduledAt *time.Time) error {
	if scheduledAt == nil {
		return nil
	}
	if !scheduledAt.After(time.Now().UTC()) {
		return fmt.Errorf("%w: scheduled_at must be in the future", domain.ErrInvalidRequest)
	}
	return nil
}

func validateCommitteeUIDs(uids []string) error {
	if len(uids) == 0 {
		return fmt.Errorf("%w: at least one committee_uid is required", domain.ErrInvalidRequest)
	}
	if len(uids) > maxCommitteesPerDraft {
		return fmt.Errorf("%w: at most %d committee_uids allowed", domain.ErrInvalidRequest, maxCommitteesPerDraft)
	}
	for _, u := range uids {
		if strings.TrimSpace(u) == "" {
			return fmt.Errorf("%w: committee_uids contains an empty value", domain.ErrInvalidRequest)
		}
	}
	return nil
}

// normalizeCommitteeUIDs trims surrounding whitespace from each UID and removes
// duplicates after normalization, preserving first-seen order. Empty values
// (post-trim) are dropped so " abc" and "abc" don't end up stored as distinct
// recipients and cause double upstream lookups.
func normalizeCommitteeUIDs(in []string) []string {
	if len(in) == 0 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

// renderLayout derives the email-safe body_html from a structured layout and
// returns it alongside the raw layout JSON to persist. The emitter owns the
// whole email (wrapper chrome + blocks), so the wrapper's runtime fields are
// bound here: write-time-known values (the reply email) bind directly,
// send-time-known values bind to PLACEHOLDER sentinels that the send path
// substitutes later.
//
// Fields bound EMPTY are dropped by the wrapper's `if=` guards at render time
// (rather than emitting a row whose href would substitute to empty at send):
//   - edition.date and edition.view_online_link: no newsletter-date field and
//     no hosted "view online" surface yet.
//   - edition.unsubscribe_url when the unsubscribe service is not configured:
//     the row is dropped instead of shipping a broken link.
//
// A render failure is surfaced as ErrUnprocessable (422), matching render-preview
// for the same unrenderable layout — the request itself is well-formed.
func renderLayout(ctx context.Context, layout *declarative.Layout, subject, replyEmail string, mode unsubFooterMode, manageEnabled bool) (bodyHTML string, raw json.RawMessage, err error) {
	html, err := renderLayoutBody(ctx, layout, LayoutWrapperContent(subject, replyEmail, mode, manageEnabled))
	if err != nil {
		return "", nil, err
	}

	raw, err = json.Marshal(layout)
	if err != nil {
		return "", nil, fmt.Errorf("%w: marshal body_layout: %v", domain.ErrUnprocessable, err)
	}
	return html, raw, nil
}

// renderLayoutBody selects a layout's template set by template_key, renders it
// against the supplied wrapper binding context, and maps failures to the domain
// sentinels by cause:
//
//   - Unknown template_key (a client-selected library the binary doesn't embed)
//     → 422.
//   - A CLIENT-driven render failure — unknown wrapper key or block_type, or
//     content the emitter cannot compile — is flagged by the emitter with
//     declarative.ErrUnrenderableLayout → 422.
//   - Anything else is a packaging/deployment defect: a present-but-broken
//     embedded library that fails to load or parse (the templates ship with the
//     binary) → untyped, so callers surface it as a 500. This keeps the
//     "present-but-broken library" 500 real instead of collapsing every render
//     failure to a client 422.
//
// It is the shared core behind render-on-write (renderLayout) and the stateless
// editor preview (NewsletterService.RenderPreview), so both classify identically.
// The two paths differ only in the wrapper content they pass: the write path uses
// the persisted reply email; the preview merges the client's wrapper_content over
// the send-path footer defaults.
func renderLayoutBody(ctx context.Context, layout *declarative.Layout, wrapperContent map[string]any) (string, error) {
	if err := validateNoReservedPlaceholders(layout.Blocks); err != nil {
		return "", err
	}
	// Normalize in place so the persisted layout (renderLayout marshals this
	// same pointer after we return) carries the same template_key this render
	// actually resolved against — otherwise a whitespace-padded key renders
	// successfully here but persists untrimmed, and can't match any catalog key
	// on a later reload.
	layout.TemplateKey = strings.TrimSpace(layout.TemplateKey)
	templates, err := declarative.LoadEmbeddedTemplateCached(layout.TemplateKey)
	if err != nil {
		if errors.Is(err, declarative.ErrTemplateNotFound) {
			return "", fmt.Errorf("%w: unknown template_key %q", domain.ErrUnprocessable, layout.TemplateKey)
		}
		return "", fmt.Errorf("load render templates: %w", err)
	}

	html, err := declarative.Render(ctx, *layout, templates, wrapperContent)
	if err != nil {
		if errors.Is(err, declarative.ErrUnrenderableLayout) {
			return "", fmt.Errorf("%w: render body_layout: %v", domain.ErrUnprocessable, err)
		}
		// Not attributable to the client layout — an embedded template that will
		// not parse is a packaging defect; stay untyped so it surfaces as a 500.
		return "", fmt.Errorf("render body_layout: %w", err)
	}
	return html, nil
}

// reservedPlaceholders are the send-time sentinel tokens substitutePlaceholders
// (send_orchestrator.go) swaps in verbatim, unconditionally, across the entire
// rendered body at send time. Only the service-owned wrapper content
// (LayoutWrapperContent) may legitimately carry one — writer-controlled block
// content never should, since bind.go's scheme gate lets a href/src value
// through unmodified when it exactly matches a sentinel, and richtext content
// renders verbatim with no scheme gate at all. A writer field (or richtext
// HTML) that happens to contain one of these tokens would otherwise have it
// silently swapped for the recipient's real per-recipient URL on send — e.g.
// an <img src> equal to %%UNSUBSCRIBE_URL%% auto-fires an unsubscribe when the
// recipient's mail client loads the image, with no recipient action.
var reservedPlaceholders = []string{
	UnsubscribeURLPlaceholder,
	ViewOnlineURLPlaceholder,
	ManageSubscriptionsURLPlaceholder,
}

// validateNoReservedPlaceholders rejects a layout whose block content —
// including richtext fields and each= array items (bind.go's lookupSlice
// resolves each= against []any elements holding map[string]any), which are
// stored in the same Content map as any other field — contains a reserved
// send-time sentinel token verbatim, at any nesting depth.
func validateNoReservedPlaceholders(blocks []declarative.Block) error {
	for _, b := range blocks {
		for field, v := range b.Content {
			if token, ok := findReservedPlaceholder(v); ok {
				return fmt.Errorf("%w: block_type %q field %q contains reserved placeholder token %q", domain.ErrInvalidRequest, b.BlockType, field, token)
			}
		}
		if err := validateNoReservedPlaceholders(b.Blocks); err != nil {
			return err
		}
	}
	return nil
}

// findReservedPlaceholder recursively inspects a Content value — a string,
// or a JSON-decoded []any / map[string]any produced by each= array items and
// nested object fields — for a reserved placeholder token.
func findReservedPlaceholder(v any) (string, bool) {
	switch val := v.(type) {
	case string:
		for _, token := range reservedPlaceholders {
			if strings.Contains(val, token) {
				return token, true
			}
		}
	case []any:
		for _, item := range val {
			if token, ok := findReservedPlaceholder(item); ok {
				return token, true
			}
		}
	case map[string]any:
		for _, item := range val {
			if token, ok := findReservedPlaceholder(item); ok {
				return token, true
			}
		}
	}
	return "", false
}

// RenderPreview renders a layout to email-safe HTML for the stateless editor
// preview. It runs the SAME template selection, render, and derived-output size
// policy as the create/update render-on-write path (renderLayoutBody +
// validateDerivedBodyHTML), so a preview and the eventual persisted render agree
// on which template_key resolves, which failures are 422 vs 500, and the output
// ceiling. clientWrapperContent is the caller's wrapper_content; it is merged
// over the send-path footer defaults (previewWrapperContent) so the preview's
// footer structure and byte size match the email that will be sent.
func (s *NewsletterService) RenderPreview(ctx context.Context, layout *declarative.Layout, clientWrapperContent map[string]any, unsubEnabled bool) (string, error) {
	html, err := renderLayoutBody(ctx, layout, previewWrapperContent(clientWrapperContent, sendUnsubFooterMode(unsubEnabled)))
	if err != nil {
		return "", err
	}
	if err := validateDerivedBodyHTML(html); err != nil {
		return "", err
	}
	return html, nil
}

// previewFooterSentinelKeys are the edition.* fields the send path substitutes
// per recipient (unsubscribe URL, sender/project name) or forces empty
// (manage_subscriptions_url — there is no preferences surface yet). A client's
// wrapper_content must NOT override these — the preview keeps the send-path
// values so its footer structure and byte size match the sent email (and it
// can't preview a working "Manage subscription" link that won't exist on send).
var previewFooterSentinelKeys = map[string]struct{}{
	"unsubscribe_url":          {},
	"unsubscribe_fallback":     {},
	"sender_name":              {},
	"project_name":             {},
	"manage_subscriptions_url": {},
}

// previewWrapperContent merges a client-supplied wrapper_content over the
// send-path footer defaults for the stateless render-preview.
//
// It starts from LayoutWrapperContent (the send-path defaults: footer sentinels
// plus reply_email taken from the client when supplied) and overlays the
// client's edition.* bindings on top, so client-owned fields like edition.date
// and edition.view_online_link are honored while the footer sentinels
// (unsubscribe / sender / project) and the already-applied reply_email are
// preserved. Only the edition sub-map is deep-merged; that is the only shape the
// wrapper contract defines.
func previewWrapperContent(clientWC map[string]any, mode unsubFooterMode) map[string]any {
	base := LayoutWrapperContent("", replyEmailFromWrapperContent(clientWC), mode, true)

	clientEdition, ok := clientWC["edition"].(map[string]any)
	if !ok {
		return base
	}
	baseEdition, ok := base["edition"].(map[string]any)
	if !ok {
		return base
	}
	for k, v := range clientEdition {
		if _, sentinel := previewFooterSentinelKeys[k]; sentinel {
			// Footer sentinel: keep the send-path value so preview size matches.
			continue
		}
		if k == "reply_email" {
			// Already applied via LayoutWrapperContent (trimmed); don't re-overlay.
			continue
		}
		baseEdition[k] = v
	}
	return base
}

// replyEmailFromWrapperContent best-effort extracts edition.reply_email from a
// client-supplied wrapper_content map (shape: {"edition": {"reply_email": ...}}).
// render-preview is stateless, so this is the only place the reply-to address
// can come from; anything missing or mis-typed yields "" and the preview simply
// omits the reply row (the wrapper's if= guard drops it).
func replyEmailFromWrapperContent(wc map[string]any) string {
	edition, ok := wc["edition"].(map[string]any)
	if !ok {
		return ""
	}
	replyEmail, _ := edition["reply_email"].(string)
	return replyEmail
}

// unsubFooterMode selects how a layout wrapper's opt-out row renders.
type unsubFooterMode int

const (
	// unsubFooterLink: a working per-recipient unsubscribe link is available
	// (the unsubscribe service is enabled). The wrapper renders the link.
	unsubFooterLink unsubFooterMode = iota
	// unsubFooterReplyFallback: no unsubscribe link can be minted (the service
	// is disabled/misconfigured), but this is a REAL send, so the email must
	// still carry an opt-out mechanism. The wrapper renders reply-based copy,
	// matching the legacy chrome path's fallback (see email_chrome.go).
	unsubFooterReplyFallback
	// unsubFooterSuppressed: omit the opt-out row entirely. Used only by test
	// sends, which mint no real token and are not commercial mailings.
	unsubFooterSuppressed
)

// unsubscribeReplyFallbackText is the reply-based opt-out copy used when no
// unsubscribe link can be minted for a real send. It mirrors the legacy chrome
// footer's fallback and mints no token, so it is compliant without the
// unsubscribe service configured.
const unsubscribeReplyFallbackText = "To unsubscribe, reply with UNSUBSCRIBE."

// sendUnsubFooterMode maps the deployment's unsubscribe-enabled flag to the
// footer mode for a REAL send or preview: a link when enabled, the reply-based
// fallback when not. A real send is never suppressed — it always needs an
// opt-out mechanism (only test sends suppress).
func sendUnsubFooterMode(unsubEnabled bool) unsubFooterMode {
	if unsubEnabled {
		return unsubFooterLink
	}
	return unsubFooterReplyFallback
}

// LayoutWrapperContent builds the wrapper template's runtime binding context for
// the layout render path. Send-time-known values bind to PLACEHOLDER sentinels
// that the send path substitutes per recipient (unsubscribe URL, sender name,
// project name); write-time-known values (the reply email) bind directly.
// Fields left empty are dropped by the wrapper's `if=` guards at render time,
// so a compiled body only carries the rows that will actually resolve. The
// opt-out row is driven by mode: a real unsubscribe link (sentinel), the
// reply-based fallback copy, or nothing (test sends) — the wrapper renders
// exactly one via mutually-exclusive `if=` guards on unsubscribe_url and
// unsubscribe_fallback.
//
// It is the single source of truth for that context so every layout-render
// caller — render-on-write (renderLayout) and the stateless render-preview
// handler — produces the SAME footer structure, and a preview's byte size
// therefore matches the email that will be sent.
func LayoutWrapperContent(subject, replyEmail string, mode unsubFooterMode, manageEnabled bool) map[string]any {
	unsubURL := ""
	unsubFallback := ""
	switch mode {
	case unsubFooterLink:
		unsubURL = UnsubscribeURLPlaceholder
	case unsubFooterReplyFallback:
		unsubFallback = unsubscribeReplyFallbackText
	case unsubFooterSuppressed:
		// Both empty: the opt-out row is dropped entirely.
	}
	// The "My Newsletters" archive link is a send-time-known value: its URL is
	// the orchestrator's Self-Serve base URL, so it binds to a sentinel the send
	// path substitutes. When My Newsletters is unavailable (no base URL bound),
	// the field stays empty and the wrapper renders the label without a link.
	manageURL := ""
	if manageEnabled {
		manageURL = ManageSubscriptionsURLPlaceholder
	}
	return map[string]any{
		"edition": map[string]any{
			"date":                     "",
			"title":                    strings.TrimSpace(subject),
			"view_online_link":         "",
			"unsubscribe_url":          unsubURL,
			"unsubscribe_fallback":     unsubFallback,
			"manage_subscriptions_url": manageURL,
			"sender_name":              SenderNamePlaceholder,
			"project_name":             ProjectNamePlaceholder,
			"reply_email":              strings.TrimSpace(replyEmail),
		},
	}
}

// IsValidationError reports whether err is a validation/domain ErrInvalidRequest.
func IsValidationError(err error) bool {
	return errors.Is(err, domain.ErrInvalidRequest)
}

// IsUnprocessableError reports whether err is a domain ErrUnprocessable (422) —
// e.g. a well-formed request whose body_layout could not be rendered.
func IsUnprocessableError(err error) bool {
	return errors.Is(err, domain.ErrUnprocessable)
}
