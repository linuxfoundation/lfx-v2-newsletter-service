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
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // guarantee IANA tzdata is embedded so time.LoadLocation works without an OS tzdata package

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// recipientHashPattern matches the lowercase-hex SHA-256 token used in
// tracking URLs. Service-layer guard so RecordOpenWithHash never persists
// malformed values even if a future caller forgets to validate upstream.
var recipientHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// targetLocalPattern matches a 24-hour HH:MM wall-clock time, e.g. "09:00".
var targetLocalPattern = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

const (
	maxSubjectLength      = 200
	maxBodyHTMLLength     = 100_000
	maxCommitteesPerDraft = 50
)

// NewsletterService implements business logic for draft management.
type NewsletterService struct {
	repo   port.NewsletterRepository
	tzRepo port.RecipientTimezoneRepository
}

// NewNewsletterService wires a NewsletterService over the given repositories.
// tzRepo may be the same concrete value as repo — PostgresNewsletterRepo
// implements both interfaces — or nil in tests that don't exercise recipient
// timezone capture.
func NewNewsletterService(repo port.NewsletterRepository, tzRepo port.RecipientTimezoneRepository) *NewsletterService {
	return &NewsletterService{repo: repo, tzRepo: tzRepo}
}

// CreateDraftInput is the typed input for CreateDraft.
type CreateDraftInput struct {
	ProjectUID    string
	Subject       string
	BodyHTML      string
	EDReplyEmail  string
	CommitteeUIDs []string
	CreatedBy     string
	// ScheduledAt is optional save-time intent — see validateScheduledAtSave.
	ScheduledAt *time.Time
	// DeliveryStrategy is "global" or "tz_local" (LFXV2-2506). Empty defaults
	// to "global".
	DeliveryStrategy string
	// TargetLocal is the HH:MM release time on the schedule date, required
	// when DeliveryStrategy is "tz_local".
	TargetLocal *string
	// DefaultTimezone is the IANA fallback timezone, required when
	// DeliveryStrategy is "tz_local".
	DefaultTimezone *string
}

// UpdateDraftInput is the typed input for UpdateDraft.
type UpdateDraftInput struct {
	ID              uuid.UUID
	ExpectedVersion int64
	Subject         string
	BodyHTML        string
	EDReplyEmail    string
	CommitteeUIDs   []string
	// ScheduledAt is full-replace like every other field here: nil clears a
	// previously-saved schedule.
	ScheduledAt *time.Time
	// DeliveryStrategy, TargetLocal, and DefaultTimezone are full-replace,
	// like ScheduledAt — see CreateDraftInput.
	DeliveryStrategy string
	TargetLocal      *string
	DefaultTimezone  *string
}

// CreateDraft validates the input and inserts a new draft row.
func (s *NewsletterService) CreateDraft(ctx context.Context, in CreateDraftInput) (*model.Newsletter, error) {
	if err := validateProjectUID(in.ProjectUID); err != nil {
		return nil, err
	}
	if err := validateSubject(in.Subject); err != nil {
		return nil, err
	}
	if err := validateBodyHTML(in.BodyHTML); err != nil {
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
	deliveryStrategy, err := validateDeliveryStrategy(in.DeliveryStrategy)
	if err != nil {
		return nil, err
	}
	if err := validateTZLocalFields(deliveryStrategy, in.TargetLocal, in.DefaultTimezone); err != nil {
		return nil, err
	}

	n := &model.Newsletter{
		ProjectUID:    in.ProjectUID,
		Subject:       strings.TrimSpace(in.Subject),
		BodyHTML:      in.BodyHTML,
		EDReplyEmail:  strings.TrimSpace(in.EDReplyEmail),
		CommitteeUIDs: normalizeCommitteeUIDs(in.CommitteeUIDs),
		Status:        model.StatusDraft,
		CreatedBy:     in.CreatedBy,
		ScheduledAt:   in.ScheduledAt,
		// deliveryStrategy is explicitly defaulted to "global" by
		// validateDeliveryStrategy rather than relying on the schema column
		// default: bun's struct-model insert sends Go's zero value ("") for
		// an unset string field, which would violate the CHECK constraint
		// instead of falling back to the DB default.
		DeliveryStrategy: deliveryStrategy,
		TargetLocal:      in.TargetLocal,
		DefaultTimezone:  in.DefaultTimezone,
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
	if err := validateBodyHTML(in.BodyHTML); err != nil {
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
	deliveryStrategy, err := validateDeliveryStrategy(in.DeliveryStrategy)
	if err != nil {
		return nil, err
	}
	if err := validateTZLocalFields(deliveryStrategy, in.TargetLocal, in.DefaultTimezone); err != nil {
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

	existing.Subject = strings.TrimSpace(in.Subject)
	existing.BodyHTML = in.BodyHTML
	existing.EDReplyEmail = strings.TrimSpace(in.EDReplyEmail)
	existing.CommitteeUIDs = normalizeCommitteeUIDs(in.CommitteeUIDs)
	existing.ScheduledAt = in.ScheduledAt
	existing.DeliveryStrategy = deliveryStrategy
	existing.TargetLocal = in.TargetLocal
	existing.DefaultTimezone = in.DefaultTimezone

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

// CaptureRecipientTimezoneInput is the typed input for CaptureRecipientTimezone.
type CaptureRecipientTimezoneInput struct {
	ProjectUID string
	Email      string
	Timezone   string
	Source     string
}

// CaptureRecipientTimezone records or overwrites the timezone on file for a
// recipient, scoped to (project_uid, email) (LFXV2-2506, Option B). It is
// independent of any single newsletter — the timezone is a property of the
// person within a project, consumed later by any tz_local send's fan-out.
func (s *NewsletterService) CaptureRecipientTimezone(ctx context.Context, in CaptureRecipientTimezoneInput) error {
	if s.tzRepo == nil {
		return fmt.Errorf("%w: recipient timezone capture is not configured", domain.ErrInvalidRequest)
	}
	if err := validateProjectUID(in.ProjectUID); err != nil {
		return err
	}
	trimmedEmail := strings.ToLower(strings.TrimSpace(in.Email))
	if trimmedEmail == "" {
		return fmt.Errorf("%w: email is required", domain.ErrInvalidRequest)
	}
	if _, err := mail.ParseAddress(trimmedEmail); err != nil {
		return fmt.Errorf("%w: email is not a valid email: %v", domain.ErrInvalidRequest, err)
	}
	if err := validateIANATimezone(in.Timezone); err != nil {
		return fmt.Errorf("%w: timezone %v", domain.ErrInvalidRequest, err)
	}
	source := strings.TrimSpace(in.Source)
	switch source {
	case model.RecipientTimezoneSourceEnrichment, model.RecipientTimezoneSourceIPGeolocation, model.RecipientTimezoneSourceBrowser:
		// valid
	default:
		return fmt.Errorf("%w: source must be 'enrichment', 'ip_geolocation', or 'browser'", domain.ErrInvalidRequest)
	}
	return s.tzRepo.UpsertRecipientTimezone(ctx, in.ProjectUID, trimmedEmail, strings.TrimSpace(in.Timezone), source)
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

// validateDeliveryStrategy normalizes and validates the delivery_strategy
// input, returning the value to persist. An empty string defaults to
// "global" — callers must use the returned value, not the raw input, when
// constructing/updating a model.Newsletter (see the comment in CreateDraft).
func validateDeliveryStrategy(strategy string) (string, error) {
	trimmed := strings.TrimSpace(strategy)
	if trimmed == "" {
		return model.DeliveryStrategyGlobal, nil
	}
	switch trimmed {
	case model.DeliveryStrategyGlobal, model.DeliveryStrategyTZLocal:
		return trimmed, nil
	default:
		return "", fmt.Errorf("%w: delivery_strategy must be 'global' or 'tz_local'", domain.ErrInvalidRequest)
	}
}

// validateTZLocalFields mirrors the schema CHECK constraint
// newsletters_tz_local_requires_fields: tz_local requires both TargetLocal
// and DefaultTimezone; global requires neither be set.
func validateTZLocalFields(deliveryStrategy string, targetLocal, defaultTimezone *string) error {
	if deliveryStrategy == model.DeliveryStrategyTZLocal {
		if targetLocal == nil || strings.TrimSpace(*targetLocal) == "" {
			return fmt.Errorf("%w: target_local is required when delivery_strategy is 'tz_local'", domain.ErrInvalidRequest)
		}
		if !targetLocalPattern.MatchString(strings.TrimSpace(*targetLocal)) {
			return fmt.Errorf("%w: target_local must be in HH:MM 24-hour format", domain.ErrInvalidRequest)
		}
		if defaultTimezone == nil || strings.TrimSpace(*defaultTimezone) == "" {
			return fmt.Errorf("%w: default_timezone is required when delivery_strategy is 'tz_local'", domain.ErrInvalidRequest)
		}
		if err := validateIANATimezone(*defaultTimezone); err != nil {
			return fmt.Errorf("%w: default_timezone %v", domain.ErrInvalidRequest, err)
		}
		return nil
	}
	// global: neither field applies. Reject rather than silently discard, so
	// a caller who mistakenly sets them on a global draft finds out at save
	// time instead of losing data silently.
	if targetLocal != nil && strings.TrimSpace(*targetLocal) != "" {
		return fmt.Errorf("%w: target_local is only valid when delivery_strategy is 'tz_local'", domain.ErrInvalidRequest)
	}
	if defaultTimezone != nil && strings.TrimSpace(*defaultTimezone) != "" {
		return fmt.Errorf("%w: default_timezone is only valid when delivery_strategy is 'tz_local'", domain.ErrInvalidRequest)
	}
	return nil
}

// validateIANATimezone confirms tz is loadable via the stdlib tzdata
// database. Used for both a newsletter's default_timezone and a captured
// recipient timezone, so both reject the same way at input time rather than
// failing later inside the fan-out's clamp computation.
func validateIANATimezone(tz string) error {
	trimmed := strings.TrimSpace(tz)
	if trimmed == "" {
		return errors.New("must not be empty")
	}
	if strings.EqualFold(trimmed, "Local") {
		// time.LoadLocation("Local") resolves to the host's local timezone,
		// which is meaningless for a server-side scheduling decision.
		return errors.New("must be a specific IANA timezone, not 'Local'")
	}
	if _, err := time.LoadLocation(trimmed); err != nil {
		return fmt.Errorf("is not a valid IANA timezone: %w", err)
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

// IsValidationError reports whether err is a validation/domain ErrInvalidRequest.
func IsValidationError(err error) bool {
	return errors.Is(err, domain.ErrInvalidRequest)
}
