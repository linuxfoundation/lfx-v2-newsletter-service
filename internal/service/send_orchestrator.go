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

// SendDraftInput is the typed input for SendDraft.
type SendDraftInput struct {
	DraftID         uuid.UUID
	ExpectedVersion int64
	EDName          string
}

// SendDraft loads a draft, resolves the recipient list, marks the draft as
// sent, and returns an aggregate result. No email is actually dispatched.
func (o *SendOrchestrator) SendDraft(ctx context.Context, in SendDraftInput) (*model.SendResult, error) {
	if in.EDName == "" {
		return nil, fmt.Errorf("%w: edName is required", domain.ErrInvalidRequest)
	}

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

	if _, markErr := o.repo.MarkSent(ctx, draft.ID, time.Now().UTC(), len(recipients), draft.Version); markErr != nil {
		return nil, fmt.Errorf("mark sent: %w", markErr)
	}

	slog.InfoContext(ctx, "draft marked sent (email dispatch not yet wired)",
		"draft_id", draft.ID,
		"total_recipients", len(recipients),
	)

	return &model.SendResult{
		TotalRecipients: len(recipients),
		Sent:            len(recipients),
		Failed:          0,
		Failures:        nil,
	}, nil
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
	if in.EDName == "" {
		return fmt.Errorf("%w: edName is required", domain.ErrInvalidRequest)
	}

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
// by lowercased email, and filters obviously bad addresses.
func (o *SendOrchestrator) resolveRecipients(ctx context.Context, committeeUIDs []string) ([]model.CommitteeMember, error) {
	type committeeResult struct {
		members []model.CommitteeMember
		err     error
	}
	results := make([]committeeResult, len(committeeUIDs))

	var wg sync.WaitGroup
	for i, uid := range committeeUIDs {
		wg.Add(1)
		go func(idx int, committeeUID string) {
			defer wg.Done()
			members, err := o.committee.GetMembers(ctx, committeeUID)
			results[idx] = committeeResult{members: members, err: err}
		}(i, uid)
	}
	wg.Wait()

	seen := make(map[string]struct{})
	out := make([]model.CommitteeMember, 0)
	for _, r := range results {
		if r.err != nil {
			return nil, r.err
		}
		for _, m := range r.members {
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
