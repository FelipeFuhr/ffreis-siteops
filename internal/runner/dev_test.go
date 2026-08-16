package runner

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func devLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newLocalListener opens a TCP listener on a free loopback port and returns
// that port; the listener stays open for the rest of the test.
func newLocalListener(t *testing.T) int {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// ── injectData ─────────────────────────────────────────────────────────────

func TestInjectData_RequiredInputsAreValidated(t *testing.T) {
	populated := t.TempDir()

	cases := []struct {
		name        string
		dataRoot    string
		lang        string
		websiteRoot string
		wantMessage string
	}{
		{"missing data root", "", "pt", populated, "data_root is required"},
		{"missing lang", populated, "", populated, "lang is required"},
		{"missing website root", populated, "pt", "", "website_root is required"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := injectData(devLogger(), tc.dataRoot, tc.lang, tc.websiteRoot)
			if err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantMessage) {
				t.Errorf("error = %v, want it to contain %q", err, tc.wantMessage)
			}
		})
	}
}

func TestInjectData_UncreatableDataDirIsReported(t *testing.T) {
	// A regular file cannot host <website_root>/src/data.
	websiteRoot := filepath.Join(t.TempDir(), "not-a-dir")
	writeFile(t, websiteRoot, "regular file")

	err := injectData(devLogger(), minimalDataRoot(t, "pt"), "pt", websiteRoot)

	if err == nil || !strings.Contains(err.Error(), "creating data dir") {
		t.Errorf("error = %v, want the data-dir creation failure reported", err)
	}
}

func TestInjectData_MissingLanguageSiteYAMLIsReported(t *testing.T) {
	dataRoot := t.TempDir() // no <lang>/site.yaml at all

	err := injectData(devLogger(), dataRoot, "pt", t.TempDir())

	if err == nil || !strings.Contains(err.Error(), "locating") {
		t.Errorf("error = %v, want the missing site.yaml reported", err)
	}
}

func TestInjectData_UnwritableDestinationSiteYAMLIsReported(t *testing.T) {
	websiteRoot := t.TempDir()
	// A directory where the copied site.yaml should land makes the copy fail.
	if err := os.MkdirAll(filepath.Join(websiteRoot, "src", "data", "site.yaml"), 0o755); err != nil {
		t.Fatalf("preparing the blocking directory: %v", err)
	}

	err := injectData(devLogger(), minimalDataRoot(t, "pt"), "pt", websiteRoot)

	if err == nil || !strings.Contains(err.Error(), "copying site.yaml") {
		t.Errorf("error = %v, want the site.yaml copy failure reported", err)
	}
}

func TestInjectData_StaleSiteDFilesAreRemovedBeforeTheCopy(t *testing.T) {
	websiteRoot := t.TempDir()
	destSiteD := filepath.Join(websiteRoot, "src", "data", "site.d")
	stale := filepath.Join(destSiteD, "removed-page.yaml")
	keptNonYAML := filepath.Join(destSiteD, "notes.txt")
	writeFile(t, stale, "stale: true\n")
	writeFile(t, keptNonYAML, "not a yaml file")

	if err := injectData(devLogger(), minimalDataRoot(t, "pt"), "pt", websiteRoot); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(stale); err == nil {
		t.Error("stale site.d yaml was not removed before the copy")
	}
	if _, err := os.Stat(keptNonYAML); err != nil {
		t.Errorf("non-yaml file in site.d should be left alone: %v", err)
	}
}

func TestInjectData_MergesPerLanguageThenSharedSiteD(t *testing.T) {
	websiteRoot := t.TempDir()
	dataRoot := minimalDataRoot(t, "pt") // pt/site.d/a.yaml + shared/site.d/b.yaml

	if err := injectData(devLogger(), dataRoot, "pt", websiteRoot); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dataDir := filepath.Join(websiteRoot, "src", "data")
	for _, want := range []string{"site.yaml", filepath.Join("site.d", "a.yaml"), filepath.Join("site.d", "b.yaml")} {
		if _, err := os.Stat(filepath.Join(dataDir, want)); err != nil {
			t.Errorf("expected %s to be staged: %v", want, err)
		}
	}
}

func TestInjectData_SharedSiteDOverwritesTheSameFileName(t *testing.T) {
	websiteRoot := t.TempDir()
	dataRoot := t.TempDir()
	writeFile(t, filepath.Join(dataRoot, "pt", "site.yaml"), "site_title: t\n")
	writeFile(t, filepath.Join(dataRoot, "pt", "site.d", "nav.yaml"), "from: lang\n")
	writeFile(t, filepath.Join(dataRoot, "shared", "site.d", "nav.yaml"), "from: shared\n")

	if err := injectData(devLogger(), dataRoot, "pt", websiteRoot); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(websiteRoot, "src", "data", "site.d", "nav.yaml"))
	if err != nil {
		t.Fatalf("reading the merged file: %v", err)
	}
	if string(got) != "from: shared\n" {
		t.Errorf("merged nav.yaml = %q, want the shared copy to win", got)
	}
}

func TestInjectData_PerLanguageSiteDCopyFailureIsReported(t *testing.T) {
	websiteRoot := t.TempDir()
	dataRoot := t.TempDir()
	writeFile(t, filepath.Join(dataRoot, "pt", "site.yaml"), "site_title: t\n")
	writeFile(t, filepath.Join(dataRoot, "pt", "site.d", "nav.yaml"), "from: lang\n")
	// A directory named nav.yaml survives the stale-file sweep (it skips
	// directories) and then blocks the copy.
	if err := os.MkdirAll(filepath.Join(websiteRoot, "src", "data", "site.d", "nav.yaml"), 0o755); err != nil {
		t.Fatalf("preparing the blocking directory: %v", err)
	}

	err := injectData(devLogger(), dataRoot, "pt", websiteRoot)

	if err == nil || !strings.Contains(err.Error(), "copying per-lang site.d") {
		t.Errorf("error = %v, want the per-language copy failure reported", err)
	}
}

func TestInjectData_SharedSiteDCopyFailureIsReported(t *testing.T) {
	websiteRoot := t.TempDir()
	dataRoot := t.TempDir()
	writeFile(t, filepath.Join(dataRoot, "pt", "site.yaml"), "site_title: t\n")
	writeFile(t, filepath.Join(dataRoot, "shared", "site.d", "footer.yaml"), "from: shared\n")
	if err := os.MkdirAll(filepath.Join(websiteRoot, "src", "data", "site.d", "footer.yaml"), 0o755); err != nil {
		t.Fatalf("preparing the blocking directory: %v", err)
	}

	err := injectData(devLogger(), dataRoot, "pt", websiteRoot)

	if err == nil || !strings.Contains(err.Error(), "copying shared site.d") {
		t.Errorf("error = %v, want the shared copy failure reported", err)
	}
}

// ── copyYAMLDir ────────────────────────────────────────────────────────────

func TestCopyYAMLDir_MissingSourceIsNotAnError(t *testing.T) {
	dest := t.TempDir()

	if err := copyYAMLDir(filepath.Join(t.TempDir(), "absent"), dest); err != nil {
		t.Errorf("unexpected error for a missing source dir: %v", err)
	}
}

func TestCopyYAMLDir_SourceThatIsAFileIsIgnored(t *testing.T) {
	src := filepath.Join(t.TempDir(), "site.d")
	writeFile(t, src, "not a directory")

	if err := copyYAMLDir(src, t.TempDir()); err != nil {
		t.Errorf("unexpected error for a file source: %v", err)
	}
}

func TestCopyYAMLDir_CopiesOnlyTopLevelYAMLFiles(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	writeFile(t, filepath.Join(src, "page.yaml"), "page: 1\n")
	writeFile(t, filepath.Join(src, "README.md"), "docs")
	writeFile(t, filepath.Join(src, "nested", "deep.yaml"), "deep: 1\n")

	if err := copyYAMLDir(src, dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("reading destination: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "page.yaml" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("destination contains %v, want only page.yaml", names)
	}
}

// ── copyFile ───────────────────────────────────────────────────────────────

func TestCopyFile_MissingSourceIsReported(t *testing.T) {
	err := copyFile(filepath.Join(t.TempDir(), "absent.yaml"), filepath.Join(t.TempDir(), "dest.yaml"))

	if err == nil {
		t.Fatal("expected an error for a missing source file")
	}
}

func TestCopyFile_UnwritableDestinationIsReported(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.yaml")
	writeFile(t, src, "a: 1\n")
	dest := filepath.Join(t.TempDir(), "dest.yaml")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("preparing the blocking directory: %v", err)
	}

	if err := copyFile(src, dest); err == nil {
		t.Fatal("expected an error when the destination cannot be opened for writing")
	}
}

func TestCopyFile_CopiesContentVerbatim(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.yaml")
	dest := filepath.Join(t.TempDir(), "dest.yaml")
	writeFile(t, src, "a: 1\nb: 2\n")

	if err := copyFile(src, dest); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading the copy: %v", err)
	}
	if string(got) != "a: 1\nb: 2\n" {
		t.Errorf("copied content = %q, want it byte-identical", got)
	}
}

// ── allocatePort ───────────────────────────────────────────────────────────

func TestAllocatePort_NonPositiveStartUsesTheDefaultRange(t *testing.T) {
	got, err := allocatePort(t.Context(), 0)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got < 8088 || got >= 8088+50 {
		t.Errorf("allocatePort(0) = %d, want a port in [8088, 8138)", got)
	}
}

func TestAllocatePort_FreeStartPortIsReturned(t *testing.T) {
	start := freePort(t)

	got, err := allocatePort(t.Context(), start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != start {
		t.Errorf("allocatePort = %d, want the free start port %d", got, start)
	}
}

// ── waitForPort ────────────────────────────────────────────────────────────

func TestWaitForPort_CancelledContextReturnsTheContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := waitForPort(ctx, "127.0.0.1", freePort(t), time.Second)

	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("error = %v, want the context cancellation surfaced", err)
	}
}

func TestWaitForPort_TimesOutWhenNothingIsListening(t *testing.T) {
	err := waitForPort(t.Context(), "127.0.0.1", freePort(t), time.Nanosecond)

	if err == nil || !strings.Contains(err.Error(), "timeout waiting for") {
		t.Errorf("error = %v, want a timeout", err)
	}
}

func TestWaitForPort_ReturnsOnceTheListenerAccepts(t *testing.T) {
	srv := newLocalListener(t)

	if err := waitForPort(t.Context(), "127.0.0.1", srv, 5*time.Second); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
