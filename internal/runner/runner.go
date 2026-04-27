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

func Run(ctx context.Context, logger *slog.Logger, cmd Command, opts Options) error {
	opts = normalizeOptions(opts)

	var lastErr error
	for attempt := 1; attempt <= opts.MaxAttempts; attempt++ {
		attemptCtx, cancel := attemptContext(ctx, opts.Timeout)
		err := runOnce(attemptCtx, cmd, opts.ShutdownGrace)
		cancel()
		if err == nil {
			return nil
		}

		lastErr = err
		if stop, stopErr := shouldStopRetry(ctx, err, attempt, opts.MaxAttempts); stop {
			if stopErr != nil {
				return stopErr
			}
			return err
		}

		delay := backoff(attempt, opts.BaseDelay, opts.MaxDelay)
		logger.Warn("command failed; retrying", "command", cmd.Name, "attempt", attempt, "max_attempts", opts.MaxAttempts, "delay", delay.String(), "error", err)

		if err := sleepWithContext(ctx, delay); err != nil {
			return err
		}
	}
	return lastErr
}

func normalizeOptions(opts Options) Options {
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

func attemptContext(parent context.Context, timeout time.Duration) (context.Context, func()) {
		       if timeout <= 0 {
			       // No timeout specified, so return the parent context and a no-op cancel function.
			       // This is intentional: the caller expects a cancel function for symmetry.
			       // The empty function below is a placeholder to match the context.WithTimeout signature.
			       return parent, func() {} // no-op cancel function
		       }
	return context.WithTimeout(parent, timeout)
}

func shouldStopRetry(ctx context.Context, err error, attempt, maxAttempts int) (bool, error) {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return true, ctx.Err()
	}
	if attempt >= maxAttempts {
		return true, nil
	}
	return !isRetryable(err), nil
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func runOnce(ctx context.Context, c Command, grace time.Duration) error {
	// Validate command name and arguments to mitigate code injection risk
	if c.Name == "" {
		return fmt.Errorf("command name must not be empty")
	}
	if strings.ContainsAny(c.Name, "|;&><$`\"'\n\r") {
		return fmt.Errorf("command name contains potentially dangerous characters")
	}
	for _, arg := range c.Args {
		if strings.ContainsAny(arg, "|;&><$`\"'\n\r") {
			return fmt.Errorf("command argument contains potentially dangerous characters: %q", arg)
		}
	}

	command := exec.Command(c.Name, c.Args...)
	command.Env = c.Env
	command.Stdin = c.Stdin
	command.Stdout = c.Stdout
	command.Stderr = c.Stderr

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

func isRetryable(err error) bool {
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

func backoff(attempt int, base, max time.Duration) time.Duration {
	delay := base * time.Duration(1<<(attempt-1))
	if delay > max {
		return max
	}
	return delay
}
