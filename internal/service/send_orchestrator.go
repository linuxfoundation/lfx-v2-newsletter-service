// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// SendOrchestrator coordinates recipient resolution and the draft → sent
// state transition.
//
// Note: email delivery is intentionally **not wired up in this iteration**.
// /send and /test-send are no-ops with respect to the email service — they
// only validate inputs, resolve recipient counts, and (for /send) flip the
// persisted draft into status=sent. Wiring up a real email publisher
// (e.g. NATS to lfx-v2-email-service) is a follow-up.
type SendOrchestrator struct {
	repo      port.NewsletterRepository
	committee port.CommitteeClient
}

// SendOrchestratorConfig configures a SendOrchestrator instance.
type SendOrchestratorConfig struct {
	Repo      port.NewsletterRepository
	Committee port.CommitteeClient
}

// NewSendOrchestrator wires a SendOrchestrator from the given config.
func NewSendOrchestrator(cfg SendOrchestratorConfig) *SendOrchestrator {
	return &SendOrchestrator{
		repo:      cfg.Repo,
		committee: cfg.Committee,
	}
}

// SendDraftInput is the typed input for SendDraft. EDName is accepted from the
// UI for forwards compatibility (the field is part of the legacy contract) but
// is not currently persisted or otherwise used; it is intentionally not
// validated.
//
// GroupID is the lfx-v2-email-service correlation identifier that lfx-v2-ui
// minted before fanning out per-recipient sends. It is persisted on the
// newsletter row so analytics queries can locate the engagement records.
type SendDraftInput struct {
	DraftID         uuid.UUID
	ExpectedVersion int64
	GroupID         string
	EDName          string
}

// SendDraft loads a draft, resolves the recipient list, and marks the draft as
// sent — persisting the email-service group_id supplied by the caller. Email
// dispatch itself happens in lfx-v2-ui's Express layer (one send_email per
// recipient against lfx-v2-email-service); this method is now a pure state
// transition.
func (o *SendOrchestrator) SendDraft(ctx context.Context, in SendDraftInput) (*model.Newsletter, error) {
	rawGroupID := strings.TrimSpace(in.GroupID)
	if rawGroupID == "" {
		return nil, fmt.Errorf("%w: groupId is required", domain.ErrInvalidRequest)
	}
	parsedGroupID, err := uuid.Parse(rawGroupID)
	if err != nil {
		return nil, fmt.Errorf("%w: groupId is not a valid UUID: %v", domain.ErrInvalidRequest, err)
	}
	groupID := parsedGroupID.String()

	draft, err := o.repo.Get(ctx, in.DraftID)
	if err != nil {
		return nil, err
	}
	if draft.Status == model.StatusSent {
		return nil, domain.ErrAlreadySent
	}
	if in.ExpectedVersion != 0 && draft.Version != in.ExpectedVersion {
		return nil, domain.ErrVersionMismatch
	}

	recipients, err := o.resolveRecipients(ctx, draft.CommitteeUIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve recipients: %w", err)
	}

	updated, markErr := o.repo.MarkSent(ctx, draft.ID, time.Now().UTC(), len(recipients), groupID, draft.Version)
	if markErr != nil {
		return nil, fmt.Errorf("mark sent: %w", markErr)
	}

	slog.InfoContext(ctx, "draft marked sent",
		"draft_id", draft.ID,
		"group_id", groupID,
		"total_recipients", len(recipients),
	)

	return updated, nil
}

// TestSendInput is the typed input for TestSend.
type TestSendInput struct {
	ContextType  model.ContextType
	ContextUID   string
	Subject      string
	BodyHTML     string
	ToEmail      string
	EDReplyEmail string
	EDName       string
}

// TestSend validates the inputs but does not dispatch any email. It exists so
// the UI's "send test" button keeps a stable contract; wire a real publisher
// in a future iteration.
func (o *SendOrchestrator) TestSend(ctx context.Context, in TestSendInput) error {
	if err := validateContextType(in.ContextType); err != nil {
		return err
	}
	if err := validateSubject(in.Subject); err != nil {
		return err
	}
	if err := validateBodyHTML(in.BodyHTML); err != nil {
		return err
	}
	if err := validateEDReplyEmail(in.EDReplyEmail); err != nil {
		return err
	}
	if _, err := mail.ParseAddress(strings.TrimSpace(in.ToEmail)); err != nil {
		return fmt.Errorf("%w: toEmail is not a valid email: %v", domain.ErrInvalidRequest, err)
	}
	// EDName is accepted for forwards compatibility (legacy UI contract) but
	// not used until email dispatch is wired.

	slog.InfoContext(ctx, "test-send accepted (email dispatch not yet wired)",
		"to_email", in.ToEmail,
		"context_type", in.ContextType,
		"context_uid", in.ContextUID,
	)
	return nil
}

// RecipientCount resolves recipients and returns the unique count.
func (o *SendOrchestrator) RecipientCount(ctx context.Context, committeeUIDs []string) (int, error) {
	if err := validateCommitteeUIDs(committeeUIDs); err != nil {
		return 0, err
	}
	recipients, err := o.resolveRecipients(ctx, committeeUIDs)
	if err != nil {
		return 0, err
	}
	return len(recipients), nil
}

// Recipients resolves recipients and returns the unique list.
func (o *SendOrchestrator) Recipients(ctx context.Context, committeeUIDs []string) ([]model.CommitteeMember, error) {
	if err := validateCommitteeUIDs(committeeUIDs); err != nil {
		return nil, err
	}
	return o.resolveRecipients(ctx, committeeUIDs)
}

// resolveRecipients fans out to the committee client across committees, dedupes
// by lowercased email, and filters obviously bad addresses. The errgroup cancels
// in-flight goroutines as soon as one returns an error so a transient failure
// from one committee doesn't keep the remaining lookups running against the
// upstream query service.
func (o *SendOrchestrator) resolveRecipients(ctx context.Context, committeeUIDs []string) ([]model.CommitteeMember, error) {
	results := make([][]model.CommitteeMember, len(committeeUIDs))

	g, gctx := errgroup.WithContext(ctx)
	for i, uid := range committeeUIDs {
		idx, committeeUID := i, uid
		g.Go(func() error {
			members, err := o.committee.GetMembers(gctx, committeeUID)
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

	seen := make(map[string]struct{})
	out := make([]model.CommitteeMember, 0)
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
			out = append(out, model.CommitteeMember{
				Email:     email,
				FirstName: strings.TrimSpace(m.FirstName),
			})
		}
	}
	return out, nil
}
