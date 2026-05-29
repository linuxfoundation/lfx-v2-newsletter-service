// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

// Package nats hosts the NATS-backed implementations of the outbound ports —
// committee member resolution, project metadata lookup, and email dispatch.
// The transport pattern mirrors lfx-v2-invite-service's NATS client (and the
// committee-service messaging client) so changes propagate consistently.
package nats

import (
	"context"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	pkgerrors "github.com/linuxfoundation/lfx-v2-newsletter-service/pkg/errors"
)

// Config configures the NATS client connection.
type Config struct {
	URL           string
	Timeout       time.Duration
	MaxReconnect  int
	ReconnectWait time.Duration
}

// Client wraps the NATS connection and provides request/reply infrastructure.
type Client struct {
	conn    *nats.Conn
	timeout time.Duration
}

// New creates a NATS client connected to the given URL. Reconnect handlers
// log disconnect/reconnect events at WARN/INFO so operators can correlate
// gaps in newsletter-service NATS calls with upstream availability.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.ReconnectWait <= 0 {
		cfg.ReconnectWait = 2 * time.Second
	}
	// MaxReconnect: negative means "reconnect forever" in nats.go semantics.
	if cfg.MaxReconnect == 0 {
		cfg.MaxReconnect = -1
	}

	conn, err := nats.Connect(cfg.URL,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(cfg.MaxReconnect),
		nats.ReconnectWait(cfg.ReconnectWait),
		nats.DisconnectErrHandler(func(_ *nats.Conn, dErr error) {
			slog.WarnContext(ctx, "NATS disconnected", "error", dErr)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			slog.InfoContext(ctx, "NATS reconnected", "url", nc.ConnectedUrl())
		}),
	)
	if err != nil {
		return nil, pkgerrors.NewServiceUnavailable("failed to connect to NATS", err)
	}

	slog.InfoContext(ctx, "NATS connected", "url", conn.ConnectedUrl())
	return &Client{conn: conn, timeout: cfg.Timeout}, nil
}

// Close drains and closes the NATS connection.
func (c *Client) Close() {
	if c.conn != nil {
		_ = c.conn.Drain()
	}
}

// IsReady reports whether the underlying NATS connection is connected and
// not draining. Used by the /readyz handler so kubelet can fail-stop the
// pod when newsletter-service can no longer reach NATS.
func (c *Client) IsReady() error {
	if c.conn == nil || !c.conn.IsConnected() || c.conn.IsDraining() {
		return pkgerrors.NewServiceUnavailable("NATS client is not ready")
	}
	return nil
}

// Request sends a synchronous NATS request and returns the raw response bytes.
// The deadline is the lesser of the per-client timeout and any deadline already
// on ctx, so a slow upstream can't blow past the caller's expectation.
func (c *Client) Request(ctx context.Context, subject string, data []byte) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	msg, err := c.conn.RequestWithContext(reqCtx, subject, data)
	if err != nil {
		return nil, pkgerrors.NewServiceUnavailable("NATS request failed", err)
	}
	return msg.Data, nil
}
