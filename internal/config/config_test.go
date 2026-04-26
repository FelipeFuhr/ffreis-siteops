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
