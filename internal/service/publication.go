// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service/render/declarative"
)

const (
	maxPublicationNameLength = 200
	maxFromNameLength        = 100
	// maxFromNameBytes matches email-service's FromDisplayName byte limit, so a
	// saved name cannot be refused at send.
	maxFromNameBytes = 256
	// maxEmailLength is the practical address limit from RFC 5321 (forward-path).
	maxEmailLength = 254
)

// PublicationService implements publication CRUD and the read of a
// publication's editions.
type PublicationService struct {
	repo                  port.PublicationRepository
	newsletter            port.NewsletterRepository
	replyToAllowedDomains []string
}

// NewPublicationService wires a PublicationService over the given repositories.
// replyToAllowedDomains is the deployment's Reply-To allowlist (empty uses the
// same default as the send path); a publication's reply_to_email must be on it.
func NewPublicationService(repo port.PublicationRepository, newsletter port.NewsletterRepository, replyToAllowedDomains []string) *PublicationService {
	return &PublicationService{repo: repo, newsletter: newsletter, replyToAllowedDomains: normalizeReplyToDomains(replyToAllowedDomains)}
}

// PublicationInput is the editable part of a publication, shared by create and
// the full-replace update. Blank optional fields clear the value.
type PublicationInput struct {
	Name          string
	TemplateKey   *string
	FromName      *string
	ReplyToEmail  *string
	CommitteeUIDs []string
}

// CreatePublication validates the input and inserts a named publication.
func (s *PublicationService) CreatePublication(ctx context.Context, projectUID, createdBy string, in PublicationInput) (*model.Publication, error) {
	if err := validateProjectUID(projectUID); err != nil {
		return nil, err
	}
	if createdBy == "" {
		return nil, fmt.Errorf("%w: createdBy is required", domain.ErrInvalidRequest)
	}
	p := &model.Publication{ProjectUID: projectUID, CreatedBy: createdBy}
	if err := s.applyPublicationInput(p, in); err != nil {
		return nil, err
	}
	if err := s.repo.CreatePublication(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// GetPublication fetches one publication within a project.
func (s *PublicationService) GetPublication(ctx context.Context, projectUID string, id uuid.UUID) (*model.Publication, error) {
	if err := validateProjectUID(projectUID); err != nil {
		return nil, err
	}
	return s.repo.GetPublication(ctx, projectUID, id)
}

// ListPublications returns a page of the project's named publications.
func (s *PublicationService) ListPublications(ctx context.Context, projectUID, pageToken string) (*port.PublicationListPage, error) {
	if err := validateProjectUID(projectUID); err != nil {
		return nil, err
	}
	return s.repo.ListPublications(ctx, projectUID, pageToken)
}

// UpdatePublication full-replaces the editable fields of a publication, gated
// by optimistic locking. Changing the template set or the default audience
// affects new editions only. Changing from_name or reply_to_email also applies
// to unsent editions, because the send path reads them from the publication.
// An implicit publication is not writable: its editions must keep sending as a
// newsletter with no publication does.
func (s *PublicationService) UpdatePublication(ctx context.Context, projectUID string, id uuid.UUID, expectedVersion int64, in PublicationInput) (*model.Publication, error) {
	if err := validateProjectUID(projectUID); err != nil {
		return nil, err
	}
	p, err := s.repo.GetPublication(ctx, projectUID, id)
	if err != nil {
		return nil, err
	}
	if p.Implicit {
		return nil, fmt.Errorf("%w: an implicit publication cannot be updated", domain.ErrInvalidRequest)
	}
	if err := s.applyPublicationInput(p, in); err != nil {
		return nil, err
	}
	return s.repo.UpdatePublication(ctx, p, expectedVersion)
}

// ListEditionsInput is the typed input for ListEditions.
type ListEditionsInput struct {
	ProjectUID     string
	PublicationUID uuid.UUID
	Status         model.Status // optional; same semantics as ListNewsletters
	PageToken      string
}

// ListEditions returns a page of a publication's editions, newest first. It
// checks the publication exists in the project so a publication id that doesn't
// belong to it reads as 404, not as an empty list.
func (s *PublicationService) ListEditions(ctx context.Context, in ListEditionsInput) (*port.ListPage, error) {
	if _, err := s.GetPublication(ctx, in.ProjectUID, in.PublicationUID); err != nil {
		return nil, err
	}
	statuses, err := listStatuses(in.Status)
	if err != nil {
		return nil, err
	}
	return s.newsletter.ListAll(ctx, port.ListFilters{
		ProjectUID:     in.ProjectUID,
		PublicationUID: &in.PublicationUID,
		Statuses:       statuses,
		PageToken:      in.PageToken,
	})
}

// applyPublicationInput validates in and copies it onto p.
func (s *PublicationService) applyPublicationInput(p *model.Publication, in PublicationInput) error {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return fmt.Errorf("%w: name is required", domain.ErrInvalidRequest)
	}
	if utf8.RuneCountInString(name) > maxPublicationNameLength {
		return fmt.Errorf("%w: name exceeds %d characters", domain.ErrInvalidRequest, maxPublicationNameLength)
	}
	templateKey, err := validateTemplateKey(in.TemplateKey)
	if err != nil {
		return err
	}
	fromName, err := validateFromName(in.FromName)
	if err != nil {
		return err
	}
	replyTo, err := validateReplyToEmail(in.ReplyToEmail, s.replyToAllowedDomains)
	if err != nil {
		return err
	}
	// A publication may have no default audience, so unlike a draft an empty
	// list is allowed here.
	committees := []string{}
	if len(in.CommitteeUIDs) > 0 {
		if err := validateCommitteeUIDs(in.CommitteeUIDs); err != nil {
			return err
		}
		committees = normalizeCommitteeUIDs(in.CommitteeUIDs)
	}

	p.Name = name
	p.TemplateKey = templateKey
	p.FromName = fromName
	p.ReplyToEmail = replyTo
	p.CommitteeUIDs = committees
	return nil
}

// trimOptional trims an optional field, mapping blank to nil.
func trimOptional(v *string) *string {
	if v == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*v)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// validateTemplateKey accepts only a template set compiled into the binary.
func validateTemplateKey(key *string) (*string, error) {
	trimmed := trimOptional(key)
	if trimmed == nil {
		return nil, nil
	}
	keys, err := declarative.EmbeddedTemplateKeys()
	if err != nil {
		return nil, fmt.Errorf("list template keys: %w", err)
	}
	if !slices.Contains(keys, *trimmed) {
		return nil, fmt.Errorf("%w: unknown template_key %q", domain.ErrInvalidRequest, *trimmed)
	}
	return trimmed, nil
}

// validateFromName bounds the From display name and refuses control, format
// (bidi overrides, zero-width) and line or paragraph separator characters,
// because the value ends up in a mail header that every recipient sees.
func validateFromName(name *string) (*string, error) {
	trimmed := trimOptional(name)
	if trimmed == nil {
		return nil, nil
	}
	if utf8.RuneCountInString(*trimmed) > maxFromNameLength || len(*trimmed) > maxFromNameBytes {
		return nil, fmt.Errorf("%w: from_name exceeds %d characters", domain.ErrInvalidRequest, maxFromNameLength)
	}
	if strings.IndexFunc(*trimmed, isUnsafeHeaderRune) >= 0 {
		return nil, fmt.Errorf("%w: from_name must not contain control or invisible characters", domain.ErrInvalidRequest)
	}
	return trimmed, nil
}

// isUnsafeHeaderRune reports control characters (Cc), format characters (Cf)
// and line or paragraph separators (Zl, Zp).
func isUnsafeHeaderRune(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
}

// validateReplyToEmail requires a bare RFC 5322 address (no display name) on an
// allowed domain. The domain is checked here so a misconfigured publication
// fails when someone saves it, not at send, where the address could be refused
// and fail the whole fan-out.
func validateReplyToEmail(email *string, allowedDomains []string) (*string, error) {
	trimmed := trimOptional(email)
	if trimmed == nil {
		return nil, nil
	}
	if len(*trimmed) > maxEmailLength {
		return nil, fmt.Errorf("%w: reply_to_email exceeds %d characters", domain.ErrInvalidRequest, maxEmailLength)
	}
	parsed, err := mail.ParseAddress(*trimmed)
	if err != nil || parsed.Address != *trimmed {
		return nil, fmt.Errorf("%w: reply_to_email is not a valid email address", domain.ErrInvalidRequest)
	}
	// Match the address rules email-service applies at send, so a reply-to it
	// would refuse can't be saved and then fail every send from the publication.
	if !emailServiceAcceptsAddress(*trimmed) {
		return nil, fmt.Errorf("%w: reply_to_email must be a plain ASCII address with at most %d characters before the @", domain.ErrInvalidRequest, maxEmailLocalPartLength)
	}
	if !domainAllowed(*trimmed, allowedDomains) {
		return nil, fmt.Errorf("%w: reply_to_email domain %q is not an allowed reply-to domain", domain.ErrInvalidRequest, domainOf(*trimmed))
	}
	return trimmed, nil
}

// maxEmailLocalPartLength is email-service's limit on the part of an address
// before the "@" (api.MaxAddressLocalPartLength in lfx-v2-email-service).
const maxEmailLocalPartLength = 64

// emailServiceAcceptsAddress mirrors email-service's checks on a bare address:
// ASCII only, a local part of at most maxEmailLocalPartLength bytes, and no
// local part that would need RFC 5322 quoting.
func emailServiceAcceptsAddress(addr string) bool {
	for i := 0; i < len(addr); i++ {
		if addr[i] >= utf8.RuneSelf {
			return false
		}
	}
	at := strings.LastIndex(addr, "@")
	if at <= 0 || at > maxEmailLocalPartLength || at == len(addr)-1 {
		return false
	}
	return (&mail.Address{Address: addr}).String() == "<"+addr+">"
}

// publicationFor fetches the publication an edition is created under.
func (s *NewsletterService) publicationFor(ctx context.Context, projectUID string, id uuid.UUID) (*model.Publication, error) {
	if s.publications == nil {
		return nil, fmt.Errorf("publications are not configured")
	}
	return s.publications.GetPublication(ctx, projectUID, id)
}

// applyPublicationDefaults fills in what a new edition inherits from its
// publication: audience and reply-to when omitted, and the template set. The
// copy is deliberate: an already-created edition keeps these through a later
// edit to the publication, and a binary that knows nothing about publications
// can still send it. An implicit publication belongs to one newsletter and
// cannot take further editions.
func applyPublicationDefaults(in *CreateDraftInput, pub *model.Publication) error {
	if pub.Implicit {
		return fmt.Errorf("%w: an implicit publication cannot take new editions", domain.ErrInvalidRequest)
	}
	// Only an omitted audience inherits. An explicit empty list is left for the
	// empty-audience check to refuse, so clearing a selection can't widen it.
	if in.CommitteeUIDs == nil {
		in.CommitteeUIDs = slices.Clone(pub.CommitteeUIDs)
	}
	if strings.TrimSpace(in.EDReplyEmail) == "" && pub.ReplyToEmail != nil {
		in.EDReplyEmail = *pub.ReplyToEmail
	}
	if pub.TemplateKey == nil {
		return nil
	}
	if in.BodyLayout == nil {
		if strings.TrimSpace(in.BodyHTML) != "" {
			return fmt.Errorf("%w: publication uses template_key %q, supply body_layout instead of body_html", domain.ErrInvalidRequest, *pub.TemplateKey)
		}
		in.BodyLayout = &declarative.Layout{TemplateKey: *pub.TemplateKey, Blocks: []declarative.Block{}}
		return nil
	}
	return pinLayoutTemplate(in.BodyLayout, *pub.TemplateKey, "")
}

// pinLayoutTemplate makes layout use the publication's template set. A blank
// template_key inherits the key the edition already has, or else the
// publication's. storedKey is the key the edition already renders
// with, which stays allowed so a later change to the publication's template set
// does not strand an edition created before it.
func pinLayoutTemplate(layout *declarative.Layout, publicationKey, storedKey string) error {
	if layout.TemplateKey == "" {
		layout.TemplateKey = publicationKey
		if storedKey != "" {
			layout.TemplateKey = storedKey
		}
		return nil
	}
	if layout.TemplateKey != publicationKey && layout.TemplateKey != storedKey {
		return fmt.Errorf("%w: body_layout template_key %q does not match the publication's template_key %q", domain.ErrInvalidRequest, layout.TemplateKey, publicationKey)
	}
	return nil
}

// enforcePublicationTemplate rejects an update that would move an edition off
// its publication's template set. Only a pinned publication constrains an
// update, so implicit publications (no template) keep today's behaviour.
func (s *NewsletterService) enforcePublicationTemplate(ctx context.Context, existing *model.Newsletter, in *UpdateDraftInput) error {
	if existing.PublicationUID == nil || !in.BodyLayoutSet {
		return nil
	}
	pub, err := s.publicationFor(ctx, existing.ProjectUID, *existing.PublicationUID)
	if err != nil {
		return err
	}
	if pub.TemplateKey == nil {
		return nil
	}
	if in.BodyLayout == nil {
		// An edition saved before its publication had a template set has no
		// layout to clear, so it keeps saving as a basic edition.
		if len(existing.BodyLayout) == 0 {
			return nil
		}
		return fmt.Errorf("%w: body_layout cannot be cleared on an edition of a publication with a template set", domain.ErrInvalidRequest)
	}
	var stored struct {
		TemplateKey string `json:"template_key"`
	}
	if len(existing.BodyLayout) > 0 {
		if err := json.Unmarshal(existing.BodyLayout, &stored); err != nil {
			return fmt.Errorf("unmarshal stored body_layout: %w", err)
		}
		// A stored layout with no template_key renders with the fallback
		// library, so that is the set it keeps; a blank key must not pick up
		// a template set the publication gained later.
		if stored.TemplateKey == "" {
			stored.TemplateKey = declarative.FallbackTemplateKey
		}
	}
	if err := pinLayoutTemplate(in.BodyLayout, *pub.TemplateKey, stored.TemplateKey); err != nil {
		return err
	}
	return nil
}
