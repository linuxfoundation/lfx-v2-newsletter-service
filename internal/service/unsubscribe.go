// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain"
	"github.com/linuxfoundation/lfx-v2-newsletter-service/internal/domain/port"
)

// UnsubscribeURLPlaceholder is the sentinel string the send orchestrator
// embeds in the rendered email body so the per-recipient unsubscribe URL can
// be substituted inside the fan-out loop without re-rendering the full HTML
// envelope for every recipient.
const UnsubscribeURLPlaceholder = "%%UNSUBSCRIBE_URL%%"

// unsubscribePath is the public route the handler registers for the
// one-click unsubscribe link.
const unsubscribePath = "/newsletters/unsubscribe"

// UnsubscribeService owns project-scoped opt-out persistence and the
// HMAC-signed token that secures the public unsubscribe link.
type UnsubscribeService struct {
	repo    port.UnsubscribeRepository
	secret  []byte
	baseURL string
}

// NewUnsubscribeService wires an UnsubscribeService. baseURL should be the
// externally-reachable origin of this service (e.g.
// "https://api.lfx.linuxfoundation.org/newsletter"); trailing slashes are
// trimmed.
func NewUnsubscribeService(repo port.UnsubscribeRepository, secret []byte, baseURL string) *UnsubscribeService {
	return &UnsubscribeService{
		repo:    repo,
		secret:  secret,
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

// Enabled reports whether the service has enough config to mint working
// links. When false, the send orchestrator falls back to the legacy "reply
// with UNSUBSCRIBE" footer copy so misconfigured environments still send
// valid emails.
func (s *UnsubscribeService) Enabled() bool {
	return s != nil && len(s.secret) > 0 && s.baseURL != ""
}

// BuildURL returns the per-recipient unsubscribe link for the given project
// and email address.
func (s *UnsubscribeService) BuildURL(projectUID, email string) string {
	return s.baseURL + unsubscribePath + "?t=" + url.QueryEscape(s.buildToken(projectUID, email))
}

// buildToken returns base64url(projectUID + "\n" + email + "\n" + hexMAC).
// Newline is the field separator because it cannot appear in a project UID
// or an email address.
func (s *UnsubscribeService) buildToken(projectUID, email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	payload := projectUID + "\n" + email
	mac := s.sign(payload)
	return base64.RawURLEncoding.EncodeToString([]byte(payload + "\n" + mac))
}

// VerifyToken decodes and authenticates an unsubscribe token. Returns
// domain.ErrInvalidRequest on any decode failure or signature mismatch so
// the handler can surface a single "invalid link" response without leaking
// which step failed.
func (s *UnsubscribeService) VerifyToken(token string) (projectUID, email string, err error) {
	if len(s.secret) == 0 {
		return "", "", fmt.Errorf("%w: unsubscribe is not configured", domain.ErrInvalidRequest)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return "", "", fmt.Errorf("%w: malformed token", domain.ErrInvalidRequest)
	}
	parts := strings.Split(string(raw), "\n")
	if len(parts) != 3 {
		return "", "", fmt.Errorf("%w: malformed token", domain.ErrInvalidRequest)
	}
	projectUID, email, gotMAC := parts[0], parts[1], parts[2]
	if projectUID == "" || email == "" {
		return "", "", fmt.Errorf("%w: malformed token", domain.ErrInvalidRequest)
	}
	wantMAC := s.sign(projectUID + "\n" + email)
	if !hmac.Equal([]byte(gotMAC), []byte(wantMAC)) {
		return "", "", fmt.Errorf("%w: invalid signature", domain.ErrInvalidRequest)
	}
	return projectUID, email, nil
}

// Unsubscribe verifies the token and records the opt-out. Returns the
// decoded project UID and email so the handler can render a confirmation.
func (s *UnsubscribeService) Unsubscribe(ctx context.Context, token string) (projectUID, email string, err error) {
	projectUID, email, err = s.VerifyToken(token)
	if err != nil {
		return "", "", err
	}
	if err := s.repo.CreateUnsubscribe(ctx, projectUID, email); err != nil {
		return "", "", err
	}
	slog.InfoContext(ctx, "newsletter unsubscribe recorded",
		"project_uid", projectUID,
		"email", redactEmail(email),
	)
	return projectUID, email, nil
}

func (s *UnsubscribeService) sign(payload string) string {
	h := hmac.New(sha256.New, s.secret)
	h.Write([]byte(payload))
	return hex.EncodeToString(h.Sum(nil))
}
