package runner

import (
	"context"
	"io/fs"
	"path/filepath"
	"sync"
	"time"
)

// FileWatcher polls the configured paths recursively for file mtime changes
// and emits a single tick on Out per coalesced change burst. Uses stdlib only
// (no fsnotify) so go.mod stays free of native deps; the polling cost is
// negligible for the ~hundreds of files in a typical website tree.
//
// Algorithm:
//   - At interval (default 500ms), walk Paths and snapshot {file → mtime}.
//   - On any diff vs the previous snapshot, (re)arm a debounce timer
//     (default 200ms). When the timer fires, a single struct{} is
//     non-blocking-sent to Out. Subsequent changes within the debounce
//     window reset the timer.
//
// Skip lists keep the walk fast: vendor/, .git/, node_modules/, dist/.
type FileWatcher struct {
	Paths    []string
	Interval time.Duration
	Debounce time.Duration
	Out      chan<- struct{}
}

const (
	defaultWatcherInterval = 500 * time.Millisecond
	defaultWatcherDebounce = 200 * time.Millisecond
)

// skipDirs are directory basenames the watcher refuses to descend into.
// They generate enormous numbers of stat calls for zero dev-loop signal.
var skipDirs = map[string]struct{}{
	"vendor":       {},
	".git":         {},
	"node_modules": {},
	"dist":         {},
}

// Run blocks until ctx is canceled. On startup, takes an initial snapshot
// (no tick is emitted for the baseline). On every subsequent interval, if
// the snapshot changed, arms the debounce timer.
//
// Returns ctx.Err() on cancel.
func (w *FileWatcher) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = defaultWatcherInterval
	}
	debounce := w.Debounce
	if debounce <= 0 {
		debounce = defaultWatcherDebounce
	}

	prev := w.snapshot()

	// pendingMu guards pending; it's accessed from both the poll loop and
	// the AfterFunc callback which fires on its own goroutine.
	var pendingMu sync.Mutex
	var pending *time.Timer

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			pendingMu.Lock()
			if pending != nil {
				pending.Stop()
			}
			pendingMu.Unlock()
			return ctx.Err()
		case <-ticker.C:
			next := w.snapshot()
			if snapshotEqual(prev, next) {
				continue
			}
			prev = next
			pendingMu.Lock()
			if pending != nil {
				pending.Stop()
			}
			out := w.Out
			pending = time.AfterFunc(debounce, func() {
				select {
				case out <- struct{}{}:
				default:
					// Already a tick queued; one is enough.
				}
			})
			pendingMu.Unlock()
		}
	}
}

// snapshot walks Paths and returns the mtime of every file. Directories
// themselves are excluded — only file mtimes matter for "did the source
// change". Walk errors are silently ignored so a transient unreadable file
// (e.g. mid-rename) doesn't crash the watcher.
func (w *FileWatcher) snapshot() map[string]time.Time {
	out := make(map[string]time.Time)
	for _, root := range w.Paths {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if _, skip := skipDirs[d.Name()]; skip && path != root {
					return fs.SkipDir
				}
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			out[path] = info.ModTime()
			return nil
		})
	}
	return out
}

func snapshotEqual(a, b map[string]time.Time) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || !vb.Equal(va) {
			return false
		}
	}
	return true
}
