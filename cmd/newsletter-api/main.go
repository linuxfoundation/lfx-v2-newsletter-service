// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/infrastructure/auth"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
)

func main() {
	// Load configuration
	config := LoadConfig()

	// Setup logger
	logger := log.New(os.Stdout, "[newsletter-api] ", log.LstdFlags)
	logger.Printf("Starting LFX v2 Newsletter Service on port %s", config.Port)

	// Initialize CMS provider repository
	repository, provider, err := buildCMSRepository(config)
	if err != nil {
		logger.Fatalf("Failed to initialize CMS provider: %v", err)
	}
	logger.Printf("CMS provider initialized: %s", provider)

	// Initialize services
	newsletterService := service.NewNewsletterService(repository)
	logger.Println("Newsletter service initialized")

	jwtAuthenticator, err := auth.NewJWTAuthenticator(auth.JWTAuthConfig{
		JWKSURL:            config.JWT.JWKSURL,
		Audience:           config.JWT.Audience,
		Issuer:             config.JWT.Issuer,
		ClockSkew:          config.JWT.ClockSkew,
		MockLocalPrincipal: config.JWT.DisabledMockLocalPrincipal,
	})
	if err != nil {
		logger.Fatalf("Failed to initialize JWT authentication: %v", err)
	}
	defer jwtAuthenticator.Close()
	logger.Println("JWT authentication initialized")

	// Create HTTP server
	server := NewServer(config, newsletterService, jwtAuthenticator, logger)

	// Start server
	go func() {
		addr := fmt.Sprintf(":%s", config.Port)
		logger.Printf("Server listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("Server failed to start: %v", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Println("Shutting down server...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Fatalf("Server forced to shutdown: %v", err)
	}
	logger.Println("Server exited")
}
