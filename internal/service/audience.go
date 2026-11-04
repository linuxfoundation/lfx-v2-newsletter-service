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
	// stopCtx is cancelled by Stop. Every job context is cancelled with it, so a
	// shutting-down pod marks its in-flight audiences failed instead of leaving
	// them 'processing' until the stale sweep.
	stopCtx context.Context
	stop    context.CancelFunc
}

// NewAudienceService wires an AudienceService.
func NewAudienceService(cfg AudienceServiceConfig) *AudienceService {
	timeout := cfg.JobTimeout
	if timeout <= 0 {
		timeout = defaultAudienceJobTimeout
	}
	stopCtx, stop := context.WithCancel(context.Background())
	return &AudienceService{
		stopCtx:      stopCtx,
		stop:         stop,
		newsletters:  cfg.Newsletters,
		audiences:    cfg.Audiences,
		committee:    cfg.Committee,
		unsubscribed: cfg.Unsubscribed,
		jobTimeout:   timeout,
	}
}

// Stop cancels every in-flight resolution job. Each cancelled job records its
// audience as failed, so the user can resolve it again straight away. Call it
// on shutdown before Drain.
func (s *AudienceService) Stop() {
	s.stop()
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
// nothing is resolved again unless refresh is set. refresh rebuilds a ready
// snapshot from the committees' current membership.
//
// The newsletter must belong to projectUID and be a draft, and committeeUIDs
// must be the committees saved on the draft. A newsletter that is sending,
// scheduled or sent keeps the audience it was sent to.
func (s *AudienceService) Resolve(ctx context.Context, projectUID string, newsletterID uuid.UUID, committeeUIDs []string, refresh bool) (*model.Audience, error) {
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
	// Only the draft's own committees can be resolved, so a caller cannot make
	// the service read and store the members of an arbitrary committee.
	if !sameCommitteeUIDs(n.CommitteeUIDs, uids) {
		return nil, fmt.Errorf("%w: committee_uids must match the draft's saved committees; save the draft first", domain.ErrInvalidRequest)
	}

	existing, err := s.audiences.Get(ctx, newsletterID)
	switch {
	case err == nil:
		if existing.ProjectUID != projectUID {
			return nil, domain.ErrNotFound
		}
		if !refresh && existing.Status == model.AudienceReady && sameCommitteeUIDs(existing.CommitteeUIDs, uids) {
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
	stopJob := context.AfterFunc(s.stopCtx, cancel)
	s.jobs.Add(1)
	go func() {
		defer s.jobs.Done()
		defer stopJob()
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

// collect reads every committee and merges the members. A committee that
// returns no members fails the audience (see readCommittees).
func (s *AudienceService) collect(ctx context.Context, projectUID string, committeeUIDs []string) (audienceMembers, error) {
	lists, err := readCommittees(ctx, s.committee, committeeUIDs, true)
	if err != nil {
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
	case errors.Is(err, domain.ErrUnprocessable):
		return "a selected committee returned no members"
	case errors.Is(err, context.DeadlineExceeded):
		return "resolving the audience took too long"
	case errors.Is(err, context.Canceled):
		return "the service restarted while resolving the audience; resolve it again"
	default:
		return "could not read the committee members"
	}
}
