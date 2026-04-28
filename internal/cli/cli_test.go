package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ffreis-siteops/internal/config"
)

// ── Run (integration) ─────────────────────────────────────────────────────

func TestRun_CommandDispatch(t *testing.T) {
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "site.yaml")
	if err := os.WriteFile(configFile, []byte("project_name: test\nout_dir: \""+tempDir+"\"\nwebsite_root: \""+tempDir+"\"\ncompiler_command: echo\n"), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	tests := []struct {
		name string
		args []string
		want int
	}{
		{"help", []string{"help"}, 1},
		{"unknown", []string{"doesnotexist"}, 1},
		{"no command", []string{}, 2},
		{"bad config path", []string{"-config", "/nonexistent", "help"}, 1},
		{"build", []string{"-config", configFile, "build"}, 1}, // will fail to exec compiler, but dispatches
		{"clean", []string{"-config", configFile, "clean"}, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Run("siteops-test", tc.args)
			if got != tc.want {
				t.Errorf("Run(%q) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}

func TestRun_ConfigValidationError(t *testing.T) {
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "site.yaml")
	// Missing website_root triggers validation error for most commands
	if err := os.WriteFile(configFile, []byte("project_name: test\nout_dir: \""+tempDir+"\"\ncompiler_command: echo\n"), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	got := Run("siteops-test", []string{"-config", configFile, "build"})
	   if got != 1 {
		   t.Errorf("Run with invalid config = %d, want 1", got)
	   }
}

// ── envToMap ─────────────────────────────────────────────────────────────────

func TestEnvToMap_Basic(t *testing.T) {
	m := envToMap([]string{"FOO=bar", "BAZ=qux=extra", "EMPTY="})
	if m["FOO"] != "bar" {
		t.Errorf("FOO: got %q, want %q", m["FOO"], "bar")
	}
	if m["BAZ"] != "qux=extra" {
		t.Errorf("BAZ: got %q, want %q", m["BAZ"], "qux=extra")
	}
	if v, ok := m["EMPTY"]; !ok || v != "" {
		t.Errorf("EMPTY: got %q ok=%v, want empty string", v, ok)
	}
}

func TestEnvToMap_SkipsMalformed(t *testing.T) {
	m := envToMap([]string{"NOEQUALS"})
	if _, ok := m["NOEQUALS"]; ok {
		t.Error("expected entry without '=' to be skipped")
	}
}

func TestEnvToMap_NilInput(t *testing.T) {
	m := envToMap(nil)
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

// ── getEnvDuration ───────────────────────────────────────────────────────────

func TestGetEnvDuration_ValidDuration(t *testing.T) {
	t.Setenv("TEST_DUR_VALID", "2m")
	got := getEnvDuration("TEST_DUR_VALID", 5*time.Second)
	if got != 2*time.Minute {
		t.Errorf("got %v, want 2m", got)
	}
}

func TestGetEnvDuration_EmptyFallback(t *testing.T) {
	t.Setenv("TEST_DUR_EMPTY", "")
	got := getEnvDuration("TEST_DUR_EMPTY", 5*time.Second)
	if got != 5*time.Second {
		t.Errorf("got %v, want 5s", got)
	}
}

func TestGetEnvDuration_InvalidFallback(t *testing.T) {
	t.Setenv("TEST_DUR_BAD", "notaduration")
	got := getEnvDuration("TEST_DUR_BAD", 5*time.Second)
	if got != 5*time.Second {
		t.Errorf("got %v, want 5s", got)
	}
}

func TestGetEnvDuration_NegativeFallback(t *testing.T) {
	t.Setenv("TEST_DUR_NEG", "-1s")
	got := getEnvDuration("TEST_DUR_NEG", 5*time.Second)
	if got != 5*time.Second {
		t.Errorf("got %v, want 5s (negative should use fallback)", got)
	}
}

// ── withImageModelDefaults ───────────────────────────────────────────────────

func envMap(env []string) map[string]string {
	return envToMap(env)
}

func TestWithImageModelDefaults_AllEmpty(t *testing.T) {
	result := withImageModelDefaults(nil)
	m := envMap(result)

	if m["IMAGE_TAG"] != "local" {
		t.Errorf("IMAGE_TAG: got %q, want %q", m["IMAGE_TAG"], "local")
	}
	if m["COMPILER_IMAGE_NAME"] != "website-compiler-cli" {
		t.Errorf("COMPILER_IMAGE_NAME: got %q", m["COMPILER_IMAGE_NAME"])
	}
	if m["COMPILER_WATCH_IMAGE_NAME"] != "website-compiler-watch" {
		t.Errorf("COMPILER_WATCH_IMAGE_NAME: got %q", m["COMPILER_WATCH_IMAGE_NAME"])
	}
	if m["COMPILER_WATCH_RUNTIME_IMAGE"] != "debian:bookworm-slim" {
		t.Errorf("COMPILER_WATCH_RUNTIME_IMAGE: got %q", m["COMPILER_WATCH_RUNTIME_IMAGE"])
	}
	if m["PREVIEW_IMAGE"] != "nginx:alpine" {
		t.Errorf("PREVIEW_IMAGE: got %q", m["PREVIEW_IMAGE"])
	}
}

func TestWithImageModelDefaults_WithPrefix(t *testing.T) {
	env := []string{"PREFIX=myorg", "IMAGE_TAG=v1.0"}
	result := withImageModelDefaults(env)
	m := envMap(result)

	if m["IMAGE_ROOT"] != "myorg" {
		t.Errorf("IMAGE_ROOT: got %q, want %q", m["IMAGE_ROOT"], "myorg")
	}
	if m["COMPILER_WATCH_IMAGE"] != "myorg/website-compiler-watch:v1.0" {
		t.Errorf("COMPILER_WATCH_IMAGE: got %q", m["COMPILER_WATCH_IMAGE"])
	}
	if m["WEBSITE_COMPILER_IMAGE"] != "myorg/website-compiler-cli:v1.0" {
		t.Errorf("WEBSITE_COMPILER_IMAGE: got %q", m["WEBSITE_COMPILER_IMAGE"])
	}
}

func TestWithImageModelDefaults_WithImageProvider(t *testing.T) {
	env := []string{"PREFIX=myorg", "IMAGE_PROVIDER=ghcr.io", "IMAGE_TAG=latest"}
	result := withImageModelDefaults(env)
	m := envMap(result)

	if m["IMAGE_PREFIX"] != "ghcr.io/myorg" {
		t.Errorf("IMAGE_PREFIX: got %q, want %q", m["IMAGE_PREFIX"], "ghcr.io/myorg")
	}
	if m["COMPILER_WATCH_IMAGE"] != "ghcr.io/myorg/website-compiler-watch:latest" {
		t.Errorf("COMPILER_WATCH_IMAGE: got %q", m["COMPILER_WATCH_IMAGE"])
	}
}

func TestWithImageModelDefaults_ExistingImageRootNotOverwritten(t *testing.T) {
	env := []string{"IMAGE_ROOT=myregistry.com/myorg", "IMAGE_TAG=prod"}
	result := withImageModelDefaults(env)
	m := envMap(result)

	if m["COMPILER_WATCH_IMAGE"] != "myregistry.com/myorg/website-compiler-watch:prod" {
		t.Errorf("COMPILER_WATCH_IMAGE: got %q", m["COMPILER_WATCH_IMAGE"])
	}
}

func TestWithImageModelDefaults_ExplicitImagesNotOverwritten(t *testing.T) {
	env := []string{
		"COMPILER_WATCH_IMAGE=registry/custom-watch:pinned",
		"COMPILER_WATCH_RUNTIME_IMAGE=ubuntu:22.04",
		"WEBSITE_COMPILER_IMAGE=registry/custom-cli:pinned",
		"PREVIEW_IMAGE=caddy:alpine",
	}
	result := withImageModelDefaults(env)
	m := envMap(result)

	if m["COMPILER_WATCH_IMAGE"] != "registry/custom-watch:pinned" {
		t.Errorf("COMPILER_WATCH_IMAGE should not be overwritten: got %q", m["COMPILER_WATCH_IMAGE"])
	}
	if m["COMPILER_WATCH_RUNTIME_IMAGE"] != "ubuntu:22.04" {
		t.Errorf("COMPILER_WATCH_RUNTIME_IMAGE should not be overwritten: got %q", m["COMPILER_WATCH_RUNTIME_IMAGE"])
	}
	if m["WEBSITE_COMPILER_IMAGE"] != "registry/custom-cli:pinned" {
		t.Errorf("WEBSITE_COMPILER_IMAGE should not be overwritten: got %q", m["WEBSITE_COMPILER_IMAGE"])
	}
	if m["PREVIEW_IMAGE"] != "caddy:alpine" {
		t.Errorf("PREVIEW_IMAGE should not be overwritten: got %q", m["PREVIEW_IMAGE"])
	}
}

func TestWithImageModelDefaults_ExistingImagePrefixNotOverwritten(t *testing.T) {
	env := []string{"PREFIX=myorg", "IMAGE_PREFIX=custom-prefix"}
	result := withImageModelDefaults(env)
	m := envMap(result)

	if m["IMAGE_PREFIX"] != "custom-prefix" {
		t.Errorf("IMAGE_PREFIX should not be overwritten: got %q", m["IMAGE_PREFIX"])
	}
}

func TestWithImageModelDefaults_CustomImageNames(t *testing.T) {
	env := []string{
		"PREFIX=myorg",
		"COMPILER_IMAGE_NAME=my-compiler",
		"COMPILER_WATCH_IMAGE_NAME=my-watch",
	}
	result := withImageModelDefaults(env)
	m := envMap(result)

	if !strings.Contains(m["WEBSITE_COMPILER_IMAGE"], "my-compiler") {
		t.Errorf("WEBSITE_COMPILER_IMAGE should use custom name: got %q", m["WEBSITE_COMPILER_IMAGE"])
	}
	if !strings.Contains(m["COMPILER_WATCH_IMAGE"], "my-watch") {
		t.Errorf("COMPILER_WATCH_IMAGE should use custom name: got %q", m["COMPILER_WATCH_IMAGE"])
	}
}

// ── buildArgs ────────────────────────────────────────────────────────────────

func TestBuildArgs_Minimal(t *testing.T) {
	cfg := config.Config{WebsiteRoot: "/site", OutDir: "/dist"}
	args := buildArgs(cfg, false)

	if args[0] != "build" {
		t.Errorf("first arg: got %q, want %q", args[0], "build")
	}
	if !containsSequence(args, "-website-root", "/site") {
		t.Errorf("missing -website-root /site in %v", args)
	}
	if !containsSequence(args, "-out", "/dist") {
		t.Errorf("missing -out /dist in %v", args)
	}
	if contains(args, "-inline-assets") {
		t.Errorf("inline-assets should not be present when inline=false")
	}
}

func TestBuildArgs_Inline(t *testing.T) {
	cfg := config.Config{WebsiteRoot: "/site", OutDir: "/dist"}
	args := buildArgs(cfg, true)
	if !contains(args, "-inline-assets") {
		t.Errorf("expected -inline-assets in args: %v", args)
	}
}

func TestBuildArgs_WithSiteData(t *testing.T) {
	cfg := config.Config{
		WebsiteRoot:    "/site",
		OutDir:         "/dist",
		SiteDataSource: "/data/site.yaml",
	}
	args := buildArgs(cfg, false)
	if !containsSequence(args, "-site-data", "/data/site.yaml") {
		t.Errorf("missing -site-data in %v", args)
	}
}

func TestBuildArgs_WithSitemapURL(t *testing.T) {
	cfg := config.Config{
		WebsiteRoot:    "/site",
		OutDir:         "/dist",
		SitemapBaseURL: "https://example.com",
	}
	args := buildArgs(cfg, false)
	if !containsSequence(args, "-sitemap-base-url", "https://example.com") {
		t.Errorf("missing -sitemap-base-url in %v", args)
	}
}

func TestBuildArgs_WithMirrorExternalAssets(t *testing.T) {
	cfg := config.Config{
		WebsiteRoot:          "/site",
		OutDir:               "/dist",
		MirrorExternalAssets: true,
	}
	args := buildArgs(cfg, false)
	if !contains(args, "-mirror-external-assets") {
		t.Errorf("missing -mirror-external-assets in %v", args)
	}
}

func TestBuildArgs_EmptySiteDataSkipped(t *testing.T) {
	cfg := config.Config{WebsiteRoot: "/site", OutDir: "/dist", SiteDataSource: "   "}
	args := buildArgs(cfg, false)
	if contains(args, "-site-data") {
		t.Errorf("-site-data should not appear for whitespace-only source: %v", args)
	}
}

// ── buildsSHA7 ───────────────────────────────────────────────────────────────

func TestBuildsSHA7(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"abc", "abc"},
		{"abc1234", "abc1234"},
		{"abc123456789", "abc1234"},
	}
	for _, tc := range cases {
		got := buildsSHA7(tc.input)
		if got != tc.want {
			t.Errorf("buildsSHA7(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ── buildsRegion ─────────────────────────────────────────────────────────────

func TestBuildsRegion_UsesBuildsRegionWhenSet(t *testing.T) {
	cfg := config.Config{
		Builds:  config.BuildsConfig{Region: "us-east-1"},
		Publish: config.PublishConfig{Region: "eu-west-1"},
	}
	if got := buildsRegion(cfg); got != "us-east-1" {
		t.Errorf("got %q, want %q", got, "us-east-1")
	}
}

func TestBuildsRegion_FallsBackToPublishRegion(t *testing.T) {
	cfg := config.Config{
		Builds:  config.BuildsConfig{Region: ""},
		Publish: config.PublishConfig{Region: "eu-west-1"},
	}
	if got := buildsRegion(cfg); got != "eu-west-1" {
		t.Errorf("got %q, want %q", got, "eu-west-1")
	}
}

// ── parseSHAFlag ─────────────────────────────────────────────────────────────

func TestParseSHAFlag_Valid(t *testing.T) {
	sha, err := parseSHAFlag("upload-build", []string{"--sha", "abc1234"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != "abc1234" {
		t.Errorf("got %q, want %q", sha, "abc1234")
	}
}

func TestParseSHAFlag_MissingSha(t *testing.T) {
	_, err := parseSHAFlag("upload-build", []string{})
	if err == nil {
		t.Fatal("expected error for missing --sha")
	}
}

func TestParseSHAFlag_WhitespaceSha(t *testing.T) {
	_, err := parseSHAFlag("upload-build", []string{"--sha", "   "})
	if err == nil {
		t.Fatal("expected error for whitespace --sha")
	}
}

// ── publishEnv ───────────────────────────────────────────────────────────────

func TestPublishEnv_HappyPath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "site.yaml")
	distDir := filepath.Join(dir, "dist")
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		OutDir: distDir,
		ComposeEnv: map[string]string{
			"WORKSPACE_ROOT": ".",
		},
		Publish: config.PublishConfig{
			Bucket:                   "my-bucket",
			Prefix:                   "/prefix",
			Region:                   "us-east-1",
			CloudFrontDistributionID: "ABCDEF123",
			CloudFrontPaths:          []string{"/css/*", "/js/*"},
		},
	}

	env, err := publishEnv(cfg, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if env["PUBLISH_BUCKET"] != "my-bucket" {
		t.Errorf("PUBLISH_BUCKET: got %q", env["PUBLISH_BUCKET"])
	}
	if env["PUBLISH_REGION"] != "us-east-1" {
		t.Errorf("PUBLISH_REGION: got %q", env["PUBLISH_REGION"])
	}
	if env["CLOUDFRONT_DISTRIBUTION_ID"] != "ABCDEF123" {
		t.Errorf("CLOUDFRONT_DISTRIBUTION_ID: got %q", env["CLOUDFRONT_DISTRIBUTION_ID"])
	}
	if env["CLOUDFRONT_PATHS"] != "/css/* /js/*" {
		t.Errorf("CLOUDFRONT_PATHS: got %q", env["CLOUDFRONT_PATHS"])
	}
	if env["PUBLISH_DIR"] == "" {
		t.Error("PUBLISH_DIR should be non-empty")
	}
}

func TestPublishEnv_DefaultCloudFrontPaths(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "site.yaml")
	distDir := filepath.Join(dir, "dist")
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		OutDir:     distDir,
		ComposeEnv: map[string]string{"WORKSPACE_ROOT": "."},
		Publish:    config.PublishConfig{Bucket: "b"},
	}

	env, err := publishEnv(cfg, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env["CLOUDFRONT_PATHS"] != "/*" {
		t.Errorf("CLOUDFRONT_PATHS default: got %q, want %q", env["CLOUDFRONT_PATHS"], "/*")
	}
}

func TestPublishEnv_EmptyWorkspaceRootDefaultsToDot(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "site.yaml")
	distDir := filepath.Join(dir, "dist")
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		OutDir:  distDir,
		Publish: config.PublishConfig{Bucket: "b"},
	}

	_, err := publishEnv(cfg, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPublishEnv_OutDirNotUnderWorkspace(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "site.yaml")
	otherDir := t.TempDir()

	cfg := config.Config{
		OutDir:     otherDir,
		ComposeEnv: map[string]string{"WORKSPACE_ROOT": "."},
		Publish:    config.PublishConfig{Bucket: "b"},
	}

	_, err := publishEnv(cfg, cfgPath)
	if err == nil {
		t.Fatal("expected error when out_dir is not under workspace root")
	}
}

// ── resolvePath ──────────────────────────────────────────────────────────────

func TestResolvePath_Empty(t *testing.T) {
	if got := resolvePath("/base", ""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestResolvePath_URL(t *testing.T) {
	url := "https://example.com/data.yaml"
	if got := resolvePath("/base", url); got != url {
		t.Errorf("URL should be unchanged: got %q", got)
	}
}

func TestResolvePath_Absolute(t *testing.T) {
	abs := "/absolute/path"
	if got := resolvePath("/base", abs); got != abs {
		t.Errorf("absolute path should be unchanged: got %q", got)
	}
}

func TestResolvePath_Relative(t *testing.T) {
	got := resolvePath("/base/dir", "../sibling")
	if got != "/base/sibling" {
		t.Errorf("got %q, want %q", got, "/base/sibling")
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func contains(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}

func containsSequence(args []string, a, b string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}
