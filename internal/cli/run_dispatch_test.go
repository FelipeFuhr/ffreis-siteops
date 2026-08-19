package cli

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ffreis-siteops/internal/config"
	"ffreis-siteops/internal/runner"
)

// dispatchRecorder captures which outward call each subcommand routed to.
type dispatchRecorder struct {
	compilerArgs []string
	composeArgs  []string
	awsArgs      []string
	devSpec      *runner.DevSpec
	published    bool
}

// stubOutwardCalls replaces every process-spawning seam in the cli package so
// Run's dispatch table can be exercised without launching anything.
func stubOutwardCalls(t *testing.T) *dispatchRecorder {
	t.Helper()
	rec := &dispatchRecorder{}

	origCompiler, origCompose, origAWS := runCompiler, runCompose, runAWS
	origDev, origRunnerRun := runnerRunDev, runner.Run
	origPubCompiler, origPubPublisher, origPubInvalidator := runPublishCompiler, runPublishPublisher, runPublishInvalidator
	t.Cleanup(func() {
		runCompiler, runCompose, runAWS = origCompiler, origCompose, origAWS
		runnerRunDev, runner.Run = origDev, origRunnerRun
		runPublishCompiler, runPublishPublisher, runPublishInvalidator = origPubCompiler, origPubPublisher, origPubInvalidator
	})

	runCompiler = func(_ context.Context, _ *slog.Logger, _ config.Config, args ...string) error {
		rec.compilerArgs = args
		return nil
	}
	runCompose = func(_ context.Context, _ *slog.Logger, _ config.Config, args ...string) error {
		rec.composeArgs = args
		return nil
	}
	runAWS = func(_ context.Context, _ *slog.Logger, _ config.Config, args ...string) error {
		rec.awsArgs = args
		return nil
	}
	runnerRunDev = func(_ context.Context, _ *slog.Logger, spec runner.DevSpec) error {
		rec.devSpec = &spec
		return nil
	}
	// watch's bootstrap probes for the compiler image; report it as present.
	runner.Run = func(context.Context, *slog.Logger, runner.Command, runner.Options) error { return nil }
	runPublishCompiler = func(context.Context, *slog.Logger, config.Config) error { return nil }
	runPublishPublisher = func(context.Context, *slog.Logger, config.Config, map[string]string) error {
		rec.published = true
		return nil
	}
	runPublishInvalidator = func(context.Context, *slog.Logger, config.Config, map[string]string) error { return nil }

	return rec
}

// writeDispatchConfig writes a config that satisfies validation for every
// subcommand, so a failing exit code always means a dispatch problem rather
// than a missing field.
func writeDispatchConfig(t *testing.T) (cfgPath, outDir string) {
	t.Helper()
	dir := t.TempDir()
	outDir = filepath.Join(dir, "dist")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("creating out dir: %v", err)
	}
	cfgPath = filepath.Join(dir, "site.yaml")
	body := strings.Join([]string{
		"project_name: dispatch",
		"compiler_command: echo",
		"website_root: " + dir,
		"out_dir: " + outDir,
		"site_data_source: " + filepath.Join(dir, "site.yaml"),
		"data_root: " + dir,
		"default_lang: pt",
		"compose_command: [echo]",
		"compose_env:",
		"  WORKSPACE_ROOT: " + dir,
		"publish:",
		"  bucket: publish-bucket",
		"builds:",
		"  bucket: builds-bucket",
		"  source: dispatch",
		"",
	}, "\n")
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return cfgPath, outDir
}

func TestRun_DispatchesCompilerSubcommands(t *testing.T) {
	cfgPath, _ := writeDispatchConfig(t)

	cases := []struct {
		name           string
		args           []string
		wantSubcommand string
		wantSiteData   bool
	}{
		{"build", []string{"build"}, "build", true},
		{"build-inline", []string{"build-inline"}, "build", true},
		{"serve", []string{"serve"}, "serve", true},
		{"validate-site-data", []string{"validate-site-data"}, "validate-site-data", true},
		{"validate-assets", []string{"validate-assets"}, "validate-assets", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := stubOutwardCalls(t)

			code := Run("siteops-test", append([]string{"-config", cfgPath}, tc.args...))

			if code != 0 {
				t.Fatalf("Run(%v) = %d, want 0", tc.args, code)
			}
			if firstArg(rec.compilerArgs) != tc.wantSubcommand {
				t.Errorf("compiler subcommand = %q, want %q (args %v)", firstArg(rec.compilerArgs), tc.wantSubcommand, rec.compilerArgs)
			}
			if tc.wantSiteData && !contains(rec.compilerArgs, flagSiteData) {
				t.Errorf("args = %v, want %s to be forwarded", rec.compilerArgs, flagSiteData)
			}
		})
	}
}

func TestRun_BuildInlinePassesInlineAssets(t *testing.T) {
	cfgPath, _ := writeDispatchConfig(t)
	rec := stubOutwardCalls(t)

	if code := Run("siteops-test", []string{"-config", cfgPath, "build-inline"}); code != 0 {
		t.Fatalf("Run = %d, want 0", code)
	}

	if !contains(rec.compilerArgs, "-inline-assets") {
		t.Errorf("args = %v, want -inline-assets", rec.compilerArgs)
	}
}

func TestRun_ForwardsExtraArgsToTheCompiler(t *testing.T) {
	cfgPath, _ := writeDispatchConfig(t)
	rec := stubOutwardCalls(t)

	if code := Run("siteops-test", []string{"-config", cfgPath, "build", "-verbose"}); code != 0 {
		t.Fatalf("Run = %d, want 0", code)
	}

	if !contains(rec.compilerArgs, "-verbose") {
		t.Errorf("args = %v, want the trailing -verbose forwarded", rec.compilerArgs)
	}
}

func TestRun_DispatchesComposeSubcommands(t *testing.T) {
	cfgPath, _ := writeDispatchConfig(t)

	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"compose-up", []string{"compose-up"}, []string{"up", flagComposeBuild}},
		{"compose-down", []string{"compose-down"}, []string{"down"}},
		{"compose-logs", []string{"compose-logs"}, []string{"logs", "-f"}},
		{"compose-rebuild", []string{"compose-rebuild"}, []string{"up", flagComposeBuild, "--force-recreate"}},
		{"docker-up alias", []string{"docker-up"}, []string{"up", flagComposeBuild}},
		{"docker-down alias", []string{"docker-down"}, []string{"down"}},
		{"docker-logs alias", []string{"docker-logs"}, []string{"logs", "-f"}},
		{"docker-rebuild alias", []string{"docker-rebuild"}, []string{"up", flagComposeBuild, "--force-recreate"}},
		{"deploy-local", []string{"deploy-local"}, []string{"up", flagComposeBuild}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := stubOutwardCalls(t)

			code := Run("siteops-test", append([]string{"-config", cfgPath}, tc.args...))

			if code != 0 {
				t.Fatalf("Run(%v) = %d, want 0", tc.args, code)
			}
			if strings.Join(rec.composeArgs, " ") != strings.Join(tc.want, " ") {
				t.Errorf("compose args = %v, want %v", rec.composeArgs, tc.want)
			}
		})
	}
}

func TestRun_DispatchesBuildsSubcommands(t *testing.T) {
	cfgPath, _ := writeDispatchConfig(t)

	cases := []struct {
		name        string
		args        []string
		wantAWSVerb string
	}{
		{"upload-build", []string{"upload-build", "--sha", "abc1234def"}, "sync"},
		{"promote", []string{"promote", "--sha", "abc1234def"}, "sync"},
		{"list-builds", []string{"list-builds"}, "ls"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := stubOutwardCalls(t)

			code := Run("siteops-test", append([]string{"-config", cfgPath}, tc.args...))

			if code != 0 {
				t.Fatalf("Run(%v) = %d, want 0", tc.args, code)
			}
			if len(rec.awsArgs) < 2 || rec.awsArgs[0] != "s3" || rec.awsArgs[1] != tc.wantAWSVerb {
				t.Errorf("aws args = %v, want an s3 %s", rec.awsArgs, tc.wantAWSVerb)
			}
		})
	}
}

func TestRun_BuildsSubcommandWithoutSHAExitsTwo(t *testing.T) {
	cfgPath, _ := writeDispatchConfig(t)
	stubOutwardCalls(t)

	for _, cmd := range []string{"upload-build", "promote"} {
		if code := Run("siteops-test", []string{"-config", cfgPath, cmd}); code != 2 {
			t.Errorf("Run(%s) without --sha = %d, want 2", cmd, code)
		}
	}
}

func TestRun_BuildsSubcommandFailureExitsOne(t *testing.T) {
	cfgPath, _ := writeDispatchConfig(t)
	stubOutwardCalls(t)
	runAWS = func(context.Context, *slog.Logger, config.Config, ...string) error {
		return errors.New("s3 refused")
	}

	if code := Run("siteops-test", []string{"-config", cfgPath, "list-builds"}); code != 1 {
		t.Errorf("Run(list-builds) with a failing aws call = %d, want 1", code)
	}
}

func TestRun_DispatchesPublishAndDeployToThePublishPipeline(t *testing.T) {
	cfgPath, _ := writeDispatchConfig(t)

	for _, cmd := range []string{"publish", "deploy"} {
		t.Run(cmd, func(t *testing.T) {
			rec := stubOutwardCalls(t)

			code := Run("siteops-test", []string{"-config", cfgPath, cmd})

			if code != 0 {
				t.Fatalf("Run(%s) = %d, want 0", cmd, code)
			}
			if !rec.published {
				t.Errorf("Run(%s) did not reach the publisher", cmd)
			}
		})
	}
}

func TestRun_DispatchesDevToTheOrchestrator(t *testing.T) {
	cfgPath, _ := writeDispatchConfig(t)
	rec := stubOutwardCalls(t)

	code := Run("siteops-test", []string{"-config", cfgPath, "dev", "--lang", "en"})

	if code != 0 {
		t.Fatalf("Run(dev) = %d, want 0", code)
	}
	if rec.devSpec == nil {
		t.Fatal("Run(dev) did not reach the dev orchestrator")
	}
	if rec.devSpec.Lang != "en" {
		t.Errorf("dev lang = %q, want %q", rec.devSpec.Lang, "en")
	}
}

func TestRun_DispatchesWatchToComposeUp(t *testing.T) {
	cfgPath, _ := writeDispatchConfig(t)
	rec := stubOutwardCalls(t)

	code := Run("siteops-test", []string{"-config", cfgPath, "watch"})

	if code != 0 {
		t.Fatalf("Run(watch) = %d, want 0", code)
	}
	if strings.Join(rec.composeArgs, " ") != "up "+flagComposeBuild {
		t.Errorf("compose args = %v, want [up %s]", rec.composeArgs, flagComposeBuild)
	}
}

func TestRun_CleanRemovesTheOutputDirectory(t *testing.T) {
	cfgPath, outDir := writeDispatchConfig(t)
	stubOutwardCalls(t)
	if err := os.WriteFile(filepath.Join(outDir, "index.html"), []byte("<html>"), 0o644); err != nil {
		t.Fatalf("seeding the out dir: %v", err)
	}

	code := Run("siteops-test", []string{"-config", cfgPath, "clean"})

	if code != 0 {
		t.Fatalf("Run(clean) = %d, want 0", code)
	}
	if _, err := os.Stat(outDir); err == nil {
		t.Error("clean left the output directory in place")
	}
}

func TestRun_HelpCommandExitsZero(t *testing.T) {
	cfgPath, _ := writeDispatchConfig(t)
	stubOutwardCalls(t)

	if code := Run("siteops-test", []string{"-config", cfgPath, "help"}); code != 0 {
		t.Errorf("Run(help) = %d, want 0", code)
	}
}

func TestRun_HelpFlagsAreHandledByFlagParsingAndExitTwo(t *testing.T) {
	cfgPath, _ := writeDispatchConfig(t)
	stubOutwardCalls(t)

	// -h/--help never reach the command switch: the flag package intercepts
	// them during fs.Parse and returns flag.ErrHelp.
	for _, alias := range []string{"-h", "--help"} {
		if code := Run("siteops-test", []string{"-config", cfgPath, alias}); code != 2 {
			t.Errorf("Run(%s) = %d, want 2", alias, code)
		}
	}
}

func TestRun_UnknownCommandExitsTwo(t *testing.T) {
	cfgPath, _ := writeDispatchConfig(t)
	stubOutwardCalls(t)

	if code := Run("siteops-test", []string{"-config", cfgPath, "teleport"}); code != 2 {
		t.Errorf("Run(teleport) = %d, want 2", code)
	}
}

func TestRun_UnparseableTopLevelFlagExitsTwo(t *testing.T) {
	if code := Run("siteops-test", []string{"-not-a-flag"}); code != 2 {
		t.Errorf("Run with an unknown top-level flag = %d, want 2", code)
	}
}

func TestRun_CompilerFailureExitsOne(t *testing.T) {
	cfgPath, _ := writeDispatchConfig(t)
	stubOutwardCalls(t)
	runCompiler = func(context.Context, *slog.Logger, config.Config, ...string) error {
		return errors.New("compiler blew up")
	}

	if code := Run("siteops-test", []string{"-config", cfgPath, "build"}); code != 1 {
		t.Errorf("Run(build) with a failing compiler = %d, want 1", code)
	}
}

func TestRun_MissingComposeCommandExitsTwo(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "site.yaml")
	body := "project_name: nocompose\ncompiler_command: echo\nwebsite_root: " + dir + "\nout_dir: " + dir + "\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	stubOutwardCalls(t)

	if code := Run("siteops-test", []string{"-config", cfgPath, "compose-up"}); code != 2 {
		t.Errorf("Run(compose-up) without compose_command = %d, want 2", code)
	}
}
