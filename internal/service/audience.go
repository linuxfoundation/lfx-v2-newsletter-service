// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/model"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// defaultAudienceJobTimeout bounds one background audience resolution.
const defaultAudienceJobTimeout = 30 * time.Minute

// newsletterGetter is the slice of the newsletter repository the audience
// service reads.
type newsletterGetter interface {
	Get(ctx context.Context, id uuid.UUID) (*model.Newsletter, error)
}

// unsubscribedLister lists the addresses that opted out of a project.
type unsubscribedLister interface {
	ListUnsubscribedEmails(ctx context.Context, projectUID string) (map[string]struct{}, error)
}

// AudienceServiceConfig configures an AudienceService.
type AudienceServiceConfig struct {
	Newsletters  newsletterGetter
	Audiences    port.AudienceRepository
	Committee    port.CommitteeClient
	Unsubscribed unsubscribedLister
	// JobTimeout bounds one background resolution. Zero selects
	// defaultAudienceJobTimeout.
	//
	// Coupled invariant: the stale sweep's TTL must exceed this timeout
	// (AppConfigFromEnv enforces AUDIENCE_STALE_TTL >= AUDIENCE_JOB_TIMEOUT + 5m),
	// or the sweep can fail a row whose job is still running.
	JobTimeout time.Duration
}

// AudienceService resolves a newsletter's recipients in a background job and
// stores them as a snapshot. The send, the recipient count and the recipient
// preview read the snapshot.
type AudienceService struct {
	newsletters  newsletterGetter
	audiences    port.AudienceRepository
	committee    port.CommitteeClient
	unsubscribed unsubscribedLister
	jobTimeout   time.Duration
	// jobs tracks detached resolution goroutines so graceful shutdown (and
	// tests) can wait for them through Drain.
	jobs sync.WaitGroup
}

// NewAudienceService wires an AudienceService.
func NewAudienceService(cfg AudienceServiceConfig) *AudienceService {
	timeout := cfg.JobTimeout
	if timeout <= 0 {
		timeout = defaultAudienceJobTimeout
	}
	return &AudienceService{
		newsletters:  cfg.Newsletters,
		audiences:    cfg.Audiences,
		committee:    cfg.Committee,
		unsubscribed: cfg.Unsubscribed,
		jobTimeout:   timeout,
	}
}

// Drain waits for in-flight resolution jobs, returning false if ctx ends first.
func (s *AudienceService) Drain(ctx context.Context) bool {
	done := make(chan struct{})
	go func() {
		s.jobs.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// Resolve starts a background resolution of the newsletter's audience and
// returns the audience row. The returned audience is 'processing' when a job is
// running, whether this call started it or an earlier call did. It is 'ready'
// when a snapshot already exists for the same committee list, in which case
// nothing is resolved again.
//
// The newsletter must belong to projectUID and be a draft. A newsletter that is
// sending, scheduled or sent keeps the audience it was sent to.
func (s *AudienceService) Resolve(ctx context.Context, projectUID string, newsletterID uuid.UUID, committeeUIDs []string) (*model.Audience, error) {
	if err := validateProjectUID(projectUID); err != nil {
		return nil, err
	}
	if err := validateCommitteeUIDs(committeeUIDs); err != nil {
		return nil, err
	}
	uids := canonicalCommitteeUIDs(committeeUIDs)
	if len(uids) == 0 {
		return nil, fmt.Errorf("%w: at least one committee_uid is required", domain.ErrInvalidRequest)
	}

	n, err := s.newsletters.Get(ctx, newsletterID)
	if err != nil {
		return nil, err
	}
	if n.ProjectUID != projectUID {
		return nil, domain.ErrNotFound
	}
	switch n.Status {
	case model.StatusDraft:
	case model.StatusSending:
		return nil, domain.ErrSendInProgress
	case model.StatusScheduled:
		return nil, domain.ErrScheduled
	default:
		return nil, domain.ErrAlreadySent
	}

	existing, err := s.audiences.Get(ctx, newsletterID)
	switch {
	case err == nil:
		if existing.ProjectUID != projectUID {
			return nil, domain.ErrNotFound
		}
		if existing.Status == model.AudienceReady && sameCommitteeUIDs(existing.CommitteeUIDs, uids) {
			return existing, nil
		}
	case errors.Is(err, domain.ErrNotFound):
	default:
		return nil, fmt.Errorf("read audience: %w", err)
	}

	claimed, err := s.audiences.BeginResolve(ctx, newsletterID, projectUID, uids)
	if err != nil {
		return nil, fmt.Errorf("begin audience resolve: %w", err)
	}
	if !claimed {
		return s.Get(ctx, projectUID, newsletterID)
	}

	// Read the claimed row before the job starts, so the caller always gets the
	// 'processing' audience even if the job is quick. The job starts whether or
	// not this read works, or the row would stay 'processing' with no job.
	processing, getErr := s.Get(ctx, projectUID, newsletterID)

	// Detach the job from the request, as the send job is detached, so a client
	// disconnect or gateway timeout cannot cancel it half way.
	jobCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.jobTimeout)
	s.jobs.Add(1)
	go func() {
		defer s.jobs.Done()
		defer cancel()
		s.runResolve(jobCtx, projectUID, newsletterID, uids)
	}()
	slog.InfoContext(ctx, "audience resolution started",
		"newsletter_id", newsletterID,
		"project_uid", projectUID,
		"committees", len(uids),
	)

	return processing, getErr
}

// Get returns the newsletter's audience, or domain.ErrNotFound when it was
// never resolved or belongs to another project.
func (s *AudienceService) Get(ctx context.Context, projectUID string, newsletterID uuid.UUID) (*model.Audience, error) {
	if err := validateProjectUID(projectUID); err != nil {
		return nil, err
	}
	a, err := s.audiences.Get(ctx, newsletterID)
	if err != nil {
		return nil, err
	}
	if a.ProjectUID != projectUID {
		return nil, domain.ErrNotFound
	}
	return a, nil
}

// runResolve is the background half of Resolve. It reads every committee,
// builds the recipient list, stores it and marks the audience ready. Any error
// marks the audience failed, so a snapshot is never ready with part of its
// members.
func (s *AudienceService) runResolve(ctx context.Context, projectUID string, newsletterID uuid.UUID, committeeUIDs []string) {
	start := time.Now()
	built, err := s.collect(ctx, projectUID, committeeUIDs)

	// The terminal writes must not depend on the job context, which may already
	// be exhausted when the reads used the whole job timeout.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()

	if err == nil {
		err = s.audiences.ReplaceMembers(persistCtx, newsletterID, built.Members)
	}
	if err == nil {
		err = s.audiences.MarkReady(persistCtx, newsletterID, len(built.Members), built.SkippedNoEmail)
	}
	if err != nil {
		slog.ErrorContext(ctx, "audience resolution failed",
			"newsletter_id", newsletterID,
			"project_uid", projectUID,
			"duration", time.Since(start).String(),
			"error", err.Error(),
		)
		if markErr := s.audiences.MarkFailed(persistCtx, newsletterID, audienceFailureMessage(err)); markErr != nil {
			slog.ErrorContext(ctx, "mark audience failed",
				"newsletter_id", newsletterID,
				"error", markErr.Error(),
			)
		}
		return
	}
	slog.InfoContext(ctx, "audience resolved",
		"newsletter_id", newsletterID,
		"project_uid", projectUID,
		"recipients", len(built.Members),
		"skipped_no_email", built.SkippedNoEmail,
		"unsubscribed", built.Unsubscribed,
		"duration", time.Since(start).String(),
	)
}

// collect reads every committee in parallel and merges the members. The
// errgroup cancels the other reads as soon as one fails.
func (s *AudienceService) collect(ctx context.Context, projectUID string, committeeUIDs []string) (audienceMembers, error) {
	lists := make([]model.CommitteeMembers, len(committeeUIDs))
	g, gctx := errgroup.WithContext(ctx)
	for i, uid := range committeeUIDs {
		g.Go(func() error {
			list, err := s.committee.ListMembers(gctx, uid)
			if err != nil {
				return err
			}
			lists[i] = list
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return audienceMembers{}, err
	}

	excluded, err := s.unsubscribed.ListUnsubscribedEmails(ctx, projectUID)
	if err != nil {
		return audienceMembers{}, fmt.Errorf("list unsubscribes: %w", err)
	}
	return buildAudienceMembers(lists, excluded), nil
}

// audienceFailureMessage turns a job error into a short message that is safe to
// show to the user. The full error is in the log.
func audienceFailureMessage(err error) string {
	var notFound pkgerrors.NotFound
	switch {
	case errors.As(err, &notFound):
		return "a selected committee was not found"
	case errors.Is(err, context.DeadlineExceeded):
		return "resolving the audience took too long"
	default:
		return "could not read the committee members"
	}
}
