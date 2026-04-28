package runner

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
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

func TestNormalizeOptions_Defaults(t *testing.T) {
	opts := NormalizeOptions(Options{})
	if opts.MaxAttempts != 1 {
		t.Errorf("MaxAttempts: got %d, want 1", opts.MaxAttempts)
	}
	if opts.ShutdownGrace != 10*time.Second {
		t.Errorf("ShutdownGrace: got %v, want 10s", opts.ShutdownGrace)
	}
	if opts.BaseDelay != 100*time.Millisecond {
		t.Errorf("BaseDelay: got %v, want 100ms", opts.BaseDelay)
	}
	if opts.MaxDelay != 1*time.Second {
		t.Errorf("MaxDelay: got %v, want 1s", opts.MaxDelay)
	}
}

func TestNormalizeOptions_PreservesExisting(t *testing.T) {
	opts := NormalizeOptions(Options{
		MaxAttempts:   5,
		ShutdownGrace: 30 * time.Second,
		BaseDelay:     500 * time.Millisecond,
		MaxDelay:      10 * time.Second,
	})
	if opts.MaxAttempts != 5 {
		t.Errorf("MaxAttempts: got %d, want 5", opts.MaxAttempts)
	}
	if opts.ShutdownGrace != 30*time.Second {
		t.Errorf("ShutdownGrace: got %v", opts.ShutdownGrace)
	}
}

func TestAttemptContext_NoTimeout(t *testing.T) {
	ctx, cancel := AttemptContext(context.Background(), 0)
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Error("expected no deadline for zero timeout")
	}
}

func TestAttemptContext_WithTimeout(t *testing.T) {
	ctx, cancel := AttemptContext(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Error("expected deadline for positive timeout")
	}
}

func TestSleepWithContext_CompletesNormally(t *testing.T) {
	err := SleepWithContext(context.Background(), 1*time.Millisecond)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSleepWithContext_CancelDuringSleep(t *testing.T) {
       ctx, cancel := context.WithCancel(context.Background())
       go func() {
	       time.Sleep(10 * time.Millisecond)
	       cancel()
       }()
       err := SleepWithContext(ctx, 10*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected Canceled, got %v", err)
	}
}

func TestBackoff_CapsAtMax(t *testing.T) {
	got := Backoff(10, 100*time.Millisecond, 1*time.Second)
	if got != 1*time.Second {
		t.Errorf("got %v, want 1s (capped)", got)
	}
}

func TestBackoff_Doubles(t *testing.T) {
	base := 100 * time.Millisecond
	attempt1 := Backoff(1, base, 10*time.Second)
	attempt2 := Backoff(2, base, 10*time.Second)
	if attempt2 != 2*attempt1 {
		t.Errorf("expected doubling: attempt1=%v attempt2=%v", attempt1, attempt2)
	}
}

func TestIsRetryable_NilError(t *testing.T) {
       if IsRetryable(nil) {
	       t.Error("nil error should not be retryable")
       }
}

func TestIsRetryable_ContextErrors(t *testing.T) {
       if IsRetryable(context.Canceled) {
	       t.Error("context.Canceled should not be retryable")
       }
       if IsRetryable(context.DeadlineExceeded) {
	       t.Error("context.DeadlineExceeded should not be retryable")
       }
}

func TestIsRetryable_ErrNotFound(t *testing.T) {
       if IsRetryable(exec.ErrNotFound) {
	       t.Error("ErrNotFound should not be retryable")
       }
}

func TestIsRetryable_TransientStrings(t *testing.T) {
       cases := []struct {
	       msg  string
	       want bool
       }{
	       {"resource temporarily unavailable", true},
	       {"connection reset by peer", true},
	       {"TEMPORARILY UNAVAILABLE", true},
	       {"CONNECTION RESET", true},
	       {"some other error", false},
       }
       for _, tc := range cases {
	       got := IsRetryable(errors.New(tc.msg))
	       if got != tc.want {
		       t.Errorf("IsRetryable(%q) = %v, want %v", tc.msg, got, tc.want)
	       }
       }
}
