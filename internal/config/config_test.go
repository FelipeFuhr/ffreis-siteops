package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_ResolvesRelativePaths(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "site.yaml")
	raw := `project_name: "demo"
compiler_command: "../compiler/website-compiler"
website_root: "../site"
out_dir: "../dist"
compose_command:
  - "../compose.sh"
compose_file: "../docker-compose.yml"
`
	if err := os.WriteFile(cfgPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if !filepath.IsAbs(cfg.CompilerCommand) || !filepath.IsAbs(cfg.WebsiteRoot) || !filepath.IsAbs(cfg.OutDir) {
		t.Fatalf("expected absolute resolved paths: %+v", cfg)
	}
}

func TestValidateForCommand_ComposeRequiresComposeCommand(t *testing.T) {
	cfg := Config{
		CompilerCommand: "/tmp/compiler",
		WebsiteRoot:     "/tmp/site",
		OutDir:          "/tmp/dist",
	}
	if err := ValidateForCommand(cfg, "compose-up"); err == nil {
		t.Fatal("expected validation error for missing compose command")
	}
}

func TestValidateForCommand_BuildRequiresCompilerFields(t *testing.T) {
	cfg := Config{
		OutDir: "/tmp/dist",
	}
	if err := ValidateForCommand(cfg, "build"); err == nil {
		t.Fatal("expected build validation error")
	}
}

func TestValidateForCommand_PublishRequiresBucketAndCompose(t *testing.T) {
	cfg := Config{
		CompilerCommand: "/tmp/compiler",
		WebsiteRoot:     "/tmp/site",
		OutDir:          "/tmp/dist",
		Publish: PublishConfig{
			Bucket: "example.com",
		},
	}
	if err := ValidateForCommand(cfg, "publish"); err == nil {
		t.Fatal("expected publish validation error for missing compose_command")
	}

	cfg.ComposeCommand = []string{"docker", "compose"}
	cfg.Publish.Bucket = ""
	if err := ValidateForCommand(cfg, "publish"); err == nil {
		t.Fatal("expected publish validation error for missing bucket")
	}
}

func TestLoad_PreservesURLSiteDataSource(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "site.yaml")
	raw := `project_name: "demo"
compiler_command: "../compiler/website-compiler"
website_root: "../site"
out_dir: "../dist"
site_data_source: "https://example.com/site.yaml"
publish:
  bucket: "example.com"
`
	if err := os.WriteFile(cfgPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.SiteDataSource != "https://example.com/site.yaml" {
		t.Fatalf("expected site_data_source URL to remain unchanged, got %q", cfg.SiteDataSource)
	}
}

func TestLoad_PublishDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "site.yaml")
	raw := `project_name: "demo"
compiler_command: "../compiler/website-compiler"
website_root: "../site"
out_dir: "../dist"
publish:
  bucket: "example.com"
`
	if err := os.WriteFile(cfgPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Publish.Prefix != "/" {
		t.Fatalf("publish prefix: got %q, want %q", cfg.Publish.Prefix, "/")
	}
	if len(cfg.Publish.CloudFrontPaths) != 1 || cfg.Publish.CloudFrontPaths[0] != "/*" {
		t.Fatalf("cloudfront paths: got %v, want [\"/*\"]", cfg.Publish.CloudFrontPaths)
	}
}

func TestLoad_FailsForDeprecatedSiteDataContractSource(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "site.yaml")
	raw := `project_name: "demo"
compiler_command: "../compiler/website-compiler"
website_root: "../site"
out_dir: "../dist"
site_data_contract_source: "https://example.com/contract.yaml"
`
	if err := os.WriteFile(cfgPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected deprecated site_data_contract_source error")
	}
	if err.Error() != "site_data_contract_source is no longer supported; keep src/data/site.contract.yaml in the website repo" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/site.yaml")
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "bad.yaml")
	if err := os.WriteFile(cfgPath, []byte("compiler_command: [not: valid: yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoad_MissingCompilerCommand(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "site.yaml")
	raw := "website_root: \"../site\"\nout_dir: \"../dist\"\n"
	if err := os.WriteFile(cfgPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for missing compiler_command")
	}
}

func TestLoad_MissingWebsiteRoot(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "site.yaml")
	raw := "compiler_command: \"../compiler\"\nout_dir: \"../dist\"\n"
	if err := os.WriteFile(cfgPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for missing website_root")
	}
}

func TestLoad_MissingOutDir(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "site.yaml")
	raw := "compiler_command: \"../compiler\"\nwebsite_root: \"../site\"\n"
	if err := os.WriteFile(cfgPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for missing out_dir")
	}
}

func TestLoad_DefaultAddr(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "site.yaml")
	raw := "compiler_command: \"../compiler\"\nwebsite_root: \"../site\"\nout_dir: \"../dist\"\n"
	if err := os.WriteFile(cfgPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DefaultAddr != ":8080" {
		t.Errorf("DefaultAddr: got %q, want %q", cfg.DefaultAddr, ":8080")
	}
}

// ── LoadFromInventory ────────────────────────────────────────────────────────

func TestLoadFromInventory_HappyPath(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "inventory.yaml")
	content := `
website: my-website
builds:
  bucket: builds-bucket
  region: us-east-1
publish:
  bucket: live-bucket
  region: us-east-2
  cloudfront_invalidate_paths:
    - /css/*
    - /js/*
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFromInventory(path, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ProjectName != "my-website" {
		t.Errorf("ProjectName: got %q", cfg.ProjectName)
	}
	if cfg.Builds.Bucket != "builds-bucket" {
		t.Errorf("Builds.Bucket: got %q", cfg.Builds.Bucket)
	}
	if cfg.Builds.Source != "my-website" {
		t.Errorf("Builds.Source: got %q", cfg.Builds.Source)
	}
	if cfg.Builds.Region != "us-east-1" {
		t.Errorf("Builds.Region: got %q", cfg.Builds.Region)
	}
	if cfg.Publish.Bucket != "live-bucket" {
		t.Errorf("Publish.Bucket: got %q", cfg.Publish.Bucket)
	}
	if cfg.Publish.Region != "us-east-2" {
		t.Errorf("Publish.Region: got %q", cfg.Publish.Region)
	}
	if cfg.Publish.Prefix != "/" {
		t.Errorf("Publish.Prefix: got %q, want /", cfg.Publish.Prefix)
	}
	if len(cfg.Publish.CloudFrontPaths) != 2 || cfg.Publish.CloudFrontPaths[0] != "/css/*" {
		t.Errorf("CloudFrontPaths: got %v", cfg.Publish.CloudFrontPaths)
	}
	if cfg.DefaultAddr != ":8080" {
		t.Errorf("DefaultAddr: got %q", cfg.DefaultAddr)
	}
}

func TestLoadFromInventory_DefaultCloudFrontPaths(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "inventory.yaml")
	content := "website: my-site\nbuilds:\n  bucket: b\npublish:\n  bucket: p\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFromInventory(path, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Publish.CloudFrontPaths) != 1 || cfg.Publish.CloudFrontPaths[0] != "/*" {
		t.Errorf("expected default CloudFrontPaths=[/*], got %v", cfg.Publish.CloudFrontPaths)
	}
}

func TestLoadFromInventory_EmptyWebsite(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "inventory.yaml")
	content := "website: \"\"\nbuilds:\n  bucket: b\npublish:\n  bucket: p\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFromInventory(path, "")
	if err == nil {
		t.Fatal("expected error for empty website field")
	}
}

func TestLoadFromInventory_FileNotFound(t *testing.T) {
	_, err := LoadFromInventory("/nonexistent/path/inventory.yaml", "")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadFromInventory_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "bad.yaml")
	if err := os.WriteFile(path, []byte("website: [not: valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFromInventory(path, "")
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoadFromInventory_NamedDeployment(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "inventory.yaml")
	content := `
website: my-site
builds:
  bucket: builds-bucket
  region: us-east-1
publish:
  bucket: live-bucket
  region: us-east-1
  cloudfront_invalidate_paths: ["/*"]
deployments:
  production:
    publish:
      prefix: ""
      cloudfront_invalidate_paths: ["/*"]
  dev:
    publish:
      prefix: dev/
      cloudfront_invalidate_paths: ["/dev/*"]
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFromInventory(path, "dev")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Builds.Source != "my-site/dev" {
		t.Errorf("Builds.Source: got %q, want %q", cfg.Builds.Source, "my-site/dev")
	}
	if cfg.Publish.Prefix != "dev/" {
		t.Errorf("Publish.Prefix: got %q, want %q", cfg.Publish.Prefix, "dev/")
	}
	if len(cfg.Publish.CloudFrontPaths) != 1 || cfg.Publish.CloudFrontPaths[0] != "/dev/*" {
		t.Errorf("CloudFrontPaths: got %v", cfg.Publish.CloudFrontPaths)
	}
}

func TestLoadFromInventory_NamedDeploymentInheritsTopLevel(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "inventory.yaml")
	content := `
website: my-site
builds:
  bucket: builds-bucket
  region: us-east-1
publish:
  bucket: live-bucket
  region: us-east-2
deployments:
  staging:
    publish:
      prefix: staging/
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFromInventory(path, "staging")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Publish.Bucket != "live-bucket" {
		t.Errorf("Publish.Bucket: got %q, want inherited %q", cfg.Publish.Bucket, "live-bucket")
	}
	if cfg.Publish.Region != "us-east-2" {
		t.Errorf("Publish.Region: got %q, want inherited %q", cfg.Publish.Region, "us-east-2")
	}
	if cfg.Publish.Prefix != "staging/" {
		t.Errorf("Publish.Prefix: got %q, want %q", cfg.Publish.Prefix, "staging/")
	}
}

func TestLoadFromInventory_MissingDeploymentFlag(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "inventory.yaml")
	content := "website: my-site\nbuilds:\n  bucket: b\npublish:\n  bucket: p\ndeployments:\n  production:\n    publish:\n      prefix: \"\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFromInventory(path, "")
	if err == nil {
		t.Fatal("expected error when deployment flag is missing")
	}
}

func TestLoadFromInventory_UnknownDeployment(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "inventory.yaml")
	content := "website: my-site\nbuilds:\n  bucket: b\npublish:\n  bucket: p\ndeployments:\n  production:\n    publish:\n      prefix: \"\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFromInventory(path, "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown deployment name")
	}
}

// ── ValidateForCommand ───────────────────────────────────────────────────────

func TestValidateForCommand_UnknownCommand(t *testing.T) {
	if err := ValidateForCommand(Config{}, "unknown-command"); err != nil {
		t.Errorf("expected nil for unknown command, got: %v", err)
	}
}

func TestValidateForCommand_BuildSuccess(t *testing.T) {
	cfg := Config{CompilerCommand: "/compiler", WebsiteRoot: "/site"}
	if err := ValidateForCommand(cfg, "build"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateForCommand_ValidateAssets(t *testing.T) {
	cfg := Config{CompilerCommand: "/compiler", WebsiteRoot: "/site"}
	if err := ValidateForCommand(cfg, "validate-assets"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateForCommand_UploadBuild_MissingFields(t *testing.T) {
	cfg := Config{OutDir: "/dist"}
	if err := ValidateForCommand(cfg, "upload-build"); err == nil {
		t.Fatal("expected error for missing builds.bucket")
	}
}

func TestValidateForCommand_UploadBuild_Success(t *testing.T) {
	cfg := Config{
		OutDir: "/dist",
		Builds: BuildsConfig{Bucket: "builds", Source: "my-site"},
	}
	if err := ValidateForCommand(cfg, "upload-build"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateForCommand_Promote_Success(t *testing.T) {
	cfg := Config{
		Builds:  BuildsConfig{Bucket: "builds", Source: "my-site"},
		Publish: PublishConfig{Bucket: "live"},
	}
	if err := ValidateForCommand(cfg, "promote"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateForCommand_Promote_MissingPublishBucket(t *testing.T) {
	cfg := Config{
		Builds: BuildsConfig{Bucket: "builds", Source: "my-site"},
	}
	if err := ValidateForCommand(cfg, "promote"); err == nil {
		t.Fatal("expected error for missing publish.bucket")
	}
}

func TestValidateForCommand_ListBuilds_Success(t *testing.T) {
	cfg := Config{Builds: BuildsConfig{Bucket: "builds", Source: "my-site"}}
	if err := ValidateForCommand(cfg, "list-builds"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateForCommand_ListBuilds_MissingSource(t *testing.T) {
	cfg := Config{Builds: BuildsConfig{Bucket: "builds"}}
	if err := ValidateForCommand(cfg, "list-builds"); err == nil {
		t.Fatal("expected error for missing builds.source")
	}
}

// ── resolvePath ──────────────────────────────────────────────────────────────

func TestResolvePath_Empty(t *testing.T) {
	if got := resolvePath("/base", ""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestResolvePath_URL(t *testing.T) {
	url := "s3://my-bucket/prefix"
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
