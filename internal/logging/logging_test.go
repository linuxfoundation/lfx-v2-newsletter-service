// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT

package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
)

func TestErrKeyConstant(t *testing.T) {
	if ErrKey != "error" {
		t.Errorf("expected ErrKey to be 'error', got %q", ErrKey)
	}
}

func TestAppendCtx(t *testing.T) {
	// Test with nil parent context
	attr := slog.String("key1", "value1")
	ctx := AppendCtx(context.TODO(), attr)

	if ctx == nil {
		t.Fatal("expected non-nil context")
	}

	// Check that the attribute was added
	if attrs, ok := ctx.Value(slogFields).([]slog.Attr); ok {
		if len(attrs) != 1 {
			t.Errorf("expected 1 attribute, got %d", len(attrs))
		}
		if attrs[0].Key != "key1" {
			t.Errorf("expected key 'key1', got %q", attrs[0].Key)
		}
		if attrs[0].Value.String() != "value1" {
			t.Errorf("expected value 'value1', got %q", attrs[0].Value.String())
		}
	} else {
		t.Error("expected slog attributes in context")
	}
}

func TestAppendCtx_WithParent(t *testing.T) {
	// Create parent context with existing attribute
	parentCtx := context.Background()
	attr1 := slog.String("parent_key", "parent_value")
	parentCtx = AppendCtx(parentCtx, attr1)

	// Add another attribute
	attr2 := slog.String("child_key", "child_value")
	childCtx := AppendCtx(parentCtx, attr2)

	// Check that both attributes are present
	if attrs, ok := childCtx.Value(slogFields).([]slog.Attr); ok {
		if len(attrs) != 2 {
			t.Errorf("expected 2 attributes, got %d", len(attrs))
		}

		// Check first attribute
		if attrs[0].Key != "parent_key" {
			t.Errorf("expected first key 'parent_key', got %q", attrs[0].Key)
		}
		if attrs[0].Value.String() != "parent_value" {
			t.Errorf("expected first value 'parent_value', got %q", attrs[0].Value.String())
		}

		// Check second attribute
		if attrs[1].Key != "child_key" {
			t.Errorf("expected second key 'child_key', got %q", attrs[1].Key)
		}
		if attrs[1].Value.String() != "child_value" {
			t.Errorf("expected second value 'child_value', got %q", attrs[1].Value.String())
		}
	} else {
		t.Error("expected slog attributes in context")
	}
}

func TestAppendCtx_MultipleAttributes(t *testing.T) {
	ctx := context.Background()

	// Add multiple attributes
	attr1 := slog.String("key1", "value1")
	attr2 := slog.Int("key2", 42)
	attr3 := slog.Bool("key3", true)

	ctx = AppendCtx(ctx, attr1)
	ctx = AppendCtx(ctx, attr2)
	ctx = AppendCtx(ctx, attr3)

	// Check all attributes are present
	if attrs, ok := ctx.Value(slogFields).([]slog.Attr); ok {
		if len(attrs) != 3 {
			t.Errorf("expected 3 attributes, got %d", len(attrs))
		}

		// Check each attribute
		expectedKeys := []string{"key1", "key2", "key3"}
		for i, expectedKey := range expectedKeys {
			if attrs[i].Key != expectedKey {
				t.Errorf("expected key[%d] %q, got %q", i, expectedKey, attrs[i].Key)
			}
		}
	} else {
		t.Error("expected slog attributes in context")
	}
}

func TestContextHandler_Handle(t *testing.T) {
	// Create a test handler that captures records
	var capturedRecord *slog.Record
	testHandler := &testSlogHandler{
		handleFunc: func(ctx context.Context, r slog.Record) error {
			capturedRecord = &r
			return nil
		},
	}

	handler := contextHandler{Handler: testHandler}

	// Create context with attributes
	ctx := context.Background()
	attr1 := slog.String("ctx_key", "ctx_value")
	ctx = AppendCtx(ctx, attr1)

	// Create a record and handle it
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "test message", 0)
	record.AddAttrs(slog.String("record_key", "record_value"))

	err := handler.Handle(ctx, record)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	if capturedRecord == nil {
		t.Fatal("expected record to be captured")
	}

	// The record should have been modified to include context attributes
	// Note: This is a basic test - in a real implementation, you'd need to
	// check that the attributes were actually added to the record
}

func TestInitStructureLogConfig_DefaultLevel(t *testing.T) {
	// Clear LOG_LEVEL environment variable
	originalLogLevel := os.Getenv("LOG_LEVEL")
	err := os.Unsetenv("LOG_LEVEL")
	if err != nil {
		t.Errorf("error unsetting LOG_LEVEL: %v", err)
		return
	}
	defer func() {
		if originalLogLevel != "" {
			err := os.Setenv("LOG_LEVEL", originalLogLevel)
			if err != nil {
				t.Errorf("error setting LOG_LEVEL: %v", err)
				return
			}
		}
	}()

	handler := InitStructureLogConfig()
	if handler == nil {
		t.Error("expected non-nil handler")
	}
}

func TestInitStructureLogConfig_WithLogLevel(t *testing.T) {
	testCases := []struct {
		name     string
		logLevel string
	}{
		{"debug level", "debug"},
		{"warn level", "warn"},
		{"error level", "error"},
		{"info level", "info"},
		{"unknown level", "unknown"},
	}

	originalLogLevel := os.Getenv("LOG_LEVEL")
	defer func() {
		if originalLogLevel != "" {
			err := os.Setenv("LOG_LEVEL", originalLogLevel)
			if err != nil {
				t.Errorf("error setting LOG_LEVEL: %v", err)
				return
			}
		} else {
			err := os.Unsetenv("LOG_LEVEL")
			if err != nil {
				t.Errorf("error unsetting LOG_LEVEL: %v", err)
				return
			}
		}
	}()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := os.Setenv("LOG_LEVEL", tc.logLevel)
			if err != nil {
				t.Errorf("error setting LOG_LEVEL: %v", err)
				return
			}
			handler := InitStructureLogConfig()
			if handler == nil {
				t.Error("expected non-nil handler")
			}
		})
	}
}

func TestInitStructureLogConfig_WithAddSource(t *testing.T) {
	testCases := []struct {
		name      string
		addSource string
	}{
		{"true", "true"},
		{"t", "t"},
		{"1", "1"},
		{"false", "false"},
		{"empty", ""},
	}

	originalAddSource := os.Getenv("LOG_ADD_SOURCE")
	defer func() {
		if originalAddSource != "" {
			err := os.Setenv("LOG_ADD_SOURCE", originalAddSource)
			if err != nil {
				t.Errorf("error setting LOG_ADD_SOURCE: %v", err)
				return
			}
		} else {
			err := os.Unsetenv("LOG_ADD_SOURCE")
			if err != nil {
				t.Errorf("error unsetting LOG_ADD_SOURCE: %v", err)
				return
			}
		}
	}()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := os.Setenv("LOG_ADD_SOURCE", tc.addSource)
			if err != nil {
				t.Errorf("error setting LOG_ADD_SOURCE: %v", err)
				return
			}
			handler := InitStructureLogConfig()
			if handler == nil {
				t.Error("expected non-nil handler")
			}
		})
	}
}

func TestInitStructureLogConfig_IncludesTraceAndSpanID(t *testing.T) {
	// Capture stdout to verify log output
	var buf bytes.Buffer
	originalStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	defer func() { _ = r.Close() }()
	os.Stdout = w
	defer func() { os.Stdout = originalStdout }()

	// Set up a trace provider
	prevTP := otel.GetTracerProvider()
	tp := trace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(prevTP)
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			t.Errorf("failed to shutdown trace provider: %v", err)
		}
	}()

	// Initialize logging (this sets up the slog-otel handler)
	InitStructureLogConfig()

	// Create a span and log within its context
	tracer := otel.Tracer("test-tracer")
	ctx, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	// Get the span context to verify IDs later
	spanCtx := span.SpanContext()
	expectedTraceID := spanCtx.TraceID().String()
	expectedSpanID := spanCtx.SpanID().String()

	// Log a message with the span context
	slog.InfoContext(ctx, "test log message with trace context")

	// Close writer and read captured output
	if err := w.Close(); err != nil {
		t.Fatalf("failed to close pipe writer: %v", err)
	}
	_, err = buf.ReadFrom(r)
	if err != nil {
		t.Fatalf("failed to read from pipe: %v", err)
	}

	// Parse the JSON log output
	logOutput := buf.String()
	if logOutput == "" {
		t.Fatal("expected log output, got empty string")
	}

	// Find the test log message in output (there may be multiple log lines)
	lines := bytes.Split(buf.Bytes(), []byte("\n"))
	var testLogLine map[string]interface{}
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var logEntry map[string]interface{}
		if err := json.Unmarshal(line, &logEntry); err != nil {
			continue
		}
		if msg, ok := logEntry["msg"].(string); ok && msg == "test log message with trace context" {
			testLogLine = logEntry
			break
		}
	}

	if testLogLine == nil {
		t.Fatalf("could not find test log message in output: %s", logOutput)
	}

	// Verify trace_id is present and matches
	traceID, ok := testLogLine["trace_id"].(string)
	if !ok {
		t.Errorf("expected trace_id in log output, got: %v", testLogLine)
	} else if traceID != expectedTraceID {
		t.Errorf("expected trace_id %q, got %q", expectedTraceID, traceID)
	}

	// Verify span_id is present and matches
	spanID, ok := testLogLine["span_id"].(string)
	if !ok {
		t.Errorf("expected span_id in log output, got: %v", testLogLine)
	} else if spanID != expectedSpanID {
		t.Errorf("expected span_id %q, got %q", expectedSpanID, spanID)
	}
}

// testSlogHandler is a helper for testing
type testSlogHandler struct {
	handleFunc func(context.Context, slog.Record) error
}

func (h *testSlogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return true
}

func (h *testSlogHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.handleFunc != nil {
		return h.handleFunc(ctx, r)
	}
	return nil
}

func (h *testSlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *testSlogHandler) WithGroup(name string) slog.Handler {
	return h
}
