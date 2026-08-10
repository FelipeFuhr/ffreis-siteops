package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// waitForTick creates a fresh file on every poll until the watcher emits a
// tick or the deadline expires. Creating a new file always changes the
// snapshot's cardinality, so it is a change the watcher cannot miss — unlike
// rewriting a file, which can land inside the same mtime tick.
func waitForTick(t *testing.T, dir string, out <-chan struct{}, poll, timeout time.Duration) bool {
	t.Helper()
	deadline := time.After(timeout)
	for i := 0; ; i++ {
		path := filepath.Join(dir, fmt.Sprintf("change-%d.txt", i))
		writeFile(t, path, "touched")
		select {
		case <-out:
			return true
		case <-deadline:
			return false
		case <-time.After(poll):
		}
	}
}

func TestFileWatcher_Run_EmitsTickAfterFileTreeChanges(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "baseline.txt"), "baseline")

	out := make(chan struct{}, 1)
	w := &FileWatcher{
		Paths:    []string{dir},
		Interval: 5 * time.Millisecond,
		Debounce: 5 * time.Millisecond,
		Out:      out,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	if !waitForTick(t, dir, out, 20*time.Millisecond, 10*time.Second) {
		t.Fatal("watcher emitted no tick after the watched tree changed")
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of ctx cancel")
	}
}

func TestFileWatcher_Run_UnchangedTreeEmitsNoTickAndCancelReturnsContextError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "stable.txt"), "stable")

	out := make(chan struct{}, 1)
	w := &FileWatcher{
		Paths:    []string{dir},
		Interval: 5 * time.Millisecond,
		Debounce: 5 * time.Millisecond,
		Out:      out,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// Long enough for many poll intervals to observe an unchanged snapshot.
	select {
	case <-out:
		t.Fatal("watcher emitted a tick for an unchanged tree")
	case <-time.After(100 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of ctx cancel")
	}
}

func TestFileWatcher_Run_LongDebounceCoalescesBurstAndCancelStopsPendingTimer(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "baseline.txt"), "baseline")

	out := make(chan struct{}, 1)
	w := &FileWatcher{
		Paths:    []string{dir},
		Interval: 5 * time.Millisecond,
		// Far longer than the burst below, so every change re-arms the timer
		// and none of them ever fires.
		Debounce: 30 * time.Second,
		Out:      out,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// A sustained burst: each new file is a distinct change, so the poll loop
	// re-arms the debounce timer many times over.
	for i := 0; i < 20; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("burst-%d.txt", i)), "burst")
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case <-out:
		t.Fatal("watcher emitted a tick before the debounce window elapsed")
	default:
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of ctx cancel")
	}

	// The pending timer must have been stopped on cancel, not left to fire.
	select {
	case <-out:
		t.Error("debounce timer fired after the watcher was cancelled")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestFileWatcher_Run_ZeroIntervalAndDebounceUseDefaults(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "baseline.txt"), "baseline")

	out := make(chan struct{}, 1)
	w := &FileWatcher{Paths: []string{dir}, Out: out} // Interval/Debounce unset

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// Defaults are a 500ms poll plus a 200ms debounce, so allow ample time.
	if !waitForTick(t, dir, out, 250*time.Millisecond, 15*time.Second) {
		t.Fatal("watcher with default interval/debounce emitted no tick")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of ctx cancel")
	}
}

// ── snapshot ───────────────────────────────────────────────────────────────

func TestFileWatcher_Snapshot_RecordsFilesAndSkipsNoiseDirectories(t *testing.T) {
	root := t.TempDir()
	tracked := filepath.Join(root, "src", "index.gohtml")
	writeFile(t, tracked, "<html>")
	for _, skipped := range []string{"vendor", ".git", "node_modules", "dist"} {
		writeFile(t, filepath.Join(root, skipped, "ignored.txt"), "noise")
	}

	w := &FileWatcher{Paths: []string{root}}
	got := w.snapshot()

	if _, ok := got[tracked]; !ok {
		t.Errorf("snapshot is missing the tracked file %q; got %v", tracked, got)
	}
	if len(got) != 1 {
		t.Errorf("snapshot recorded %d files, want only the tracked one: %v", len(got), got)
	}
}

func TestFileWatcher_Snapshot_DescendsIntoASkipNamedRoot(t *testing.T) {
	// A watched root may legitimately be called "dist"; the skip list applies
	// to nested directories only.
	parent := t.TempDir()
	root := filepath.Join(parent, "dist")
	tracked := filepath.Join(root, "index.html")
	writeFile(t, tracked, "<html>")

	w := &FileWatcher{Paths: []string{root}}
	got := w.snapshot()

	if _, ok := got[tracked]; !ok {
		t.Errorf("snapshot skipped the watched root itself; got %v", got)
	}
}

func TestFileWatcher_Snapshot_MissingPathIsIgnored(t *testing.T) {
	w := &FileWatcher{Paths: []string{filepath.Join(t.TempDir(), "does-not-exist")}}

	got := w.snapshot()

	if len(got) != 0 {
		t.Errorf("snapshot of a missing path = %v, want empty", got)
	}
}

func TestFileWatcher_Snapshot_ReflectsFileModificationTimes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	writeFile(t, path, "first")

	want := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, want, want); err != nil {
		t.Fatalf("os.Chtimes: %v", err)
	}

	w := &FileWatcher{Paths: []string{root}}
	got := w.snapshot()

	if !got[path].Equal(want) {
		t.Errorf("snapshot mtime = %v, want %v", got[path], want)
	}
}

// ── snapshotEqual ──────────────────────────────────────────────────────────

func TestSnapshotEqual(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	later := base.Add(time.Second)

	cases := []struct {
		name string
		a, b map[string]time.Time
		want bool
	}{
		{"both empty", map[string]time.Time{}, map[string]time.Time{}, true},
		{"identical", map[string]time.Time{"a": base}, map[string]time.Time{"a": base}, true},
		{"file added", map[string]time.Time{"a": base}, map[string]time.Time{"a": base, "b": base}, false},
		{"file removed", map[string]time.Time{"a": base, "b": base}, map[string]time.Time{"a": base}, false},
		{"file renamed", map[string]time.Time{"a": base}, map[string]time.Time{"b": base}, false},
		{"mtime changed", map[string]time.Time{"a": base}, map[string]time.Time{"a": later}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := snapshotEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("snapshotEqual = %v, want %v", got, tc.want)
			}
		})
	}
}
