// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package utils

import (
	"context"

	"os"
	"testing"
)

// TestOTelConfigFromEnv_Defaults verifies that OTelConfigFromEnv returns
// sensible default values when no environment variables are set.
func TestOTelConfigFromEnv_Defaults(t *testing.T) {
	// Clear all relevant environment variables
	envVars := []string{
		"OTEL_SERVICE_NAME",
		"OTEL_SERVICE_VERSION",
		"OTEL_EXPORTER_OTLP_PROTOCOL",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_INSECURE",
		"OTEL_TRACES_EXPORTER",
		"OTEL_TRACES_SAMPLE_RATIO",
		"OTEL_METRICS_EXPORTER",
		"OTEL_LOGS_EXPORTER",
	}
	for _, env := range envVars {
		_ = os.Unsetenv(env)
	}

	cfg := OTelConfigFromEnv()

	if cfg.ServiceName != "lfx-v2-meeting-service" {
		t.Errorf("expected default ServiceName 'lfx-v2-meeting-service', got %q", cfg.ServiceName)
	}
	if cfg.ServiceVersion != "" {
		t.Errorf("expected empty ServiceVersion, got %q", cfg.ServiceVersion)
	}
	if cfg.Protocol != OTelProtocolGRPC {
		t.Errorf("expected default Protocol %q, got %q", OTelProtocolGRPC, cfg.Protocol)
	}
	if cfg.Endpoint != "" {
		t.Errorf("expected empty Endpoint, got %q", cfg.Endpoint)
	}
	if cfg.Insecure != false {
		t.Errorf("expected Insecure false, got %t", cfg.Insecure)
	}
	if cfg.TracesExporter != OTelExporterNone {
		t.Errorf("expected default TracesExporter %q, got %q", OTelExporterNone, cfg.TracesExporter)
	}
	if cfg.TracesSampleRatio != 1.0 {
		t.Errorf("expected default TracesSampleRatio 1.0, got %f", cfg.TracesSampleRatio)
	}
	if cfg.MetricsExporter != OTelExporterNone {
		t.Errorf("expected default MetricsExporter %q, got %q", OTelExporterNone, cfg.MetricsExporter)
	}
	if cfg.LogsExporter != OTelExporterNone {
		t.Errorf("expected default LogsExporter %q, got %q", OTelExporterNone, cfg.LogsExporter)
	}
}

// TestOTelConfigFromEnv_CustomValues verifies that OTelConfigFromEnv correctly
// reads and parses all supported OTEL_* environment variables.
func TestOTelConfigFromEnv_CustomValues(t *testing.T) {
	// Set all environment variables
	_ = os.Setenv("OTEL_SERVICE_NAME", "test-service")
	_ = os.Setenv("OTEL_SERVICE_VERSION", "1.2.3")
	_ = os.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http")
	_ = os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4318")
	_ = os.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")
	_ = os.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	_ = os.Setenv("OTEL_TRACES_SAMPLE_RATIO", "0.5")
	_ = os.Setenv("OTEL_METRICS_EXPORTER", "otlp")
	_ = os.Setenv("OTEL_LOGS_EXPORTER", "otlp")

	defer func() {
		_ = os.Unsetenv("OTEL_SERVICE_NAME")
		_ = os.Unsetenv("OTEL_SERVICE_VERSION")
		_ = os.Unsetenv("OTEL_EXPORTER_OTLP_PROTOCOL")
		_ = os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")
		_ = os.Unsetenv("OTEL_EXPORTER_OTLP_INSECURE")
		_ = os.Unsetenv("OTEL_TRACES_EXPORTER")
		_ = os.Unsetenv("OTEL_TRACES_SAMPLE_RATIO")
		_ = os.Unsetenv("OTEL_METRICS_EXPORTER")
		_ = os.Unsetenv("OTEL_LOGS_EXPORTER")
	}()

	cfg := OTelConfigFromEnv()

	if cfg.ServiceName != "test-service" {
		t.Errorf("expected ServiceName 'test-service', got %q", cfg.ServiceName)
	}
	if cfg.ServiceVersion != "1.2.3" {
		t.Errorf("expected ServiceVersion '1.2.3', got %q", cfg.ServiceVersion)
	}
	if cfg.Protocol != OTelProtocolHTTP {
		t.Errorf("expected Protocol %q, got %q", OTelProtocolHTTP, cfg.Protocol)
	}
	if cfg.Endpoint != "localhost:4318" {
		t.Errorf("expected Endpoint 'localhost:4318', got %q", cfg.Endpoint)
	}
	if cfg.Insecure != true {
		t.Errorf("expected Insecure true, got %t", cfg.Insecure)
	}
	if cfg.TracesExporter != OTelExporterOTLP {
		t.Errorf("expected TracesExporter %q, got %q", OTelExporterOTLP, cfg.TracesExporter)
	}
	if cfg.TracesSampleRatio != 0.5 {
		t.Errorf("expected TracesSampleRatio 0.5, got %f", cfg.TracesSampleRatio)
	}
	if cfg.MetricsExporter != OTelExporterOTLP {
		t.Errorf("expected MetricsExporter %q, got %q", OTelExporterOTLP, cfg.MetricsExporter)
	}
	if cfg.LogsExporter != OTelExporterOTLP {
		t.Errorf("expected LogsExporter %q, got %q", OTelExporterOTLP, cfg.LogsExporter)
	}
}

// TestOTelConfigFromEnv_TracesSampleRatio tests the parsing and validation of
// the OTEL_TRACES_SAMPLE_RATIO environment variable, including edge cases like
// invalid values, out-of-range numbers, and empty strings.
func TestOTelConfigFromEnv_TracesSampleRatio(t *testing.T) {
	tests := []struct {
		name          string
		envValue      string
		expectedRatio float64
	}{
		{"valid zero", "0.0", 0.0},
		{"valid half", "0.5", 0.5},
		{"valid one", "1.0", 1.0},
		{"valid small", "0.01", 0.01},
		{"invalid negative", "-0.5", 1.0},      // defaults to 1.0
		{"invalid above one", "1.5", 1.0},      // defaults to 1.0
		{"invalid non-number", "invalid", 1.0}, // defaults to 1.0
		{"empty string", "", 1.0},              // defaults to 1.0
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear and set the env var
			_ = os.Unsetenv("OTEL_TRACES_SAMPLE_RATIO")
			if tt.envValue != "" {
				_ = os.Setenv("OTEL_TRACES_SAMPLE_RATIO", tt.envValue)
			}
			defer func() { _ = os.Unsetenv("OTEL_TRACES_SAMPLE_RATIO") }()

			cfg := OTelConfigFromEnv()

			if cfg.TracesSampleRatio != tt.expectedRatio {
				t.Errorf("expected TracesSampleRatio %f, got %f", tt.expectedRatio, cfg.TracesSampleRatio)
			}
		})
	}
}

// TestOTelConfigFromEnv_InsecureFlag tests the parsing of the
// OTEL_EXPORTER_OTLP_INSECURE environment variable. Only the literal string
// "true" enables insecure mode; all other values default to false.
func TestOTelConfigFromEnv_InsecureFlag(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected bool
	}{
		{"true", "true", true},
		{"false", "false", false},
		{"empty", "", false},
		{"TRUE uppercase", "TRUE", false}, // only "true" is recognized
		{"1", "1", false},                 // only "true" is recognized
		{"yes", "yes", false},             // only "true" is recognized
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.Unsetenv("OTEL_EXPORTER_OTLP_INSECURE")
			if tt.envValue != "" {
				_ = os.Setenv("OTEL_EXPORTER_OTLP_INSECURE", tt.envValue)
			}
			defer func() { _ = os.Unsetenv("OTEL_EXPORTER_OTLP_INSECURE") }()

			cfg := OTelConfigFromEnv()

			if cfg.Insecure != tt.expected {
				t.Errorf("expected Insecure %t, got %t", tt.expected, cfg.Insecure)
			}
		})
	}
}

// TestSetupOTelSDKWithConfig_AllDisabled verifies that the SDK can be
// initialized successfully when all exporters (traces, metrics, logs) are
// disabled, and that the returned shutdown function works correctly.
func TestSetupOTelSDKWithConfig_AllDisabled(t *testing.T) {
	cfg := OTelConfig{
		ServiceName:       "test-service",
		ServiceVersion:    "1.0.0",
		Protocol:          OTelProtocolGRPC,
		TracesExporter:    OTelExporterNone,
		TracesSampleRatio: 1.0,
		MetricsExporter:   OTelExporterNone,
		LogsExporter:      OTelExporterNone,
	}

	ctx := context.Background()
	shutdown, err := SetupOTelSDKWithConfig(ctx, cfg)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if shutdown == nil {
		t.Fatal("expected non-nil shutdown function")
	}

	// Call shutdown to ensure it works without error
	err = shutdown(ctx)
	if err != nil {
		t.Errorf("shutdown returned unexpected error: %v", err)
	}
}

// TestSetupOTelSDKWithConfig_ShutdownIdempotent verifies that the shutdown
// function can be called multiple times without error. This is important for
// graceful shutdown scenarios where shutdown may be triggered multiple times.
func TestSetupOTelSDKWithConfig_ShutdownIdempotent(t *testing.T) {
	cfg := OTelConfig{
		ServiceName:       "test-service",
		ServiceVersion:    "1.0.0",
		Protocol:          OTelProtocolGRPC,
		TracesExporter:    OTelExporterNone,
		TracesSampleRatio: 1.0,
		MetricsExporter:   OTelExporterNone,
		LogsExporter:      OTelExporterNone,
	}

	ctx := context.Background()
	shutdown, err := SetupOTelSDKWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Call shutdown multiple times
	err = shutdown(ctx)
	if err != nil {
		t.Errorf("first shutdown returned unexpected error: %v", err)
	}

	// Second call should also succeed (shutdownFuncs is cleared)
	err = shutdown(ctx)
	if err != nil {
		t.Errorf("second shutdown returned unexpected error: %v", err)
	}
}

// TestNewResource verifies that newResource creates a valid OpenTelemetry
// resource with the expected service.name attribute for various input values,
// including edge cases like empty versions and unicode characters.
func TestNewResource(t *testing.T) {
	tests := []struct {
		name           string
		serviceName    string
		serviceVersion string
	}{
		{"basic", "test-service", "1.0.0"},
		{"empty version", "test-service", ""},
		{"unicode name", "测试服务", "2.0.0"},
		{"special chars", "test-service-123", "1.0.0-beta.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := OTelConfig{
				ServiceName:    tt.serviceName,
				ServiceVersion: tt.serviceVersion,
			}

			res, err := newResource(cfg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if res == nil {
				t.Fatal("expected non-nil resource")
			}

			// Verify resource contains expected attributes
			attrs := res.Attributes()
			found := false
			for _, attr := range attrs {
				if string(attr.Key) == "service.name" && attr.Value.AsString() == tt.serviceName {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("resource missing service.name attribute with value %q", tt.serviceName)
			}
		})
	}
}

// TestNewPropagator verifies that newPropagator returns a composite
// TextMapPropagator that includes the standard W3C trace context fields
// (traceparent, tracestate) and baggage propagation.
func TestNewPropagator(t *testing.T) {
	prop := newPropagator()

	if prop == nil {
		t.Fatal("expected non-nil propagator")
	}

	// Verify it's a composite propagator with expected fields
	fields := prop.Fields()
	if len(fields) == 0 {
		t.Error("expected propagator to have fields")
	}

	// Check for expected propagation fields (traceparent, tracestate, baggage)
	expectedFields := map[string]bool{
		"traceparent": false,
		"tracestate":  false,
		"baggage":     false,
	}

	for _, field := range fields {
		expectedFields[field] = true
	}

	for field, found := range expectedFields {
		if !found {
			t.Errorf("expected propagator to include field %q", field)
		}
	}
}

// TestOTelConstants verifies that the exported OTel constants have their
// expected string values, ensuring API compatibility.
func TestOTelConstants(t *testing.T) {
	// Verify constants have expected values
	if OTelProtocolGRPC != "grpc" {
		t.Errorf("expected OTelProtocolGRPC to be 'grpc', got %q", OTelProtocolGRPC)
	}
	if OTelProtocolHTTP != "http" {
		t.Errorf("expected OTelProtocolHTTP to be 'http', got %q", OTelProtocolHTTP)
	}
	if OTelExporterOTLP != "otlp" {
		t.Errorf("expected OTelExporterOTLP to be 'otlp', got %q", OTelExporterOTLP)
	}
	if OTelExporterNone != "none" {
		t.Errorf("expected OTelExporterNone to be 'none', got %q", OTelExporterNone)
	}
}

// TestSetupOTelSDK tests the convenience function SetupOTelSDK which reads
// configuration from environment variables. With no env vars set, it should
// use defaults and successfully initialize the SDK.
func TestSetupOTelSDK(t *testing.T) {
	// Clear environment to use defaults
	envVars := []string{
		"OTEL_SERVICE_NAME",
		"OTEL_SERVICE_VERSION",
		"OTEL_EXPORTER_OTLP_PROTOCOL",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_INSECURE",
		"OTEL_TRACES_EXPORTER",
		"OTEL_TRACES_SAMPLE_RATIO",
		"OTEL_METRICS_EXPORTER",
		"OTEL_LOGS_EXPORTER",
	}
	for _, env := range envVars {
		_ = os.Unsetenv(env)
	}

	ctx := context.Background()
	shutdown, err := SetupOTelSDK(ctx)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if shutdown == nil {
		t.Fatal("expected non-nil shutdown function")
	}

	err = shutdown(ctx)
	if err != nil {
		t.Errorf("shutdown returned unexpected error: %v", err)
	}
}

// TestOTelConfig_ZeroValue documents that a zero-value OTelConfig does not
// have exporters disabled by default. Users must explicitly set exporters to
// OTelExporterNone to disable them.
func TestOTelConfig_ZeroValue(t *testing.T) {
	// Test that zero-value config (with empty strings) tries to enable exporters
	// because empty string != "none". This verifies the expected behavior that
	// users should explicitly set exporters to "none" to disable them.
	cfg := OTelConfig{}

	// Verify the zero-value behavior: empty string fields mean exporters would be enabled
	if cfg.TracesExporter == OTelExporterNone {
		t.Error("expected zero-value TracesExporter to NOT equal OTelExporterNone")
	}
	if cfg.MetricsExporter == OTelExporterNone {
		t.Error("expected zero-value MetricsExporter to NOT equal OTelExporterNone")
	}
	if cfg.LogsExporter == OTelExporterNone {
		t.Error("expected zero-value LogsExporter to NOT equal OTelExporterNone")
	}
}

// TestOTelConfig_MinimalConfig verifies that the SDK can be initialized with
// a minimal configuration where only the exporter settings are specified.
func TestOTelConfig_MinimalConfig(t *testing.T) {
	// Test minimal config with all exporters explicitly disabled
	cfg := OTelConfig{
		TracesExporter:  OTelExporterNone,
		MetricsExporter: OTelExporterNone,
		LogsExporter:    OTelExporterNone,
	}

	ctx := context.Background()
	shutdown, err := SetupOTelSDKWithConfig(ctx, cfg)

	if err != nil {
		t.Fatalf("unexpected error with minimal config: %v", err)
	}

	if shutdown == nil {
		t.Fatal("expected non-nil shutdown function")
	}

	err = shutdown(ctx)
	if err != nil {
		t.Errorf("shutdown returned unexpected error: %v", err)
	}
}

// TestEndpointURL verifies that endpointURL prepends the correct scheme
// when missing and preserves existing schemes.
func TestEndpointURL(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		insecure bool
		want     string
	}{
		{
			name:     "IP:port insecure",
			raw:      "127.0.0.1:4317",
			insecure: true,
			want:     "http://127.0.0.1:4317",
		},
		{
			name:     "IP:port secure",
			raw:      "127.0.0.1:4317",
			insecure: false,
			want:     "https://127.0.0.1:4317",
		},
		{
			name:     "localhost:port insecure",
			raw:      "localhost:4317",
			insecure: true,
			want:     "http://localhost:4317",
		},
		{
			name:     "hostname without port",
			raw:      "collector",
			insecure: true,
			want:     "http://collector",
		},
		{
			name:     "http URL preserved",
			raw:      "http://collector.example.com:4318",
			insecure: false,
			want:     "http://collector.example.com:4318",
		},
		{
			name:     "https URL preserved",
			raw:      "https://collector.example.com:4318",
			insecure: true,
			want:     "https://collector.example.com:4318",
		},
		{
			name:     "https URL with path preserved",
			raw:      "https://collector.example.com:4318/v1/traces",
			insecure: false,
			want:     "https://collector.example.com:4318/v1/traces",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := endpointURL(tt.raw, tt.insecure)
			if got != tt.want {
				t.Errorf("endpointURL(%q, %t) = %q, want %q", tt.raw, tt.insecure, got, tt.want)
			}
		})
	}
}

// TestSetupOTelSDKWithConfig_IPEndpoint verifies that SetupOTelSDKWithConfig
// normalizes a bare IP:port endpoint to include a scheme, preventing the
// "first path segment in URL cannot contain colon" error from the SDK.
func TestSetupOTelSDKWithConfig_IPEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "127.0.0.1:4317")

	cfg := OTelConfig{
		ServiceName:       "test-service",
		ServiceVersion:    "1.0.0",
		Protocol:          OTelProtocolGRPC,
		Endpoint:          "127.0.0.1:4317",
		Insecure:          true,
		TracesExporter:    OTelExporterOTLP,
		TracesSampleRatio: 1.0,
		MetricsExporter:   OTelExporterNone,
		LogsExporter:      OTelExporterNone,
	}

	ctx := context.Background()
	shutdown, err := SetupOTelSDKWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if shutdown == nil {
		t.Fatal("expected non-nil shutdown function")
	}

	_ = shutdown(ctx)
}
