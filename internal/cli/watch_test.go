package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ffreis-siteops/internal/config"
	"ffreis-siteops/internal/runner"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubRunnerRun replaces the package-level runner.Run seam for the duration of
// the test and records every command it was asked to execute.
func stubRunnerRun(t *testing.T, result func(cmd runner.Command) error) *[]runner.Command {
	t.Helper()
	var seen []runner.Command
	orig := runner.Run
	t.Cleanup(func() { runner.Run = orig })
	runner.Run = func(_ context.Context, _ *slog.Logger, cmd runner.Command, _ runner.Options) error {
		seen = append(seen, cmd)
		return result(cmd)
	}
	return &seen
}

// redirectCloneDir points the GitHub-clone cache at a scratch path so tests
// never read or write the shared /tmp location.
func redirectCloneDir(t *testing.T, path string) {
	t.Helper()
	orig := compilerCloneDir
	t.Cleanup(func() { compilerCloneDir = orig })
	compilerCloneDir = path
}

// ── deriveCompilerImageName ────────────────────────────────────────────────

func TestDeriveCompilerImageName(t *testing.T) {
	cases := []struct {
		name       string
		composeEnv map[string]string
		want       string
	}{
		{
			name:       "no env falls back to defaults",
			composeEnv: nil,
			want:       "website-compiler-cli:local",
		},
		{
			name:       "explicit image wins over every other key",
			composeEnv: map[string]string{"WEBSITE_COMPILER_IMAGE": "ghcr.io/acme/custom:v9", "IMAGE_ROOT": "ignored", "IMAGE_TAG": "ignored"},
			want:       "ghcr.io/acme/custom:v9",
		},
		{
			name:       "image root is preferred over prefix keys",
			composeEnv: map[string]string{"IMAGE_ROOT": "root.io/acme", "IMAGE_PREFIX": "prefix.io/acme", "PREFIX": "acme"},
			want:       "root.io/acme/website-compiler-cli:local",
		},
		{
			name:       "image prefix is used when image root is unset",
			composeEnv: map[string]string{"IMAGE_PREFIX": "prefix.io/acme", "PREFIX": "acme"},
			want:       "prefix.io/acme/website-compiler-cli:local",
		},
		{
			name:       "prefix is the last resort for the image root",
			composeEnv: map[string]string{"PREFIX": "acme"},
			want:       "acme/website-compiler-cli:local",
		},
		{
			name:       "custom image name and tag are honoured",
			composeEnv: map[string]string{"PREFIX": "acme", "COMPILER_IMAGE_NAME": "my-compiler", "IMAGE_TAG": "v1.2.3"},
			want:       "acme/my-compiler:v1.2.3",
		},
		{
			name:       "custom name and tag without a root omit the registry segment",
			composeEnv: map[string]string{"COMPILER_IMAGE_NAME": "my-compiler", "IMAGE_TAG": "v1.2.3"},
			want:       "my-compiler:v1.2.3",
		},
		{
			name:       "whitespace-only values are treated as unset",
			composeEnv: map[string]string{"WEBSITE_COMPILER_IMAGE": "  ", "IMAGE_ROOT": " ", "IMAGE_PREFIX": " ", "PREFIX": " ", "COMPILER_IMAGE_NAME": " ", "IMAGE_TAG": " "},
			want:       "website-compiler-cli:local",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveCompilerImageName(config.Config{ComposeEnv: tc.composeEnv})
			if got != tc.want {
				t.Errorf("deriveCompilerImageName = %q, want %q", got, tc.want)
			}
		})
	}
}

// ── resolveWorkspaceRoot ───────────────────────────────────────────────────

func TestResolveWorkspaceRoot_UnsetValueLeavesConfigUnchanged(t *testing.T) {
	cfg := config.Config{ComposeEnv: map[string]string{"OTHER": "value"}}

	got := resolveWorkspaceRoot(cfg)

	if _, ok := got.ComposeEnv["WORKSPACE_ROOT"]; ok {
		t.Error("WORKSPACE_ROOT should not be introduced when it was unset")
	}
	if got.ComposeEnv["OTHER"] != "value" {
		t.Errorf("unrelated compose_env key was lost: %v", got.ComposeEnv)
	}
}

func TestResolveWorkspaceRoot_WhitespaceValueLeavesConfigUnchanged(t *testing.T) {
	cfg := config.Config{ComposeEnv: map[string]string{"WORKSPACE_ROOT": "   "}}

	got := resolveWorkspaceRoot(cfg)

	if got.ComposeEnv["WORKSPACE_ROOT"] != "   " {
		t.Errorf("WORKSPACE_ROOT = %q, want it untouched", got.ComposeEnv["WORKSPACE_ROOT"])
	}
}

func TestResolveWorkspaceRoot_RelativePathBecomesAbsolute(t *testing.T) {
	original := map[string]string{"WORKSPACE_ROOT": "..", "OTHER": "value"}
	cfg := config.Config{ComposeEnv: original}

	got := resolveWorkspaceRoot(cfg)

	resolved := got.ComposeEnv["WORKSPACE_ROOT"]
	if !filepath.IsAbs(resolved) {
		t.Errorf("WORKSPACE_ROOT = %q, want an absolute path", resolved)
	}
	if got.ComposeEnv["OTHER"] != "value" {
		t.Errorf("unrelated compose_env key was lost: %v", got.ComposeEnv)
	}
	if original["WORKSPACE_ROOT"] != ".." {
		t.Error("resolveWorkspaceRoot mutated the caller's compose_env map")
	}
}

func TestResolveWorkspaceRoot_AbsolutePathIsPreserved(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{ComposeEnv: map[string]string{"WORKSPACE_ROOT": dir}}

	got := resolveWorkspaceRoot(cfg)

	if got.ComposeEnv["WORKSPACE_ROOT"] != dir {
		t.Errorf("WORKSPACE_ROOT = %q, want %q", got.ComposeEnv["WORKSPACE_ROOT"], dir)
	}
}

// ── imageExistsLocally ─────────────────────────────────────────────────────

func TestImageExistsLocally_InspectSucceeds(t *testing.T) {
	seen := stubRunnerRun(t, func(runner.Command) error { return nil })

	if !imageExistsLocally(context.Background(), discardLogger(), config.Config{}, "acme/img:local") {
		t.Error("expected the image to be reported as present when inspect succeeds")
	}

	if len(*seen) != 1 {
		t.Fatalf("ran %d commands, want 1", len(*seen))
	}
	cmd := (*seen)[0]
	if cmd.Name != "docker" {
		t.Errorf("command = %q, want docker", cmd.Name)
	}
	if !containsSequence(cmd.Args, "image", "inspect") || !contains(cmd.Args, "acme/img:local") {
		t.Errorf("args = %v, want a docker image inspect of acme/img:local", cmd.Args)
	}
	if cmd.Stdout != io.Discard || cmd.Stderr != io.Discard {
		t.Error("inspect output should be discarded; it is a silent existence probe")
	}
}

func TestImageExistsLocally_InspectFails(t *testing.T) {
	stubRunnerRun(t, func(runner.Command) error { return errors.New("no such image") })

	if imageExistsLocally(context.Background(), discardLogger(), config.Config{}, "acme/img:local") {
		t.Error("expected the image to be reported as absent when inspect fails")
	}
}

// ── ensureCompilerSrc ──────────────────────────────────────────────────────

func TestEnsureCompilerSrc_ConfiguredPathExists(t *testing.T) {
	src := t.TempDir()
	seen := stubRunnerRun(t, func(runner.Command) error {
		t.Error("no command should run when the configured source exists")
		return nil
	})

	got, err := ensureCompilerSrc(context.Background(), discardLogger(), config.Config{CompilerSrc: src})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != src {
		t.Errorf("source = %q, want %q", got, src)
	}
	if len(*seen) != 0 {
		t.Errorf("ran %d commands, want none", len(*seen))
	}
}

func TestEnsureCompilerSrc_ConfiguredPathMissingFallsBackToClone(t *testing.T) {
	clone := filepath.Join(t.TempDir(), "clone")
	redirectCloneDir(t, clone)
	seen := stubRunnerRun(t, func(runner.Command) error { return nil })

	got, err := ensureCompilerSrc(context.Background(), discardLogger(),
		config.Config{CompilerSrc: filepath.Join(t.TempDir(), "absent")})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != clone {
		t.Errorf("source = %q, want the clone dir %q", got, clone)
	}
	if len(*seen) != 1 || (*seen)[0].Name != "git" {
		t.Errorf("commands = %v, want a single git clone", *seen)
	}
}

func TestEnsureCompilerSrc_NoConfiguredPathClonesFromGitHub(t *testing.T) {
	clone := filepath.Join(t.TempDir(), "clone")
	redirectCloneDir(t, clone)
	seen := stubRunnerRun(t, func(runner.Command) error { return nil })

	got, err := ensureCompilerSrc(context.Background(), discardLogger(), config.Config{})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != clone {
		t.Errorf("source = %q, want the clone dir %q", got, clone)
	}
	if len(*seen) != 1 {
		t.Fatalf("ran %d commands, want 1", len(*seen))
	}
}

// ── cloneCompilerFromGitHub ────────────────────────────────────────────────

func TestCloneCompilerFromGitHub_ExistingCloneIsReused(t *testing.T) {
	clone := t.TempDir() // already present on disk
	redirectCloneDir(t, clone)
	seen := stubRunnerRun(t, func(runner.Command) error {
		t.Error("git should not run when the clone cache already exists")
		return nil
	})

	got, err := cloneCompilerFromGitHub(context.Background(), discardLogger(), config.Config{})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != clone {
		t.Errorf("source = %q, want %q", got, clone)
	}
	if len(*seen) != 0 {
		t.Errorf("ran %d commands, want none", len(*seen))
	}
}

func TestCloneCompilerFromGitHub_ClonesWhenCacheIsAbsent(t *testing.T) {
	clone := filepath.Join(t.TempDir(), "clone")
	redirectCloneDir(t, clone)
	seen := stubRunnerRun(t, func(runner.Command) error { return nil })

	got, err := cloneCompilerFromGitHub(context.Background(), discardLogger(), config.Config{})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != clone {
		t.Errorf("source = %q, want %q", got, clone)
	}
	if len(*seen) != 1 {
		t.Fatalf("ran %d commands, want 1", len(*seen))
	}
	cmd := (*seen)[0]
	if cmd.Name != "git" {
		t.Errorf("command = %q, want git", cmd.Name)
	}
	if !containsSequence(cmd.Args, "--depth=1", compilerGitHubURL) || !contains(cmd.Args, clone) {
		t.Errorf("args = %v, want a shallow clone of %s into %s", cmd.Args, compilerGitHubURL, clone)
	}
}

func TestCloneCompilerFromGitHub_CloneFailureIsWrapped(t *testing.T) {
	redirectCloneDir(t, filepath.Join(t.TempDir(), "clone"))
	stubRunnerRun(t, func(runner.Command) error { return errors.New("network down") })

	_, err := cloneCompilerFromGitHub(context.Background(), discardLogger(), config.Config{})

	if err == nil {
		t.Fatal("expected an error when the clone fails")
	}
	if !strings.Contains(err.Error(), "cloning compiler from GitHub") || !strings.Contains(err.Error(), "network down") {
		t.Errorf("error = %v, want the clone failure wrapped with context", err)
	}
}

// ── buildCompilerImage ─────────────────────────────────────────────────────

func TestBuildCompilerImage_UsesDefaultBuilderAndRuntimeImages(t *testing.T) {
	src := t.TempDir()
	seen := stubRunnerRun(t, func(runner.Command) error { return nil })

	err := buildCompilerImage(context.Background(), discardLogger(), config.Config{}, "acme/img:local", src)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*seen) != 1 {
		t.Fatalf("ran %d commands, want 1", len(*seen))
	}
	cmd := (*seen)[0]
	if cmd.Name != "docker" {
		t.Errorf("command = %q, want docker", cmd.Name)
	}
	wantDockerfile := filepath.Join(src, "containers", "Dockerfile.cli")
	if !containsSequence(cmd.Args, "-f", wantDockerfile) {
		t.Errorf("args = %v, want -f %s", cmd.Args, wantDockerfile)
	}
	if !containsSequence(cmd.Args, "-t", "acme/img:local") {
		t.Errorf("args = %v, want -t acme/img:local", cmd.Args)
	}
	if !contains(cmd.Args, "--build-arg") ||
		!contains(cmd.Args, "BUILDER_IMAGE=docker.io/library/golang:1.25.8-bookworm") ||
		!contains(cmd.Args, "RUNTIME_IMAGE=docker.io/library/debian:bookworm-slim") {
		t.Errorf("args = %v, want the default builder and runtime build-args", cmd.Args)
	}
}

func TestBuildCompilerImage_HonoursConfiguredBuilderAndRuntimeImages(t *testing.T) {
	src := t.TempDir()
	seen := stubRunnerRun(t, func(runner.Command) error { return nil })
	cfg := config.Config{ComposeEnv: map[string]string{
		"COMPILER_BUILDER_IMAGE": "docker.io/library/golang:1.99",
		"COMPILER_RUNTIME_IMAGE": "docker.io/library/alpine:3.20",
	}}

	if err := buildCompilerImage(context.Background(), discardLogger(), cfg, "acme/img:local", src); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cmd := (*seen)[0]
	if !contains(cmd.Args, "BUILDER_IMAGE=docker.io/library/golang:1.99") ||
		!contains(cmd.Args, "RUNTIME_IMAGE=docker.io/library/alpine:3.20") {
		t.Errorf("args = %v, want the configured builder and runtime build-args", cmd.Args)
	}
}

func TestBuildCompilerImage_BuildFailureIsReturned(t *testing.T) {
	stubRunnerRun(t, func(runner.Command) error { return errors.New("build blew up") })

	err := buildCompilerImage(context.Background(), discardLogger(), config.Config{}, "acme/img:local", t.TempDir())

	if err == nil || !strings.Contains(err.Error(), "build blew up") {
		t.Errorf("error = %v, want the build failure surfaced", err)
	}
}

// ── bootstrapCompilerImage ─────────────────────────────────────────────────

func TestBootstrapCompilerImage_ExistingImageSkipsTheBuild(t *testing.T) {
	seen := stubRunnerRun(t, func(runner.Command) error { return nil }) // inspect succeeds

	err := bootstrapCompilerImage(context.Background(), discardLogger(), config.Config{})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*seen) != 1 || !contains((*seen)[0].Args, "inspect") {
		t.Errorf("commands = %v, want only the image inspect", *seen)
	}
}

func TestBootstrapCompilerImage_MissingImageClonesAndBuilds(t *testing.T) {
	clone := filepath.Join(t.TempDir(), "clone")
	redirectCloneDir(t, clone)
	seen := stubRunnerRun(t, func(cmd runner.Command) error {
		if contains(cmd.Args, "inspect") {
			return errors.New("no such image")
		}
		return nil
	})

	err := bootstrapCompilerImage(context.Background(), discardLogger(), config.Config{})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*seen) != 3 {
		t.Fatalf("ran %d commands, want 3 (inspect, clone, build): %v", len(*seen), *seen)
	}
	if (*seen)[1].Name != "git" {
		t.Errorf("second command = %q, want git clone", (*seen)[1].Name)
	}
	if (*seen)[2].Name != "docker" || !contains((*seen)[2].Args, "build") {
		t.Errorf("third command = %v, want a docker build", (*seen)[2])
	}
}

func TestBootstrapCompilerImage_SourceFailureIsWrapped(t *testing.T) {
	redirectCloneDir(t, filepath.Join(t.TempDir(), "clone"))
	stubRunnerRun(t, func(cmd runner.Command) error {
		if contains(cmd.Args, "inspect") {
			return errors.New("no such image")
		}
		return errors.New("clone refused")
	})

	err := bootstrapCompilerImage(context.Background(), discardLogger(), config.Config{})

	if err == nil {
		t.Fatal("expected an error when the compiler source cannot be obtained")
	}
	if !strings.Contains(err.Error(), "obtaining compiler source") || !strings.Contains(err.Error(), "clone refused") {
		t.Errorf("error = %v, want the source failure wrapped with context", err)
	}
}

// ── pickFreePort ───────────────────────────────────────────────────────────

// reservePortWindow binds `count` consecutive TCP ports so a test controls
// exactly which ports pickFreePort will find free. Returns the base port and
// the listeners holding the window; all are closed at test end.
func reservePortWindow(t *testing.T, count int) (int, []net.Listener) {
	t.Helper()
	var lc net.ListenConfig
	ctx := t.Context()

	for attempt := 0; attempt < 50; attempt++ {
		probe, err := lc.Listen(ctx, "tcp", ":0")
		if err != nil {
			t.Fatalf("probing for a free port: %v", err)
		}
		base := probe.Addr().(*net.TCPAddr).Port
		if err := probe.Close(); err != nil {
			t.Fatalf("closing the probe listener: %v", err)
		}

		held := make([]net.Listener, 0, count)
		complete := true
		for port := base; port < base+count; port++ {
			ln, err := lc.Listen(ctx, "tcp", fmt.Sprintf(":%d", port))
			if err != nil {
				complete = false
				break
			}
			held = append(held, ln)
		}
		if complete {
			t.Cleanup(func() {
				for _, ln := range held {
					_ = ln.Close()
				}
			})
			return base, held
		}
		for _, ln := range held {
			_ = ln.Close()
		}
	}
	t.Fatalf("could not reserve %d consecutive TCP ports", count)
	return 0, nil
}

func TestPickFreePort_ReturnsStartWhenItIsFree(t *testing.T) {
	base, held := reservePortWindow(t, 1)
	if err := held[0].Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}

	if got := pickFreePort(t.Context(), base); got != base {
		t.Errorf("pickFreePort = %d, want the free start port %d", got, base)
	}
}

func TestPickFreePort_SkipsTakenPortsWithinTheSearchRange(t *testing.T) {
	base, held := reservePortWindow(t, portSearchRange)
	const freeOffset = 3
	if err := held[freeOffset].Close(); err != nil {
		t.Fatalf("releasing a port inside the window: %v", err)
	}

	if got := pickFreePort(t.Context(), base); got != base+freeOffset {
		t.Errorf("pickFreePort = %d, want the first free port %d", got, base+freeOffset)
	}
}

func TestPickFreePort_FallsBackToStartWhenTheWholeRangeIsTaken(t *testing.T) {
	base, _ := reservePortWindow(t, portSearchRange)

	if got := pickFreePort(t.Context(), base); got != base {
		t.Errorf("pickFreePort = %d, want the start port %d as the fallback", got, base)
	}
}

// ── previewStartPort ───────────────────────────────────────────────────────

func TestPreviewStartPort(t *testing.T) {
	cases := []struct {
		name       string
		composeEnv map[string]string
		want       int
	}{
		{"unset uses the default", nil, defaultPreviewPort},
		{"empty uses the default", map[string]string{"PREVIEW_PORT": ""}, defaultPreviewPort},
		{"whitespace uses the default", map[string]string{"PREVIEW_PORT": "  "}, defaultPreviewPort},
		{"non-numeric uses the default", map[string]string{"PREVIEW_PORT": "eighty-eighty"}, defaultPreviewPort},
		{"zero uses the default", map[string]string{"PREVIEW_PORT": "0"}, defaultPreviewPort},
		{"negative uses the default", map[string]string{"PREVIEW_PORT": "-1"}, defaultPreviewPort},
		{"valid port is honoured", map[string]string{"PREVIEW_PORT": "9000"}, 9000},
		{"surrounding whitespace is trimmed", map[string]string{"PREVIEW_PORT": " 9001 "}, 9001},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := previewStartPort(config.Config{ComposeEnv: tc.composeEnv})
			if got != tc.want {
				t.Errorf("previewStartPort = %d, want %d", got, tc.want)
			}
		})
	}
}

// ── injectPort ─────────────────────────────────────────────────────────────

func TestInjectPort_SetsPreviewPortWithoutMutatingTheSource(t *testing.T) {
	original := map[string]string{"PREVIEW_PORT": "8088", "OTHER": "value"}
	cfg := config.Config{ComposeEnv: original}

	got := injectPort(cfg, 9123)

	if got.ComposeEnv["PREVIEW_PORT"] != "9123" {
		t.Errorf("PREVIEW_PORT = %q, want %q", got.ComposeEnv["PREVIEW_PORT"], "9123")
	}
	if got.ComposeEnv["OTHER"] != "value" {
		t.Errorf("unrelated compose_env key was lost: %v", got.ComposeEnv)
	}
	if original["PREVIEW_PORT"] != "8088" {
		t.Error("injectPort mutated the caller's compose_env map")
	}
}

func TestInjectPort_NilComposeEnvGetsAFreshMap(t *testing.T) {
	got := injectPort(config.Config{}, 8100)

	if got.ComposeEnv["PREVIEW_PORT"] != "8100" {
		t.Errorf("PREVIEW_PORT = %q, want %q", got.ComposeEnv["PREVIEW_PORT"], "8100")
	}
}

// ── runWatch ───────────────────────────────────────────────────────────────

func TestRunWatch_BootstrapsImageThenStartsComposeOnAFreePort(t *testing.T) {
	stubRunnerRun(t, func(runner.Command) error { return nil }) // image inspect succeeds

	var composeCfg config.Config
	var composeArgs []string
	origCompose := runCompose
	t.Cleanup(func() { runCompose = origCompose })
	runCompose = func(_ context.Context, _ *slog.Logger, cfg config.Config, args ...string) error {
		composeCfg = cfg
		composeArgs = args
		return nil
	}

	base, held := reservePortWindow(t, 1)
	if err := held[0].Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}

	cfg := config.Config{ComposeEnv: map[string]string{
		"PREVIEW_PORT":   strconv.Itoa(base),
		"WORKSPACE_ROOT": "..",
	}}

	if err := runWatch(context.Background(), discardLogger(), cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(composeArgs) != 2 || composeArgs[0] != "up" || composeArgs[1] != "--build" {
		t.Errorf("compose args = %v, want [up --build]", composeArgs)
	}
	if got := composeCfg.ComposeEnv["PREVIEW_PORT"]; got != strconv.Itoa(base) {
		t.Errorf("PREVIEW_PORT handed to compose = %q, want %q", got, strconv.Itoa(base))
	}
	if wsRoot := composeCfg.ComposeEnv["WORKSPACE_ROOT"]; !filepath.IsAbs(wsRoot) {
		t.Errorf("WORKSPACE_ROOT handed to compose = %q, want an absolute path", wsRoot)
	}
}

func TestRunWatch_BootstrapFailureStopsBeforeCompose(t *testing.T) {
	redirectCloneDir(t, filepath.Join(t.TempDir(), "clone"))
	stubRunnerRun(t, func(cmd runner.Command) error {
		if contains(cmd.Args, "inspect") {
			return errors.New("no such image")
		}
		return errors.New("clone refused")
	})

	origCompose := runCompose
	t.Cleanup(func() { runCompose = origCompose })
	runCompose = func(context.Context, *slog.Logger, config.Config, ...string) error {
		t.Error("compose must not start when the compiler image cannot be bootstrapped")
		return nil
	}

	err := runWatch(context.Background(), discardLogger(), config.Config{})

	if err == nil || !strings.Contains(err.Error(), "obtaining compiler source") {
		t.Errorf("error = %v, want the bootstrap failure surfaced", err)
	}
}
