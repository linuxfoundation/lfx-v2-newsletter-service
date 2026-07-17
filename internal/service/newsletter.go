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
	// render-on-write step binds an empty unsubscribe URL so the wrapper's
	// `if=` guard drops the opt-out row entirely, instead of rendering a row
	// whose href would substitute to an empty string at send time.
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

	// When a layout is supplied the emitter owns the whole email: body_html is
	// DERIVED from it and the request's body_html is ignored. Otherwise the
	// legacy body_html-only path applies and body_html must be valid as-is.
	bodyHTML := in.BodyHTML
	var bodyLayout json.RawMessage
	if in.BodyLayout != nil {
		html, raw, err := renderLayout(ctx, in.BodyLayout, in.EDReplyEmail, s.unsubEnabled)
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
	case model.StatusDraft, model.StatusSending:
		statuses = []model.Status{in.Status}
	case model.StatusSent:
		statuses = []model.Status{model.StatusSent, model.StatusSending}
	default:
		return nil, fmt.Errorf("%w: status must be 'draft', 'sending', or 'sent'", domain.ErrInvalidRequest)
	}
	return s.repo.ListAll(ctx, port.ListFilters{
		ProjectUID: in.ProjectUID,
		Statuses:   statuses,
		PageToken:  in.PageToken,
	})
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

	// Resolve the body. A render failure returns before repo.Update, so nothing
	// is persisted.
	bodyHTML := in.BodyHTML
	var bodyLayout json.RawMessage
	switch {
	case in.BodyLayoutSet && in.BodyLayout != nil:
		// New / updated layout: the emitter owns the whole email; body_html is
		// DERIVED and the request's body_html is ignored.
		html, raw, renderErr := renderLayout(ctx, in.BodyLayout, in.EDReplyEmail, s.unsubEnabled)
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
		html, raw, renderErr := renderLayout(ctx, &stored, in.EDReplyEmail, s.unsubEnabled)
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
//   - edition.manage_subscriptions_url: no preferences surface exists yet, and
//     aliasing it to the one-click unsubscribe URL would silently opt out a
//     recipient who only wanted to manage preferences.
//   - edition.unsubscribe_url when the unsubscribe service is not configured:
//     the row is dropped instead of shipping a broken link.
//
// A render failure is surfaced as ErrUnprocessable (422), matching render-preview
// for the same unrenderable layout — the request itself is well-formed.
func renderLayout(ctx context.Context, layout *declarative.Layout, replyEmail string, unsubEnabled bool) (bodyHTML string, raw json.RawMessage, err error) {
	templates, err := declarative.LoadEmbeddedTemplateCached(layout.TemplateKey)
	if err != nil {
		if errors.Is(err, declarative.ErrTemplateNotFound) {
			// A client-selected library that isn't embedded makes the stored layout
			// unrenderable (422), consistent with an unknown block_type.
			return "", nil, fmt.Errorf("%w: unknown template_key %q", domain.ErrUnprocessable, strings.TrimSpace(layout.TemplateKey))
		}
		// A present library that failed to parse is a deployment defect (templates
		// ship with the binary), not a client error — bubble it up untyped so it
		// maps to 500.
		return "", nil, fmt.Errorf("load render templates: %w", err)
	}

	unsubURL := ""
	if unsubEnabled {
		unsubURL = UnsubscribeURLPlaceholder
	}
	wrapperContent := map[string]any{
		"edition": map[string]any{
			"date":                     "",
			"view_online_link":         "",
			"unsubscribe_url":          unsubURL,
			"manage_subscriptions_url": "",
			"sender_name":              SenderNamePlaceholder,
			"project_name":             ProjectNamePlaceholder,
			"reply_email":              strings.TrimSpace(replyEmail),
		},
	}

	html, err := declarative.Render(ctx, *layout, templates, wrapperContent)
	if err != nil {
		return "", nil, fmt.Errorf("%w: render body_layout: %v", domain.ErrUnprocessable, err)
	}

	raw, err = json.Marshal(layout)
	if err != nil {
		return "", nil, fmt.Errorf("%w: marshal body_layout: %v", domain.ErrUnprocessable, err)
	}
	return html, raw, nil
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
