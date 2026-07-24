// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package service provides configuration and dependency-injection wiring for
// the newsletter-api binary. All environment variable reads live in this
// package; the rest of the codebase receives typed values.
package service

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// AppConfig holds all runtime configuration read from environment variables.
type AppConfig struct {
	// Server
	Port     string
	LogLevel string

	// Database (required) — DatabaseURL is the resolved DSN used at runtime.
	// It is either set directly via DATABASE_URL (external mode) or composed
	// in-process from PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE (CloudNativePG
	// mode). Composing in-process keeps the cluster-app password out of the
	// pod spec — the env-var interpolation pattern would embed it verbatim.
	DatabaseURL string

	// NATS (required) — single connection used by the email dispatcher,
	// committee member client, and project metadata client.
	NATSURL           string
	NATSTimeout       time.Duration
	NATSMaxReconnect  int
	NATSReconnectWait time.Duration

	// SendFanoutEnabled toggles the per-recipient send loop. When false, the
	// send orchestrator validates inputs and transitions the draft to sent
	// without dispatching email to recipients. Useful for dev/staging shake-out
	// of the recipient-resolution path without sending real mail.
	SendFanoutEnabled bool

	// SendConcurrency caps in-flight per-recipient sends during fan-out.
	SendConcurrency int

	// SendJobTimeout bounds the detached background fan-out that runs after a
	// send is accepted (the HTTP request returns 202 as soon as the draft
	// transitions to 'sending').
	SendJobTimeout time.Duration

	// StuckSendTTL is the age after which a newsletter stranded in 'sending'
	// (pod crash mid-fan-out) is recovered to 'sent' by the periodic sweep.
	// Kept above SendJobTimeout so the sweep never races a live job.
	StuckSendTTL time.Duration

	// EmailFromAddress is the bare address used as the SMTP envelope From on
	// outbound newsletters. Defaults to newsletter@lfx.linuxfoundation.org; override
	// per environment when a different sender is configured upstream. The
	// domain must be in the email-service allowlist or send_email will reject
	// the request.
	EmailFromAddress string

	// EmailFromAddressOverrides maps a normalized (lowercased) project slug to
	// the bare From address used for that project's newsletters, overriding
	// EmailFromAddress. Empty when unset. As with EmailFromAddress, each override
	// domain must be in the email-service allowlist or send_email rejects it.
	EmailFromAddressOverrides map[string]string

	// EmailReplyToAllowedDomains lists the domains a resolved sender email is
	// allowed to use as Reply-To, mirroring lfx-v2-email-service's
	// SMTP_ALLOWED_REPLY_TO_DOMAINS (subdomain suffix matching applies, so the
	// default also permits lfx.linuxfoundation.org). Validating locally lets a
	// disallowed sender domain fall back to the draft's stored ed_reply_email
	// instead of email-service rejecting the entire fan-out. Kept in sync with
	// email-service's allowlist manually; update both when either changes.
	EmailReplyToAllowedDomains []string

	// EmailProvider selects the outbound send provider: "email-service" (default;
	// NATS request/reply to lfx-v2-email-service -> SES, kept for transactional)
	// or "sendgrid" (newsletter-service brokers SendGrid directly for
	// newsletter/marketing sends, LFXV2-2388). SendGrid engagement analytics are
	// served from a local store the signed event webhook populates, routed per
	// newsletter by send_provider.
	EmailProvider string

	// SendGridAPIKey is the SendGrid API key (subuser-scoped in production).
	// Required when EmailProvider="sendgrid".
	SendGridAPIKey string

	// SendGridWebhookPublicKey is the base64 PKIX ECDSA public key from the
	// SendGrid signed-event-webhook settings, used to verify inbound event
	// batches. Optional; when unset the webhook route is not registered and
	// engagement analytics stay empty for SendGrid-sent newsletters.
	SendGridWebhookPublicKey string

	// SendGridAuthenticatedDomains lists the SendGrid-authenticated sending
	// domains for the configured subuser. A send whose From domain is not one of
	// these (or a subdomain) is rejected before hitting SendGrid. Empty permits
	// any From (dev/test).
	SendGridAuthenticatedDomains []string

	// UnsubscribeSecret is the HMAC key signing per-recipient unsubscribe
	// tokens. When empty, the footer falls back to the legacy "reply with
	// UNSUBSCRIBE" copy and the public endpoint rejects all requests.
	UnsubscribeSecret string

	// PublicBaseURL is the externally-reachable origin of this service,
	// used to build unsubscribe links embedded in outgoing emails.
	PublicBaseURL string

	// Auth
	JWKSURL          string
	ExpectedAudience string
	RequireUserAuth  bool

	// Environment (informational)
	LFXEnvironment string
}

// Defaults centralizes default values referenced from AppConfigFromEnv.
const (
	defaultPort                  = "8080"
	defaultNATSTimeout           = 10 * time.Second
	defaultNATSReconnectWaitSecs = 2
	defaultNATSURL               = "nats://nats:4222"
	defaultSendConcurrency       = 5
	defaultEmailFromAddress      = "newsletter@lfx.linuxfoundation.org"
	defaultEmailReplyToDomain    = "linuxfoundation.org"
	defaultSendJobTimeout        = 30 * time.Minute
	defaultStuckSendTTL          = 45 * time.Minute
	// minStuckSendSlack is the minimum margin enforced between SendJobTimeout
	// and StuckSendTTL so the recovery sweep never marks a row 'sent' while
	// its fan-out job is still running.
	minStuckSendSlack = 5 * time.Minute
)

// AppConfigFromEnv reads AppConfig from environment variables, applying defaults
// where reasonable. It returns an error if a required variable is missing.
func AppConfigFromEnv() (AppConfig, error) {
	cfg := AppConfig{
		Port:                      envOr("PORT", defaultPort),
		LogLevel:                  os.Getenv("LOG_LEVEL"),
		DatabaseURL:               os.Getenv("DATABASE_URL"),
		NATSURL:                   envOr("NATS_URL", defaultNATSURL),
		NATSTimeout:               durationOr("NATS_TIMEOUT", defaultNATSTimeout),
		NATSMaxReconnect:          intOr("NATS_MAX_RECONNECT", -1),
		NATSReconnectWait:         durationOr("NATS_RECONNECT_WAIT", time.Duration(defaultNATSReconnectWaitSecs)*time.Second),
		SendFanoutEnabled:         boolOr("SEND_FANOUT_ENABLED", true),
		SendConcurrency:           intOr("SEND_CONCURRENCY", defaultSendConcurrency),
		SendJobTimeout:            durationOr("SEND_JOB_TIMEOUT", defaultSendJobTimeout),
		StuckSendTTL:              durationOr("STUCK_SEND_TTL", defaultStuckSendTTL),
		EmailFromAddress:          envOr("EMAIL_FROM_ADDRESS", defaultEmailFromAddress),
		EmailFromAddressOverrides: parseFromAddressOverrides(os.Getenv("EMAIL_FROM_ADDRESS_OVERRIDES")),
		EmailReplyToAllowedDomains: parseAllowedDomains(
			os.Getenv("EMAIL_REPLY_TO_ALLOWED_DOMAINS"), defaultEmailReplyToDomain,
		),
		EmailProvider:                strings.ToLower(envOr("EMAIL_PROVIDER", "email-service")),
		SendGridAPIKey:               strings.TrimSpace(os.Getenv("SENDGRID_API_KEY")),
		SendGridWebhookPublicKey:     strings.TrimSpace(os.Getenv("SENDGRID_WEBHOOK_PUBLIC_KEY")),
		SendGridAuthenticatedDomains: splitCSV(os.Getenv("SENDGRID_AUTHENTICATED_DOMAINS")),
		UnsubscribeSecret:            os.Getenv("NEWSLETTER_UNSUBSCRIBE_SECRET"),
		PublicBaseURL:                strings.TrimSpace(os.Getenv("NEWSLETTER_PUBLIC_BASE_URL")),
		JWKSURL:                      os.Getenv("JWKS_URL"),
		ExpectedAudience:             os.Getenv("JWT_AUDIENCE"),
		RequireUserAuth:              boolOr("REQUIRE_USER_AUTH", true),
		LFXEnvironment:               os.Getenv("LFX_ENVIRONMENT"),
	}

	// If DATABASE_URL is not set, compose it from PG* env vars in-process so
	// the cluster-app password is never embedded as a literal string in the
	// pod spec (where it would be visible via `kubectl describe pod`).
	if cfg.DatabaseURL == "" {
		if composed, ok := composeDatabaseURL(); ok {
			cfg.DatabaseURL = composed
		}
	}

	var missing []string
	if cfg.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL (or PGHOST/PGUSER/PGPASSWORD/PGDATABASE)")
	}
	if cfg.RequireUserAuth && cfg.JWKSURL == "" {
		missing = append(missing, "JWKS_URL (required when REQUIRE_USER_AUTH=true)")
	}
	if cfg.RequireUserAuth && cfg.ExpectedAudience == "" {
		missing = append(missing, "JWT_AUDIENCE (required when REQUIRE_USER_AUTH=true)")
	}
	if cfg.SendFanoutEnabled && cfg.UnsubscribeSecret == "" {
		missing = append(missing, "NEWSLETTER_UNSUBSCRIBE_SECRET (required when SEND_FANOUT_ENABLED=true)")
	}
	if cfg.SendFanoutEnabled && cfg.PublicBaseURL == "" {
		missing = append(missing, "NEWSLETTER_PUBLIC_BASE_URL (required when SEND_FANOUT_ENABLED=true)")
	}
	if cfg.EmailProvider == "sendgrid" && cfg.SendGridAPIKey == "" {
		missing = append(missing, "SENDGRID_API_KEY (required when EMAIL_PROVIDER=sendgrid)")
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	// Reject an unknown EMAIL_PROVIDER up front. newEmailDispatcher treats any
	// non-"sendgrid" value as email-service, but the raw value is also stamped
	// onto newsletters.send_provider, whose CHECK constraint would then fail every
	// send at MarkSending — so a typo must fail fast at startup, not per send.
	if cfg.EmailProvider != "email-service" && cfg.EmailProvider != "sendgrid" {
		return cfg, fmt.Errorf("EMAIL_PROVIDER must be \"email-service\" or \"sendgrid\", got %q", cfg.EmailProvider)
	}
	// For a deployed SendGrid provider — any LFX_ENVIRONMENT other than an
	// explicit local/dev value; the chart does not set it, so an unset value is
	// treated as production and fails closed — require the two settings that would
	// otherwise silently degrade in exactly the misconfiguration each guard exists
	// to catch:
	//   - the authenticated-domain allowlist: an empty list makes fromDomainAllowed
	//     permit every From, disabling the authenticated-domain guard.
	//   - the event-webhook public key: without it the webhook route is never
	//     registered, so no delivery/open/bounce events arrive and engagement
	//     analytics stays empty for every SendGrid send.
	if cfg.EmailProvider == "sendgrid" {
		if env := strings.ToLower(strings.TrimSpace(cfg.LFXEnvironment)); env != "local" && env != "development" && env != "dev" {
			if len(cfg.SendGridAuthenticatedDomains) == 0 {
				return cfg, fmt.Errorf("SENDGRID_AUTHENTICATED_DOMAINS is required when EMAIL_PROVIDER=sendgrid outside an explicit local/dev LFX_ENVIRONMENT (LFX_ENVIRONMENT=%q); an empty allowlist disables the authenticated-domain guard", cfg.LFXEnvironment)
			}
			if cfg.SendGridWebhookPublicKey == "" {
				return cfg, fmt.Errorf("SENDGRID_WEBHOOK_PUBLIC_KEY is required when EMAIL_PROVIDER=sendgrid outside an explicit local/dev LFX_ENVIRONMENT (LFX_ENVIRONMENT=%q); without it no delivery/open/bounce events arrive and engagement analytics stays empty", cfg.LFXEnvironment)
			}
		}
	}

	// Keep the stuck-send sweep strictly behind the job timeout so it can
	// never mark a row 'sent' while its fan-out is still running.
	if cfg.StuckSendTTL < cfg.SendJobTimeout+minStuckSendSlack {
		cfg.StuckSendTTL = cfg.SendJobTimeout + minStuckSendSlack
	}

	return cfg, nil
}

// composeDatabaseURL builds a Postgres DSN from PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE.
// Returns ok=false when the required fields are missing so the caller can fall
// through to the missing-env-var error path. PGPORT defaults to 5432.
func composeDatabaseURL() (string, bool) {
	host := strings.TrimSpace(os.Getenv("PGHOST"))
	user := strings.TrimSpace(os.Getenv("PGUSER"))
	password := os.Getenv("PGPASSWORD")
	database := strings.TrimSpace(os.Getenv("PGDATABASE"))
	if host == "" || user == "" || password == "" || database == "" {
		return "", false
	}
	port := strings.TrimSpace(os.Getenv("PGPORT"))
	if port == "" {
		port = "5432"
	}
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   host + ":" + port,
		Path:   "/" + database,
	}
	return u.String(), true
}

// Validate is reserved for future invariant checks; currently a no-op.
func (c AppConfig) Validate() error {
	return nil
}

// parseFromAddressOverrides parses a comma-separated list of `slug=address`
// pairs into a slug→address map. Slug keys are trimmed and lowercased so
// matching against a project slug is case-insensitive; addresses are trimmed.
// Malformed entries (no `=`, empty slug, or empty address) are skipped. An
// empty or all-malformed input yields an empty (non-nil) map.
func parseFromAddressOverrides(raw string) map[string]string {
	out := make(map[string]string)
	for pair := range strings.SplitSeq(raw, ",") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		slug := strings.ToLower(strings.TrimSpace(key))
		addr := strings.TrimSpace(value)
		if slug == "" || addr == "" {
			continue
		}
		out[slug] = addr
	}
	return out
}

// parseAllowedDomains parses a comma-separated list of domains into a
// normalized (trimmed, lowercased) slice. An empty or all-blank raw value
// falls back to []string{fallback} so the allowlist is never accidentally
// empty (which would reject every sender email).
func parseAllowedDomains(raw, fallback string) []string {
	var out []string
	for domain := range strings.SplitSeq(raw, ",") {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" {
			continue
		}
		out = append(out, domain)
	}
	if len(out) == 0 {
		return []string{fallback}
	}
	return out
}

// splitCSV parses a comma-separated env value into a trimmed, blank-free slice,
// returning nil (not a one-element blank slice) when the value is empty.
func splitCSV(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func boolOr(key string, fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch raw {
	case "":
		return fallback
	case "1", "true", "yes", "y":
		return true
	case "0", "false", "no", "n":
		return false
	default:
		return fallback
	}
}

func intOr(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

func durationOr(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return d
}
