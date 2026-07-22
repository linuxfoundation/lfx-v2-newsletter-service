// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/handler"
	natsinfra "github.com/linuxfoundation/lfx-v2-newsletter-service/internal/infrastructure/nats"
	sendgridinfra "github.com/linuxfoundation/lfx-v2-newsletter-service/internal/infrastructure/sendgrid"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/repository"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/schema"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
)

// Package-level singletons populated by InitInfrastructure and torn down by Shutdown.
var (
	pgPool       *pgxpool.Pool
	bunDB        *bun.DB
	sqlDB        *sql.DB
	httpHandler  http.Handler
	handlerImpl  *handler.Handler
	authImpl     *handler.AuthValidator
	natsClient   *natsinfra.Client
	sendSvc      *service.SendOrchestrator
	recoveryStop chan struct{}
)

// stuckSendSweepInterval is how often the recovery sweep re-checks for
// newsletters stranded in 'sending' by a crashed pod.
// stuckSendSweepTimeout bounds a single sweep iteration.
const (
	stuckSendSweepInterval = 5 * time.Minute
	stuckSendSweepTimeout  = 30 * time.Second
)

// drainTimeout bounds how long Shutdown waits for in-flight background send
// jobs. Kept under main's 25s graceful-shutdown window; anything that doesn't
// finish is recovered by the stuck-sending sweep on the next pod.
const drainTimeout = 20 * time.Second

// InitInfrastructure wires every singleton in dependency order. Idempotent
// in the sense that callers should not call it twice.
func InitInfrastructure(ctx context.Context, cfg AppConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	// Step 1: open the pgx connection pool used at runtime.
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open pgx pool: %w", err)
	}
	pgPool = pool

	sqlDB = stdlib.OpenDBFromPool(pool)
	bunDB = bun.NewDB(sqlDB, pgdialect.New())

	// Step 2: bootstrap database schema (idempotent, advisory-locked).
	if err := schema.Apply(ctx, pgPool); err != nil {
		return fmt.Errorf("schema apply: %w", err)
	}

	// Step 3: NATS — used by the email dispatcher, committee client, and
	// project metadata client. No upstream HTTP service-to-service calls
	// remain (the prior HTTP committee query client used to forward the user
	// bearer token, but Heimdall mints a JWT the query-service can't validate,
	// so that path returned empty results in practice).
	nc, err := natsinfra.New(ctx, natsinfra.Config{
		URL:           cfg.NATSURL,
		Timeout:       cfg.NATSTimeout,
		MaxReconnect:  cfg.NATSMaxReconnect,
		ReconnectWait: cfg.NATSReconnectWait,
	})
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}
	natsClient = nc
	committeeClient := natsinfra.NewCommitteeClient(nc)
	projectClient := natsinfra.NewProjectClient(nc)
	emailDispatcher, err := newEmailDispatcher(ctx, cfg, nc)
	if err != nil {
		return err
	}
	userMetadataClient := natsinfra.NewUserMetadataClient(nc)

	// Step 4: auth.
	auth, err := handler.NewAuthValidator(ctx, cfg.JWKSURL, cfg.ExpectedAudience)
	if err != nil {
		return fmt.Errorf("auth validator: %w", err)
	}
	authImpl = auth
	if auth == nil && cfg.RequireUserAuth {
		return errors.New("REQUIRE_USER_AUTH is true but JWKS_URL is empty")
	}
	if auth == nil {
		// Fail closed outside of explicitly-local environments. A misconfigured
		// deploy with REQUIRE_USER_AUTH=false in production would silently
		// disable all JWT validation; refuse to start instead.
		env := strings.ToLower(strings.TrimSpace(cfg.LFXEnvironment))
		if env != "" && env != "local" && env != "development" && env != "dev" {
			return fmt.Errorf("auth is disabled (REQUIRE_USER_AUTH=false) but LFX_ENVIRONMENT=%q is not local/development — refusing to start", cfg.LFXEnvironment)
		}
		slog.WarnContext(ctx, "AuthValidator is nil; JWT validation is disabled (REQUIRE_USER_AUTH=false)")
	}

	// Step 5: domain wiring.
	repo := repository.NewPostgresNewsletterRepo(bunDB)
	newsletterSvc := service.NewNewsletterService(repo)
	unsubSvc := service.NewUnsubscribeService(repo, []byte(cfg.UnsubscribeSecret), cfg.PublicBaseURL)
	sendSvc = service.NewSendOrchestrator(service.SendOrchestratorConfig{
		Repo:                  repo,
		Committee:             committeeClient,
		Project:               projectClient,
		Email:                 emailDispatcher,
		UserMetadata:          userMetadataClient,
		UserEmail:             userMetadataClient,
		Unsubscribe:           unsubSvc,
		Concurrency:           cfg.SendConcurrency,
		FanoutEnabled:         cfg.SendFanoutEnabled,
		FromAddress:           cfg.EmailFromAddress,
		FromAddressOverrides:  cfg.EmailFromAddressOverrides,
		ReplyToAllowedDomains: cfg.EmailReplyToAllowedDomains,
		SendJobTimeout:        cfg.SendJobTimeout,
	})
	analyticsSvc := service.NewAnalyticsService(repo, emailDispatcher)

	// Step 6: recovery sweep for newsletters stranded in 'sending' by a pod
	// crash mid-fan-out. Runs once at startup (catches strands from previous
	// pods immediately) and then on a ticker.
	startStuckSendRecovery(repo, cfg.StuckSendTTL)

	handlerImpl = handler.New(handler.Config{
		Newsletter:      newsletterSvc,
		Send:            sendSvc,
		Analytics:       analyticsSvc,
		Unsubscribe:     unsubSvc,
		Project:         projectClient,
		DB:              sqlDB,
		Auth:            authImpl,
		RequireUserAuth: cfg.RequireUserAuth,
	})
	httpHandler = handlerImpl.Routes()

	return nil
}

// HTTPHandler returns the http.Handler wired by InitInfrastructure.
func HTTPHandler() http.Handler { return httpHandler }

// SQLDB returns the runtime *sql.DB for use by health probes during startup.
func SQLDB() *sql.DB { return sqlDB }

// stuckSendRecoverer is the narrow repository slice the recovery sweep needs.
type stuckSendRecoverer interface {
	RecoverStuckSending(ctx context.Context, olderThan time.Duration) (int64, error)
}

// startStuckSendRecovery launches the background sweep that settles
// newsletters stranded in 'sending'. Stopped via recoveryStop in Shutdown.
func startStuckSendRecovery(repo stuckSendRecoverer, ttl time.Duration) {
	recoveryStop = make(chan struct{})
	go func() {
		sweep := func() {
			// Bound each iteration so a hung DB call can't wedge the sweep
			// goroutine (Shutdown closes recoveryStop without awaiting it).
			ctx, cancel := context.WithTimeout(context.Background(), stuckSendSweepTimeout)
			defer cancel()
			recovered, err := repo.RecoverStuckSending(ctx, ttl)
			if err != nil {
				slog.ErrorContext(ctx, "stuck-sending recovery sweep failed", "error", err)
				return
			}
			if recovered > 0 {
				slog.WarnContext(ctx, "stuck-sending recovery sweep settled stranded newsletters",
					"recovered", recovered,
					"ttl", ttl.String(),
				)
			}
		}
		sweep()
		ticker := time.NewTicker(stuckSendSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sweep()
			case <-recoveryStop:
				return
			}
		}
	}()
}

// Shutdown tears down singletons in reverse order. Safe to call from defer.
func Shutdown() {
	if recoveryStop != nil {
		close(recoveryStop)
		recoveryStop = nil
	}
	// Give in-flight background send jobs a bounded chance to settle before
	// the NATS and DB connections go away. An undrained job is recovered by
	// the stuck-sending sweep on the next pod. This drain is race-free only
	// because main.go stops the HTTP server (the sole source of jobs.Add)
	// before the deferred Shutdown runs; the pod's terminationGracePeriodSeconds
	// must cover both windows sequentially (see chart values.yaml).
	if sendSvc != nil {
		drainCtx, cancel := context.WithTimeout(context.Background(), drainTimeout)
		defer cancel()
		if !sendSvc.Drain(drainCtx) {
			slog.WarnContext(drainCtx, "shutdown: background send jobs did not drain in time; stuck-sending sweep will recover them")
		}
	}
	if natsClient != nil {
		natsClient.Close()
	}
	if bunDB != nil {
		if err := bunDB.Close(); err != nil {
			slog.Warn("bun close error", "error", err)
		}
	}
	if pgPool != nil {
		pgPool.Close()
	}
}

// newEmailDispatcher selects the outbound send provider from cfg.EmailProvider.
// "sendgrid" brokers SendGrid directly (newsletter/marketing, LFXV2-2388); any
// other value keeps the NATS request/reply path to lfx-v2-email-service (SES,
// transactional). The SendGrid provider does not serve engagement reads until
// the event-webhook store lands, so selecting it logs a warning.
func newEmailDispatcher(ctx context.Context, cfg AppConfig, nc *natsinfra.Client) (port.EmailDispatcher, error) {
	if cfg.EmailProvider == "sendgrid" {
		sg, err := sendgridinfra.NewDispatcher(sendgridinfra.Config{
			APIKey:      cfg.SendGridAPIKey,
			DefaultFrom: cfg.EmailFromAddress,
			SandboxMode: cfg.SendGridSandboxMode,
		})
		if err != nil {
			return nil, fmt.Errorf("sendgrid dispatcher: %w", err)
		}
		slog.WarnContext(ctx, "email provider: SendGrid — send path active; engagement/analytics reads are NOT yet wired (event webhook pending, LFXV2-2388)",
			"sandbox", cfg.SendGridSandboxMode)
		return sg, nil
	}
	return natsinfra.NewEmailDispatcher(nc), nil
}
