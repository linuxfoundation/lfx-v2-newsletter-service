// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	newslettersvr "github.com/linuxfoundation/lfx-v2-newsletter-service/gen/http/newsletter_service/server"
	newsletterservice "github.com/linuxfoundation/lfx-v2-newsletter-service/gen/newsletter_service"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/infrastructure/auth"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/service"
	goahttp "goa.design/goa/v3/http"
)

// Server wraps the HTTP server
type Server struct {
	*http.Server
	newsletterService *service.NewsletterService
	authParser        auth.PrincipalParser
	logger            *log.Logger
}

// NewServer creates a new HTTP server
func NewServer(config *Config, newsletterService *service.NewsletterService, authParser auth.PrincipalParser, logger *log.Logger) *Server {
	s := &Server{
		newsletterService: newsletterService,
		authParser:        authParser,
		logger:            logger,
	}

	rootMux := http.NewServeMux()
	// Health check endpoint
	rootMux.HandleFunc("/health", s.handleHealth)
	rootMux.HandleFunc("/livez", s.handleHealth)
	// Ready check endpoint
	rootMux.HandleFunc("/ready", s.handleReady)
	rootMux.HandleFunc("/readyz", s.handleReady)

	goaMux := goahttp.NewMuxer()
	decoder := goahttp.RequestDecoder
	encoder := goahttp.ResponseEncoder
	errFormatter := func(ctx context.Context, err error) goahttp.Statuser {
		var statuser goahttp.Statuser
		if errors.As(err, &statuser) {
			return statuser
		}
		return goahttp.NewErrorResponse(ctx, err)
	}
	errHandler := func(ctx context.Context, w http.ResponseWriter, err error) {
		goahttp.ErrorEncoder(encoder, errFormatter)(ctx, w, err)
	}

	service := newGoaNewsletterService(newsletterService, authParser)
	endpoints := newsletterservice.NewEndpoints(service)
	server := newslettersvr.New(endpoints, goaMux, decoder, encoder, errHandler, errFormatter)
	newslettersvr.Mount(goaMux, server)
	mountRouteAlias(goaMux, "GET", "/newsletters/tag", server.GetNewslettersByTag)
	mountRouteAlias(goaMux, "GET", "/newsletters/id/{id}", server.GetNewsletterByID)

	rootMux.Handle("/", goaMux)

	s.Server = &http.Server{
		Addr:    fmt.Sprintf(":%s", config.Port),
		Handler: s.loggingMiddleware(rootMux),
	}

	return s
}

func mountRouteAlias(mux goahttp.Muxer, method, pattern string, handler http.Handler) {
	h, ok := handler.(http.HandlerFunc)
	if !ok {
		h = func(w http.ResponseWriter, r *http.Request) {
			handler.ServeHTTP(w, r)
		}
	}

	mux.Handle(method, pattern, h)
}

// handleHealth handles health check requests
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

// handleReady handles readiness check requests
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Ready"))
}

// loggingMiddleware logs HTTP requests
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.logger.Printf("%s %s %s", r.Method, r.RequestURI, r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
}
