package runner

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRun_RetriesTransientFailure(t *testing.T) {
	dir := t.TempDir()
	flagFile := filepath.Join(dir, "first-run.flag")

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	cmd := Command{
		Name: "bash",
		Args: []string{
			"-lc",
			"if [ ! -f '" + flagFile + "' ]; then touch '" + flagFile + "'; exit 125; fi; exit 0",
		},
	}

	err := Run(context.Background(), logger, cmd, Options{
		Timeout:       3 * time.Second,
		ShutdownGrace: 200 * time.Millisecond,
		MaxAttempts:   2,
		BaseDelay:     10 * time.Millisecond,
		MaxDelay:      20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("expected retry to succeed, got: %v", err)
	}
}

func TestRun_TimeoutKillsCommand(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	cmd := Command{
		Name: "bash",
		Args: []string{"-lc", "sleep 2"},
	}

	err := Run(context.Background(), logger, cmd, Options{
		Timeout:       100 * time.Millisecond,
		ShutdownGrace: 50 * time.Millisecond,
		MaxAttempts:   1,
		BaseDelay:     1 * time.Millisecond,
		MaxDelay:      2 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestRun_ContextCancelStopsExecution(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cmd := Command{
		Name: "bash",
		Args: []string{"-lc", "sleep 2"},
	}
	err := Run(ctx, logger, cmd, Options{
		Timeout:       1 * time.Second,
		ShutdownGrace: 50 * time.Millisecond,
		MaxAttempts:   3,
		BaseDelay:     10 * time.Millisecond,
		MaxDelay:      20 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected canceled error")
	}
}

func TestRun_NonRetryableNotFound(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	err := Run(context.Background(), logger, Command{Name: "definitely-not-a-real-binary"}, Options{
		MaxAttempts: 3,
	})
	if err == nil {
		t.Fatal("expected command not found error")
	}
	if _, statErr := os.Stat("definitely-not-a-real-binary"); statErr == nil {
		t.Fatal("sanity check failed: command unexpectedly exists")
	}
}
