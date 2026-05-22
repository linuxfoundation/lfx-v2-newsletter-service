// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package observability

import (
	"context"
	"log"
	"log/slog"
	"os"

	slogotel "github.com/remychantenay/slog-otel"
)

type ctxKey string

const (
	slogFields      ctxKey = "slog_fields"
	logLevelDefault        = slog.LevelDebug
	debug                  = "debug"
	warn                   = "warn"
	info                   = "info"
	errorLvl               = "error"
)

type contextHandler struct {
	slog.Handler
}

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if attrs, ok := ctx.Value(slogFields).([]slog.Attr); ok {
		for _, v := range attrs {
			r.AddAttrs(v)
		}
	}
	return h.Handler.Handle(ctx, r)
}

// AppendCtx attaches an slog.Attr to ctx so it will be included on every
// Record emitted with that context (via the package's contextHandler). Use
// this from middleware to thread request-scoped fields (e.g. request_id)
// into every log line for the request.
func AppendCtx(parent context.Context, attr slog.Attr) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	if v, ok := parent.Value(slogFields).([]slog.Attr); ok {
		// Copy before append: the parent's backing array may still have capacity,
		// so a bare append on a shared slice would mutate state visible to
		// concurrent goroutines that share the parent ctx (cross-request race).
		next := make([]slog.Attr, len(v), len(v)+1)
		copy(next, v)
		next = append(next, attr)
		return context.WithValue(parent, slogFields, next)
	}
	return context.WithValue(parent, slogFields, []slog.Attr{attr})
}

// InitStructureLogConfig initializes the structured log configuration.
// logLevel should be "debug", "info", "warn", "error", or "" for the default
// (debug). The "error" level matches what Helm values.yaml advertises.
// Call with "" during init() for early startup logging, then call again after
// AppConfigFromEnv() to apply the configured level.
func InitStructureLogConfig(logLevel string) {
	level := new(slog.LevelVar)
	level.Set(logLevelDefault)

	switch logLevel {
	case debug:
		level.Set(slog.LevelDebug)
	case info:
		level.Set(slog.LevelInfo)
	case warn:
		level.Set(slog.LevelWarn)
	case errorLvl:
		level.Set(slog.LevelError)
	default:
		level.Set(logLevelDefault)
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	handler := contextHandler{slogotel.OtelHandler{
		Next: slog.NewJSONHandler(os.Stderr, opts),
	}}

	logger := slog.New(handler)
	slog.SetDefault(logger)
	log.SetOutput(os.Stderr)
}
