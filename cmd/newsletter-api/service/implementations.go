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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/handler"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/infrastructure/upstream"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/repository"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/schema"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
)

// Package-level singletons populated by InitInfrastructure and torn down by Shutdown.
var (
	pgPool      *pgxpool.Pool
	bunDB       *bun.DB
	sqlDB       *sql.DB
	httpHandler http.Handler
	handlerImpl *handler.Handler
	authImpl    *handler.AuthValidator
)

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

	// Step 3: upstream HTTP client with OTel instrumentation.
	tracedClient := &http.Client{
		Timeout:   10 * time.Second,
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}
	committeeClient := upstream.NewCommitteeQueryClient(cfg.CommitteeServiceURL, tracedClient)

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
		slog.WarnContext(ctx, "AuthValidator is nil; JWT validation is disabled (REQUIRE_USER_AUTH=false)")
	}

	// Step 5: domain wiring.
	repo := repository.NewPostgresNewsletterRepo(bunDB)
	newsletterSvc := service.NewNewsletterService(repo)
	sendSvc := service.NewSendOrchestrator(service.SendOrchestratorConfig{
		Repo:      repo,
		Committee: committeeClient,
	})

	handlerImpl = handler.New(handler.Config{
		Newsletter:      newsletterSvc,
		Send:            sendSvc,
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

// Shutdown tears down singletons in reverse order. Safe to call from defer.
func Shutdown() {
	if bunDB != nil {
		if err := bunDB.Close(); err != nil {
			slog.Warn("bun close error", "error", err)
		}
	}
	if pgPool != nil {
		pgPool.Close()
	}
}
