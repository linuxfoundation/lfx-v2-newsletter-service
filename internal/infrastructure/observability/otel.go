// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package observability

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/propagators/jaeger"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

const (
	otelProtocolGRPC = "grpc"
	otelProtocolHTTP = "http"
	otelExporterNone = "none"
)

// OTelConfig holds OpenTelemetry configuration options.
type OTelConfig struct {
	// ServiceName is the name of the service for resource identification.
	// Env: OTEL_SERVICE_NAME (default: "lfx-v2-newsletter-service")
	ServiceName string
	// ServiceVersion is the version of the service.
	// Env: OTEL_SERVICE_VERSION
	ServiceVersion string
	// Protocol specifies the OTLP protocol to use: "grpc" or "http".
	// Env: OTEL_EXPORTER_OTLP_PROTOCOL (default: "grpc")
	Protocol string
	// Endpoint is the OTLP collector endpoint.
	// For gRPC: typically "localhost:4317"
	// For HTTP: typically "localhost:4318"
	// Env: OTEL_EXPORTER_OTLP_ENDPOINT
	Endpoint string
	// Insecure disables TLS for the connection.
	// Env: OTEL_EXPORTER_OTLP_INSECURE (set to "true" for insecure connections)
	Insecure bool
	// TracesExporter specifies the traces exporter: "otlp" or "none".
	// Env: OTEL_TRACES_EXPORTER (default: "none")
	TracesExporter string
	// TracesSampleRatio specifies the sampling ratio for traces (0.0 to 1.0).
	// Env: OTEL_TRACES_SAMPLE_RATIO (default: 1.0)
	TracesSampleRatio float64
	// MetricsExporter specifies the metrics exporter: "otlp" or "none".
	// Env: OTEL_METRICS_EXPORTER (default: "none")
	MetricsExporter string
	// LogsExporter specifies the logs exporter: "otlp" or "none".
	// Env: OTEL_LOGS_EXPORTER (default: "none")
	LogsExporter string
	// Propagators specifies the trace context propagators to use.
	// Comma-separated list of: "tracecontext", "baggage", "jaeger"
	// Env: OTEL_PROPAGATORS (default: "tracecontext,baggage")
	Propagators string
}

// OTelConfigFromEnv creates an OTelConfig from environment variables.
func OTelConfigFromEnv(ctx context.Context) OTelConfig {
	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "lfx-v2-newsletter-service"
	}

	serviceVersion := os.Getenv("OTEL_SERVICE_VERSION")

	protocol := os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")
	if protocol == "" {
		protocol = otelProtocolGRPC
	}

	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	insecure := os.Getenv("OTEL_EXPORTER_OTLP_INSECURE") == "true"

	tracesExporter := os.Getenv("OTEL_TRACES_EXPORTER")
	if tracesExporter == "" {
		tracesExporter = otelExporterNone
	}

	metricsExporter := os.Getenv("OTEL_METRICS_EXPORTER")
	if metricsExporter == "" {
		metricsExporter = otelExporterNone
	}

	logsExporter := os.Getenv("OTEL_LOGS_EXPORTER")
	if logsExporter == "" {
		logsExporter = otelExporterNone
	}

	propagators := os.Getenv("OTEL_PROPAGATORS")
	if propagators == "" {
		propagators = "tracecontext,baggage"
	}

	tracesSampleRatio := 1.0
	if ratio := os.Getenv("OTEL_TRACES_SAMPLE_RATIO"); ratio != "" {
		if parsed, err := strconv.ParseFloat(ratio, 64); err == nil {
			if parsed >= 0.0 && parsed <= 1.0 {
				tracesSampleRatio = parsed
			} else {
				slog.WarnContext(ctx, "OTEL_TRACES_SAMPLE_RATIO must be between 0.0 and 1.0, using default 1.0",
					"provided-value", ratio)
			}
		} else {
			slog.Warn("invalid OTEL_TRACES_SAMPLE_RATIO value, using default 1.0",
				"provided-value", ratio, "error", err)
		}
	}

	slog.Debug("OTelConfig",
		"service-name", serviceName,
		"version", serviceVersion,
		"protocol", protocol,
		"endpoint", endpoint,
		"insecure", insecure,
		"traces-exporter", tracesExporter,
		"traces-sample-ratio", tracesSampleRatio,
		"metrics-exporter", metricsExporter,
		"logs-exporter", logsExporter,
		"propagators", propagators,
	)

	return OTelConfig{
		ServiceName:       serviceName,
		ServiceVersion:    serviceVersion,
		Protocol:          protocol,
		Endpoint:          endpoint,
		Insecure:          insecure,
		TracesExporter:    tracesExporter,
		TracesSampleRatio: tracesSampleRatio,
		MetricsExporter:   metricsExporter,
		LogsExporter:      logsExporter,
		Propagators:       propagators,
	}
}

// SetupOTelSDKWithConfig bootstraps the OpenTelemetry pipeline with the provided configuration.
// If it does not return an error, make sure to call the returned shutdown function.
func SetupOTelSDKWithConfig(ctx context.Context, cfg OTelConfig) (shutdown func(context.Context) error, err error) {
	var shutdownFuncs []func(context.Context) error

	shutdown = func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		shutdownFuncs = nil
		return err
	}

	handleErr := func(inErr error) {
		err = errors.Join(inErr, shutdown(ctx))
	}

	if cfg.Endpoint != "" {
		cfg.Endpoint = endpointURL(cfg.Endpoint, cfg.Insecure)
	}

	res, err := newResource(cfg)
	if err != nil {
		handleErr(err)
		return
	}

	otel.SetTextMapPropagator(newPropagator(cfg))

	if cfg.TracesExporter != otelExporterNone {
		var tracerProvider *trace.TracerProvider
		tracerProvider, err = newTraceProvider(ctx, cfg, res)
		if err != nil {
			handleErr(err)
			return
		}
		shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
		otel.SetTracerProvider(tracerProvider)
	}

	if cfg.MetricsExporter != otelExporterNone {
		var metricsProvider *metric.MeterProvider
		metricsProvider, err = newMetricsProvider(ctx, cfg, res)
		if err != nil {
			handleErr(err)
			return
		}
		shutdownFuncs = append(shutdownFuncs, metricsProvider.Shutdown)
		otel.SetMeterProvider(metricsProvider)
	}

	if cfg.LogsExporter != otelExporterNone {
		var loggerProvider *log.LoggerProvider
		loggerProvider, err = newLoggerProvider(ctx, cfg, res)
		if err != nil {
			handleErr(err)
			return
		}
		shutdownFuncs = append(shutdownFuncs, loggerProvider.Shutdown)
		global.SetLoggerProvider(loggerProvider)
	}

	return
}

func newResource(cfg OTelConfig) (*resource.Resource, error) {
	return resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
		),
	)
}

func newPropagator(cfg OTelConfig) propagation.TextMapPropagator {
	var propagators []propagation.TextMapPropagator

	for p := range strings.SplitSeq(cfg.Propagators, ",") {
		switch strings.TrimSpace(p) {
		case "tracecontext":
			propagators = append(propagators, propagation.TraceContext{})
		case "baggage":
			propagators = append(propagators, propagation.Baggage{})
		case "jaeger":
			propagators = append(propagators, jaeger.Jaeger{})
		default:
			slog.Warn("unknown propagator, skipping", "propagator", p)
		}
	}

	if len(propagators) == 0 {
		propagators = []propagation.TextMapPropagator{
			propagation.TraceContext{},
			propagation.Baggage{},
		}
	}

	return propagation.NewCompositeTextMapPropagator(propagators...)
}

func endpointURL(raw string, insecure bool) string {
	if strings.Contains(raw, "://") {
		return raw
	}
	if insecure {
		return "http://" + raw
	}
	return "https://" + raw
}

func newTraceProvider(ctx context.Context, cfg OTelConfig, res *resource.Resource) (*trace.TracerProvider, error) {
	var exporter trace.SpanExporter
	var err error

	if cfg.Protocol == otelProtocolHTTP {
		var opts []otlptracehttp.Option
		if cfg.Endpoint != "" {
			opts = append(opts, otlptracehttp.WithEndpointURL(cfg.Endpoint))
		}
		if cfg.Insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		exporter, err = otlptracehttp.New(ctx, opts...)
	} else {
		var opts []otlptracegrpc.Option
		if cfg.Endpoint != "" {
			opts = append(opts, otlptracegrpc.WithEndpointURL(cfg.Endpoint))
		}
		if cfg.Insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		exporter, err = otlptracegrpc.New(ctx, opts...)
	}

	if err != nil {
		return nil, err
	}

	return trace.NewTracerProvider(
		trace.WithResource(res),
		trace.WithSampler(trace.TraceIDRatioBased(cfg.TracesSampleRatio)),
		trace.WithBatcher(exporter, trace.WithBatchTimeout(time.Second)),
	), nil
}

func newMetricsProvider(ctx context.Context, cfg OTelConfig, res *resource.Resource) (*metric.MeterProvider, error) {
	var exporter metric.Exporter
	var err error

	if cfg.Protocol == otelProtocolHTTP {
		var opts []otlpmetrichttp.Option
		if cfg.Endpoint != "" {
			opts = append(opts, otlpmetrichttp.WithEndpointURL(cfg.Endpoint))
		}
		if cfg.Insecure {
			opts = append(opts, otlpmetrichttp.WithInsecure())
		}
		exporter, err = otlpmetrichttp.New(ctx, opts...)
	} else {
		var opts []otlpmetricgrpc.Option
		if cfg.Endpoint != "" {
			opts = append(opts, otlpmetricgrpc.WithEndpointURL(cfg.Endpoint))
		}
		if cfg.Insecure {
			opts = append(opts, otlpmetricgrpc.WithInsecure())
		}
		exporter, err = otlpmetricgrpc.New(ctx, opts...)
	}

	if err != nil {
		return nil, err
	}

	return metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metric.NewPeriodicReader(exporter, metric.WithInterval(30*time.Second))),
	), nil
}

func newLoggerProvider(ctx context.Context, cfg OTelConfig, res *resource.Resource) (*log.LoggerProvider, error) {
	var exporter log.Exporter
	var err error

	if cfg.Protocol == otelProtocolHTTP {
		var opts []otlploghttp.Option
		if cfg.Endpoint != "" {
			opts = append(opts, otlploghttp.WithEndpointURL(cfg.Endpoint))
		}
		if cfg.Insecure {
			opts = append(opts, otlploghttp.WithInsecure())
		}
		exporter, err = otlploghttp.New(ctx, opts...)
	} else {
		var opts []otlploggrpc.Option
		if cfg.Endpoint != "" {
			opts = append(opts, otlploggrpc.WithEndpointURL(cfg.Endpoint))
		}
		if cfg.Insecure {
			opts = append(opts, otlploggrpc.WithInsecure())
		}
		exporter, err = otlploggrpc.New(ctx, opts...)
	}

	if err != nil {
		return nil, err
	}

	return log.NewLoggerProvider(
		log.WithResource(res),
		log.WithProcessor(log.NewBatchProcessor(exporter)),
	), nil
}
