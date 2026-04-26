package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ProjectName          string            `yaml:"project_name"`
	CompilerCommand      string            `yaml:"compiler_command"`
	WebsiteRoot          string            `yaml:"website_root"`
	OutDir               string            `yaml:"out_dir"`
	SiteDataSource       string            `yaml:"site_data_source"`
	SitemapBaseURL       string            `yaml:"sitemap_base_url"`
	MirrorExternalAssets bool              `yaml:"mirror_external_assets"`
	DefaultAddr          string            `yaml:"default_addr"`
	ContainerCommand     string            `yaml:"container_command"`
	ComposeCommand       []string          `yaml:"compose_command"`
	ComposeFile          string            `yaml:"compose_file"`
	ComposeEnv           map[string]string `yaml:"compose_env"`
	Publish              PublishConfig     `yaml:"publish"`
	Builds               BuildsConfig      `yaml:"builds"`
}

// BuildsConfig holds settings for the build artifact staging bucket.
// Builds are stored at s3://{Bucket}/{Source}/{sha7}/ and can be promoted
// to the live bucket via the promote command.
type BuildsConfig struct {
	// Bucket is the S3 bucket used to store build artifacts.
	Bucket string `yaml:"bucket"`
	// Source is this repo's identifier within the builds bucket (used as path prefix).
	Source string `yaml:"source"`
	// Region optionally overrides the AWS region; defaults to publish.region.
	Region string `yaml:"region"`
}

type PublishConfig struct {
	// Bucket is the S3 bucket to sync the built site into.
	// In a bucket-per-domain setup, this is typically one bucket per domain.
	Bucket string `yaml:"bucket"`
	// Prefix is the S3 prefix to sync to. Optional; defaults to "/" (bucket root).
	Prefix string `yaml:"prefix"`
	// NoDelete disables remote deletions (upload/update only).
	NoDelete bool `yaml:"no_delete"`
	// Region optionally overrides the AWS region for S3 operations.
	Region string `yaml:"region"`

	CloudFrontDistributionID string   `yaml:"cloudfront_distribution_id"`
	CloudFrontPaths          []string `yaml:"cloudfront_invalidate_paths"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("reading config file: %w", err)
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("parsing yaml: %w", err)
	}
	if _, exists := raw["site_data_contract_source"]; exists {
		return Config{}, fmt.Errorf("site_data_contract_source is no longer supported; keep src/data/site.contract.yaml in the website repo")
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing yaml: %w", err)
	}

	if cfg.CompilerCommand == "" {
		return Config{}, fmt.Errorf("compiler_command is required")
	}
	if cfg.WebsiteRoot == "" {
		return Config{}, fmt.Errorf("website_root is required")
	}
	if cfg.OutDir == "" {
		return Config{}, fmt.Errorf("out_dir is required")
	}

	configDir := filepath.Dir(path)
	cfg.CompilerCommand = resolvePath(configDir, cfg.CompilerCommand)
	cfg.WebsiteRoot = resolvePath(configDir, cfg.WebsiteRoot)
	cfg.OutDir = resolvePath(configDir, cfg.OutDir)
	cfg.SiteDataSource = resolvePath(configDir, cfg.SiteDataSource)
	if cfg.ComposeFile != "" {
		cfg.ComposeFile = resolvePath(configDir, cfg.ComposeFile)
	}

	for i := range cfg.ComposeCommand {
		if i == 0 {
			cfg.ComposeCommand[i] = resolvePath(configDir, cfg.ComposeCommand[i])
		}
	}

	if cfg.DefaultAddr == "" {
		cfg.DefaultAddr = ":8080"
	}
	if strings.TrimSpace(cfg.Publish.Prefix) == "" {
		cfg.Publish.Prefix = "/"
	}
	if len(cfg.Publish.CloudFrontPaths) == 0 {
		cfg.Publish.CloudFrontPaths = []string{"/*"}
	}

	return cfg, nil
}

// inventoryYAML is the parse target for a websites-inventory YAML file.
// It is a different schema from the siteops local config file.
type inventoryYAML struct {
	Website string `yaml:"website"`
	Builds  struct {
		Bucket string `yaml:"bucket"`
		Region string `yaml:"region"`
	} `yaml:"builds"`
	Publish struct {
		Bucket                    string   `yaml:"bucket"`
		Region                    string   `yaml:"region"`
		CloudFrontInvalidatePaths []string `yaml:"cloudfront_invalidate_paths"`
	} `yaml:"publish"`
}

// LoadFromInventory parses a websites-inventory YAML file (e.g. flemming.yaml)
// and returns a Config suitable for the builds-related commands (upload-build,
// promote, list-builds). Fields that require local paths (compiler_command,
// website_root, out_dir) are not set — those commands do not need them.
func LoadFromInventory(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("reading inventory file: %w", err)
	}

	var inv inventoryYAML
	if err := yaml.Unmarshal(data, &inv); err != nil {
		return Config{}, fmt.Errorf("parsing inventory yaml: %w", err)
	}
	if strings.TrimSpace(inv.Website) == "" {
		return Config{}, fmt.Errorf("inventory file must have a non-empty 'website' field")
	}

	paths := inv.Publish.CloudFrontInvalidatePaths
	if len(paths) == 0 {
		paths = []string{"/*"}
	}

	return Config{
		ProjectName: inv.Website,
		DefaultAddr: ":8080",
		Builds: BuildsConfig{
			Bucket: inv.Builds.Bucket,
			Source: inv.Website,
			Region: inv.Builds.Region,
		},
		Publish: PublishConfig{
			Bucket:          inv.Publish.Bucket,
			Prefix:          "/",
			Region:          inv.Publish.Region,
			CloudFrontPaths: paths,
		},
	}, nil
}

func ValidateForCommand(cfg Config, command string) error {
	for _, check := range requiredForCommand(command) {
		if err := check(cfg, command); err != nil {
			return err
		}
	}
	return nil
}

const (
	fieldBuildsBucket  = "builds.bucket"
	fieldBuildsSource  = "builds.source"
	fieldPublishBucket = "publish.bucket"
)

type commandRequirement func(cfg Config, command string) error

func requiredForCommand(command string) []commandRequirement {
	switch command {
	case "build", "build-inline", "serve", "validate-site-data", "validate-assets":
		return []commandRequirement{
			requireNonEmpty("compiler_command", func(cfg Config) string { return cfg.CompilerCommand }),
			requireNonEmpty("website_root", func(cfg Config) string { return cfg.WebsiteRoot }),
		}
	case "publish", "deploy":
		return []commandRequirement{
			requireNonEmpty("compiler_command", func(cfg Config) string { return cfg.CompilerCommand }),
			requireNonEmpty("website_root", func(cfg Config) string { return cfg.WebsiteRoot }),
			requireNonEmpty("out_dir", func(cfg Config) string { return cfg.OutDir }),
			requireNonEmpty(fieldPublishBucket, func(cfg Config) string { return cfg.Publish.Bucket }),
			requireComposeCommand(),
		}
	case "upload-build":
		return []commandRequirement{
			requireNonEmpty("out_dir", func(cfg Config) string { return cfg.OutDir }),
			requireNonEmpty(fieldBuildsBucket, func(cfg Config) string { return cfg.Builds.Bucket }),
			requireNonEmpty(fieldBuildsSource, func(cfg Config) string { return cfg.Builds.Source }),
		}
	case "promote":
		return []commandRequirement{
			requireNonEmpty(fieldBuildsBucket, func(cfg Config) string { return cfg.Builds.Bucket }),
			requireNonEmpty(fieldBuildsSource, func(cfg Config) string { return cfg.Builds.Source }),
			requireNonEmpty(fieldPublishBucket, func(cfg Config) string { return cfg.Publish.Bucket }),
		}
	case "list-builds":
		return []commandRequirement{
			requireNonEmpty(fieldBuildsBucket, func(cfg Config) string { return cfg.Builds.Bucket }),
			requireNonEmpty(fieldBuildsSource, func(cfg Config) string { return cfg.Builds.Source }),
		}
	case "deploy-local",
		"compose-up", "compose-down", "compose-logs", "compose-rebuild",
		"docker-up", "docker-down", "docker-logs", "docker-rebuild":
		return []commandRequirement{requireComposeCommand()}
	default:
		return nil
	}
}

func requireNonEmpty(field string, get func(cfg Config) string) commandRequirement {
	return func(cfg Config, command string) error {
		if strings.TrimSpace(get(cfg)) == "" {
			return fmt.Errorf("%s is required for %s", field, command)
		}
		return nil
	}
}

func requireComposeCommand() commandRequirement {
	return func(cfg Config, command string) error {
		if len(cfg.ComposeCommand) == 0 {
			return fmt.Errorf("compose_command is required for %s", command)
		}
		return nil
	}
}

func resolvePath(baseDir, v string) string {
	if v == "" {
		return v
	}
	if strings.Contains(v, "://") {
		return v
	}
	if filepath.IsAbs(v) {
		return v
	}
	return filepath.Clean(filepath.Join(baseDir, v))
}
