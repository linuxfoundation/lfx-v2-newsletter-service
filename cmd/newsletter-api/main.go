// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Newsletter API service entry point.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	appsvc "github.com/linuxfoundation/lfx-v2-newsletter-service/cmd/newsletter-api/service"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/infrastructure/observability"
)

// Build-time metadata injected via -ldflags.
var (
	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

const (
	gracefulShutdownSeconds = 25
	readHeaderTimeout       = 10 * time.Second
)

func init() {
	// Bootstrap with default log level; reconfigured in run() once AppConfigFromEnv loads LOG_LEVEL.
	observability.InitStructureLogConfig("")
}

func main() {
	if err := run(); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	otelConfig := observability.OTelConfigFromEnv(ctx)
	if otelConfig.ServiceVersion == "" {
		otelConfig.ServiceVersion = Version
	}
	otelShutdown, err := observability.SetupOTelSDKWithConfig(ctx, otelConfig)
	if err != nil {
		return err
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), gracefulShutdownSeconds*time.Second)
		defer cancel()
		if shutErr := otelShutdown(shutCtx); shutErr != nil {
			slog.ErrorContext(ctx, "error shutting down OpenTelemetry SDK", "error", shutErr)
		}
	}()

	slog.InfoContext(ctx, "starting newsletter service",
		"version", Version,
		"build_time", BuildTime,
		"git_commit", GitCommit,
	)

	cfg, err := appsvc.AppConfigFromEnv()
	if err != nil {
		return err
	}
	observability.InitStructureLogConfig(cfg.LogLevel)

	if err := appsvc.InitInfrastructure(ctx, cfg); err != nil {
		return err
	}
	defer appsvc.Shutdown()

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           appsvc.HTTPHandler(),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	serverErr := make(chan error, 1)
	go func() {
		slog.InfoContext(ctx, "HTTP server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		slog.InfoContext(ctx, "received shutdown signal, stopping", "signal", sig.String())
	case err := <-serverErr:
		slog.ErrorContext(ctx, "HTTP server error", "error", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), gracefulShutdownSeconds*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.ErrorContext(ctx, "HTTP server shutdown error", "error", err)
		return err
	}
	slog.InfoContext(ctx, "HTTP server stopped cleanly")
	return nil
}
