package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type Command struct {
	Name   string
	Args   []string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type Options struct {
	Timeout       time.Duration
	ShutdownGrace time.Duration
	MaxAttempts   int
	BaseDelay     time.Duration
	MaxDelay      time.Duration
}

var Run = func(ctx context.Context, logger *slog.Logger, cmd Command, opts Options) error {
	opts = NormalizeOptions(opts)

	var lastErr error
	for attempt := 1; attempt <= opts.MaxAttempts; attempt++ {
		attemptCtx, cancel := AttemptContext(ctx, opts.Timeout)
		err := RunOnce(attemptCtx, cmd, opts.ShutdownGrace)
		cancel()
		if err == nil {
			return nil
		}

		lastErr = err
		if stop, stopErr := ShouldStopRetry(ctx, err, attempt, opts.MaxAttempts); stop {
			if stopErr != nil {
				return stopErr
			}
			return err
		}

		delay := Backoff(attempt, opts.BaseDelay, opts.MaxDelay)
		logger.Warn("command failed; retrying", "command", cmd.Name, "attempt", attempt, "max_attempts", opts.MaxAttempts, "delay", delay.String(), "error", err)

		if err := SleepWithContext(ctx, delay); err != nil {
			return err
		}
	}
	return lastErr
}

var NormalizeOptions = func(opts Options) Options {
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 1
	}
	if opts.ShutdownGrace <= 0 {
		opts.ShutdownGrace = 10 * time.Second
	}
	if opts.BaseDelay <= 0 {
		opts.BaseDelay = 100 * time.Millisecond
	}
	if opts.MaxDelay <= 0 {
		opts.MaxDelay = 1 * time.Second
	}
	return opts
}

var AttemptContext = func(parent context.Context, timeout time.Duration) (context.Context, func()) {
	if timeout <= 0 {
		return parent, func() { /* no timeout to cancel */ }
	}
	return context.WithTimeout(parent, timeout)
}

var ShouldStopRetry = func(ctx context.Context, err error, attempt, maxAttempts int) (bool, error) {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return true, ctx.Err()
	}
	if attempt >= maxAttempts {
		return true, nil
	}
	return !IsRetryable(err), nil
}

var SleepWithContext = func(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// SECURITY: Command.Name and Command.Args must not contain untrusted user input.
// These values should be set only from trusted sources or validated before use.
// If user input is allowed, sanitize or restrict allowed commands.
var AllowedCommands = []string{"echo", "aws", "docker-compose", "siteops-compiler", "website-compiler-cli", "mock-website-compiler.sh"}

var RunOnce = func(ctx context.Context, c Command, grace time.Duration) error {
	// Basic validation: prevent empty or obviously dangerous command names
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("command name must not be empty")
	}
	if strings.ContainsAny(c.Name, ";&|$") {
		return fmt.Errorf("command name contains potentially dangerous characters: %q", c.Name)
	}
	allowed := false
	for _, cmd := range AllowedCommands {
		if c.Name == cmd {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("command %q is not in the allowed whitelist", c.Name)
	}
	path, err := exec.LookPath(c.Name)
	if err != nil {
		return fmt.Errorf("resolving command %q: %w", c.Name, err)
	}
	command := &exec.Cmd{
		Path:   path,
		Args:   append([]string{c.Name}, c.Args...),
		Env:    c.Env,
		Stdin:  c.Stdin,
		Stdout: c.Stdout,
		Stderr: c.Stderr,
	}

	// nosemgrep: go.lang.security.audit.dangerous-exec-cmd.dangerous-exec-cmd
	// safe: path and args are internally controlled, not user input
	if err := command.Start(); err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		if command.Process != nil {
			_ = command.Process.Signal(syscall.SIGTERM)
		}

		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case err := <-done:
			if err != nil {
				return fmt.Errorf("command canceled: %w", ctx.Err())
			}
			return ctx.Err()
		case <-timer.C:
			if command.Process != nil {
				_ = command.Process.Kill()
			}
			<-done
			return fmt.Errorf("command killed after timeout: %w", ctx.Err())
		}
	}
}

var IsRetryable = func(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, exec.ErrNotFound) {
		return false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			switch status.ExitStatus() {
			case 125, 137, 143:
				return true
			default:
				return false
			}
		}
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "temporarily unavailable") || strings.Contains(lower, "connection reset") {
		return true
	}
	return false
}

var Backoff = func(attempt int, base, max time.Duration) time.Duration {
	delay := base * time.Duration(1<<(attempt-1))
	if delay > max {
		return max
	}
	return delay
}
