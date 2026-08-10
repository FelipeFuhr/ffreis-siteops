package logx

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestNewWithEnv_DefaultTextInfo(t *testing.T) {
	var buf bytes.Buffer
	logger := newWithEnv("siteops", emptyEnv, &buf)

	logger.Info("hello", "component", "test")
	logger.Debug("debug-hidden")

	out := buf.String()
	if !strings.Contains(out, "level=INFO") {
		t.Fatalf("expected text INFO level log, got: %s", out)
	}
	if !strings.Contains(out, "service=siteops") {
		t.Fatalf("expected service field in output, got: %s", out)
	}
	if strings.Contains(out, "debug-hidden") {
		t.Fatalf("unexpected debug log at default info level: %s", out)
	}
}

func TestNewWithEnv_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	env := mapEnv(map[string]string{
		"LOG_FORMAT": "json",
		"LOG_LEVEL":  "debug",
	})
	logger := newWithEnv("siteops", env, &buf)
	logger.Debug("json-debug")

	out := buf.String()
	if !strings.Contains(out, "\"level\":\"DEBUG\"") {
		t.Fatalf("expected JSON debug level log, got: %s", out)
	}
	if !strings.Contains(out, "\"service\":\"siteops\"") {
		t.Fatalf("expected JSON service field, got: %s", out)
	}
}

func TestNewWithEnv_InvalidValuesWarnAndFallback(t *testing.T) {
	var buf bytes.Buffer
	env := mapEnv(map[string]string{
		"LOG_LEVEL":  "verbose",
		"LOG_FORMAT": "yaml",
		"LOG_SOURCE": "sometimes",
	})
	logger := newWithEnv("siteops", env, &buf)
	logger.Info("after-invalid")

	out := buf.String()
	for _, key := range []string{"LOG_LEVEL", "LOG_FORMAT", "LOG_SOURCE"} {
		if !strings.Contains(out, key) {
			t.Fatalf("expected warning for %s, got: %s", key, out)
		}
	}
	if !strings.Contains(out, "after-invalid") {
		t.Fatalf("expected info log after fallback, got: %s", out)
	}
}

func TestNewWithEnv_WarnLevel(t *testing.T) {
	var buf bytes.Buffer
	env := mapEnv(map[string]string{"LOG_LEVEL": "warn"})
	logger := newWithEnv("siteops", env, &buf)
	logger.Info("info-hidden")
	logger.Warn("warn-visible")
	out := buf.String()
	if strings.Contains(out, "info-hidden") {
		t.Error("info should be hidden at warn level")
	}
	if !strings.Contains(out, "warn-visible") {
		t.Error("warn should be visible at warn level")
	}
}

func TestNewWithEnv_ErrorLevel(t *testing.T) {
	var buf bytes.Buffer
	env := mapEnv(map[string]string{"LOG_LEVEL": "error"})
	logger := newWithEnv("siteops", env, &buf)
	logger.Warn("warn-hidden")
	logger.Error("error-visible")
	out := buf.String()
	if strings.Contains(out, "warn-hidden") {
		t.Error("warn should be hidden at error level")
	}
	if !strings.Contains(out, "error-visible") {
		t.Error("error should be visible at error level")
	}
}

func TestNewWithEnv_SourceEnabled(t *testing.T) {
	var buf bytes.Buffer
	env := mapEnv(map[string]string{"LOG_SOURCE": "true"})
	logger := newWithEnv("siteops", env, &buf)
	logger.Info("with-source")
	if !strings.Contains(buf.String(), "with-source") {
		t.Error("expected log output with source enabled")
	}
}

func TestNewWithEnv_WarningLevel(t *testing.T) {
	var buf bytes.Buffer
	env := mapEnv(map[string]string{"LOG_LEVEL": "warning"})
	logger := newWithEnv("siteops", env, &buf)
	logger.Info("info-hidden")
	logger.Warn("warn-visible")
	out := buf.String()
	if strings.Contains(out, "info-hidden") {
		t.Error("info should be hidden at warning level")
	}
}

// ── New (reads the real process environment) ───────────────────────────────

func TestNew_LogLevelDebugEnablesDebugRecords(t *testing.T) {
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_FORMAT", "")
	t.Setenv("LOG_SOURCE", "")

	logger := New("siteops")

	if logger == nil {
		t.Fatal("New returned a nil logger")
	}
	if !logger.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("expected debug records to be enabled when LOG_LEVEL=debug")
	}
}

func TestNew_UnsetLogLevelDefaultsToInfo(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_FORMAT", "")
	t.Setenv("LOG_SOURCE", "")

	logger := New("siteops")

	ctx := context.Background()
	if logger.Enabled(ctx, slog.LevelDebug) {
		t.Error("debug records should be disabled at the default level")
	}
	if !logger.Enabled(ctx, slog.LevelInfo) {
		t.Error("info records should be enabled at the default level")
	}
}

// ── errInvalidValue message ────────────────────────────────────────────────

func TestParseFormat_UnsupportedValueReportsInvalidValue(t *testing.T) {
	_, err := parseFormat("yaml")
	if err == nil {
		t.Fatal("expected an error for an unsupported log format")
	}
	if got := err.Error(); got != "invalid value" {
		t.Errorf("err.Error() = %q, want %q", got, "invalid value")
	}
}

func emptyEnv(string) string {
	return ""
}

func mapEnv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}
