package cli

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"ffreis-siteops/internal/config"
	"ffreis-siteops/internal/logx"
	"ffreis-siteops/internal/runner"
)

const (
	flagWebsiteRoot  = "-website-root"
	flagSiteData     = "-site-data"
	flagAWSRegion    = "--region"
	flagComposeBuild = "--build"
)

func Run(appName string, args []string) int {
	logger := logx.New(appName)
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fs := flag.NewFlagSet(appName, flag.ContinueOnError)
	cfgPath := fs.String("config", "config/site.local.yaml", "path to siteops yaml config")
	inventoryPath := fs.String("inventory", "", "path to websites-inventory yaml (alternative to -config)")
	deploymentName := fs.String("deployment", "", "deployment name from the inventory deployments map (requires -inventory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cmdArgs := fs.Args()
	if len(cmdArgs) == 0 {
		printUsage(appName)
		return 2
	}

	cfg, loadErr := loadConfig(*cfgPath, *inventoryPath, *deploymentName)
	if loadErr != nil {
		logger.Error("failed to load config", "error", loadErr)
		return 1
	}
	logger.Debug("loaded config", "project_name", cfg.ProjectName)

	cmd := cmdArgs[0]
	extra := cmdArgs[1:]
	var err error
	if err = config.ValidateForCommand(cfg, cmd); err != nil {
		logger.Error("invalid command/config combination", "command", cmd, "error", err)
		return 2
	}

	switch cmd {
	case "build":
		err = runCompiler(rootCtx, logger, cfg, append(buildArgs(cfg, false), extra...)...)
	case "build-inline":
		err = runCompiler(rootCtx, logger, cfg, append(buildArgs(cfg, true), extra...)...)
	case "publish":
		err = runPublish(rootCtx, logger, cfg, *cfgPath)
	case "serve":
		addr := cfg.DefaultAddr
		serveArgs := []string{"serve", flagWebsiteRoot, cfg.WebsiteRoot, "-addr", addr}
		if strings.TrimSpace(cfg.SiteDataSource) != "" {
			serveArgs = append(serveArgs, flagSiteData, cfg.SiteDataSource)
		}
		err = runCompiler(rootCtx, logger, cfg, append(serveArgs, extra...)...)
	case "watch":
		err = runWatch(rootCtx, logger, cfg)
	case "validate-site-data":
		validateArgs := []string{"validate-site-data", flagWebsiteRoot, cfg.WebsiteRoot}
		if strings.TrimSpace(cfg.SiteDataSource) != "" {
			validateArgs = append(validateArgs, flagSiteData, cfg.SiteDataSource)
		}
		err = runCompiler(rootCtx, logger, cfg, append(validateArgs, extra...)...)
	case "validate-assets":
		validateArgs := []string{"validate-assets", flagWebsiteRoot, cfg.WebsiteRoot}
		if strings.TrimSpace(cfg.SiteDataSource) != "" {
			validateArgs = append(validateArgs, flagSiteData, cfg.SiteDataSource)
		}
		err = runCompiler(rootCtx, logger, cfg, append(validateArgs, extra...)...)
	case "clean":
		logger.Info("cleaning output directory", "out_dir", cfg.OutDir)
		err = os.RemoveAll(cfg.OutDir)
	case "compose-up", "docker-up":
		err = runCompose(rootCtx, logger, cfg, append([]string{"up", flagComposeBuild}, extra...)...)
	case "compose-down", "docker-down":
		err = runCompose(rootCtx, logger, cfg, append([]string{"down"}, extra...)...)
	case "compose-logs", "docker-logs":
		err = runCompose(rootCtx, logger, cfg, append([]string{"logs", "-f"}, extra...)...)
	case "compose-rebuild", "docker-rebuild":
		err = runCompose(rootCtx, logger, cfg, append([]string{"up", flagComposeBuild, "--force-recreate"}, extra...)...)
	case "deploy":
		err = runPublish(rootCtx, logger, cfg, *cfgPath)
	case "deploy-local":
		err = runCompose(rootCtx, logger, cfg, append([]string{"up", flagComposeBuild}, extra...)...)
	case "upload-build", "promote", "list-builds":
		return runBuildsCmd(rootCtx, logger, cfg, cmd, extra)
	case "help", "-h", "--help":
		printUsage(appName)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		printUsage(appName)
		return 2
	}

	if err != nil {
		logger.Error("command failed", "command", cmd, "error", err)
		return 1
	}
	return 0
}

func buildArgs(cfg config.Config, inline bool) []string {
	args := []string{"build", flagWebsiteRoot, cfg.WebsiteRoot, "-out", cfg.OutDir}
	if strings.TrimSpace(cfg.SiteDataSource) != "" {
		args = append(args, flagSiteData, cfg.SiteDataSource)
	}
	if strings.TrimSpace(cfg.SitemapBaseURL) != "" {
		args = append(args, "-sitemap-base-url", cfg.SitemapBaseURL)
	}
	if cfg.MirrorExternalAssets {
		args = append(args, "-mirror-external-assets")
	}
	if inline {
		args = append(args, "-inline-assets")
	}
	return args
}

func runPublish(ctx context.Context, logger *slog.Logger, cfg config.Config, cfgPath string) error {
	if err := runPublishCompiler(ctx, logger, cfg); err != nil {
		return err
	}
	env, err := runPublishEnv(cfg, cfgPath)
	if err != nil {
		return err
	}
	if err := runPublishPublisher(ctx, logger, cfg, env); err != nil {
		return err
	}
	return runPublishInvalidator(ctx, logger, cfg, env)
}

var runPublishCompiler = func(ctx context.Context, logger *slog.Logger, cfg config.Config) error {
	return runCompiler(ctx, logger, cfg, buildArgs(cfg, false)...)
}

var runPublishEnv = func(cfg config.Config, cfgPath string) (map[string]string, error) {
	return publishEnv(cfg, cfgPath)
}

var runPublishPublisher = func(ctx context.Context, logger *slog.Logger, cfg config.Config, env map[string]string) error {
	return runComposeWithEnv(ctx, logger, cfg, env, "run", "--rm", "publisher")
}

var runPublishInvalidator = func(ctx context.Context, logger *slog.Logger, cfg config.Config, env map[string]string) error {
	return runComposeWithEnv(ctx, logger, cfg, env, "run", "--rm", "invalidator")
}

func publishEnv(cfg config.Config, cfgPath string) (map[string]string, error) {
	configDir := filepath.Dir(cfgPath)
	workspaceRoot := cfg.ComposeEnv["WORKSPACE_ROOT"]
	if strings.TrimSpace(workspaceRoot) == "" {
		workspaceRoot = "."
	}
	workspaceRoot = resolvePath(configDir, workspaceRoot)
	workspaceRootAbs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving WORKSPACE_ROOT: %w", err)
	}

	outDirAbs, err := filepath.Abs(cfg.OutDir)
	if err != nil {
		return nil, fmt.Errorf("resolving out_dir: %w", err)
	}

	relOutDir, err := filepath.Rel(workspaceRootAbs, outDirAbs)
	if err != nil {
		return nil, fmt.Errorf("computing out_dir relative to WORKSPACE_ROOT: %w", err)
	}
	if relOutDir == "." || strings.HasPrefix(relOutDir, ".."+string(filepath.Separator)) || relOutDir == ".." {
		return nil, fmt.Errorf("out_dir must be under compose_env.WORKSPACE_ROOT for publish (out_dir=%q workspace_root=%q)", cfg.OutDir, workspaceRootAbs)
	}

	paths := cfg.Publish.CloudFrontPaths
	if len(paths) == 0 {
		paths = []string{"/*"}
	}

	return map[string]string{
		"PUBLISH_BUCKET":    cfg.Publish.Bucket,
		"PUBLISH_PREFIX":    cfg.Publish.Prefix,
		"PUBLISH_DIR":       filepath.ToSlash(relOutDir),
		"PUBLISH_NO_DELETE": fmt.Sprintf("%t", cfg.Publish.NoDelete),
		"PUBLISH_REGION":    cfg.Publish.Region,

		"CLOUDFRONT_DISTRIBUTION_ID": cfg.Publish.CloudFrontDistributionID,
		"CLOUDFRONT_PATHS":           strings.Join(paths, " "),
	}, nil
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

var runCompiler = func(ctx context.Context, logger *slog.Logger, cfg config.Config, args ...string) error {
	logger.Info(
		"running compiler command",
		"compiler_command", cfg.CompilerCommand,
		"subcommand", firstArg(args),
		"website_root", cfg.WebsiteRoot,
		"out_dir", cfg.OutDir,
	)
	env := buildCompilerEnv(cfg)
	opts := buildRunnerOptions()
	return runner.Run(ctx, logger, runner.Command{
		Name:   cfg.CompilerCommand,
		Args:   args,
		Env:    env,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}, opts)
}

func buildCompilerEnv(cfg config.Config) []string {
	env := os.Environ()
	if cfg.ContainerCommand != "" {
		env = append(env, "CONTAINER_COMMAND="+cfg.ContainerCommand)
	}
	for k, v := range cfg.ComposeEnv {
		env = append(env, k+"="+v)
	}
	env = withImageModelDefaults(env)
	return env
}

func buildRunnerOptions() runner.Options {
	return runner.Options{
		Timeout:       getEnvDuration("SITEOPS_COMMAND_TIMEOUT", 15*time.Minute),
		ShutdownGrace: getEnvDuration("SITEOPS_SHUTDOWN_GRACE", 10*time.Second),
		MaxAttempts:   3,
		BaseDelay:     100 * time.Millisecond,
		MaxDelay:      1 * time.Second,
	}
}

var runCompose = func(ctx context.Context, logger *slog.Logger, cfg config.Config, args ...string) error {
	return runComposeWithEnv(ctx, logger, cfg, nil, args...)
}

var runComposeWithEnv = func(ctx context.Context, logger *slog.Logger, cfg config.Config, extraEnv map[string]string, args ...string) error {
	if len(cfg.ComposeCommand) == 0 {
		return fmt.Errorf("compose_command is required for compose-* commands")
	}
	composeArgs := append([]string{}, cfg.ComposeCommand[1:]...)
	if cfg.ComposeFile != "" {
		composeArgs = append(composeArgs, "-f", cfg.ComposeFile)
	}
	composeArgs = append(composeArgs, args...)
	logger.Info(
		"running compose command",
		"compose_command", cfg.ComposeCommand[0],
		"compose_file", cfg.ComposeFile,
		"action", firstArg(args),
		"compose_env_keys", strings.Join(sortedMapKeys(cfg.ComposeEnv), ","),
	)
	env := buildComposeEnv(cfg, extraEnv)
	opts := buildRunnerOptions()
	return runner.Run(ctx, logger, runner.Command{
		Name:   cfg.ComposeCommand[0],
		Args:   composeArgs,
		Env:    env,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}, opts)
}

func buildComposeEnv(cfg config.Config, extraEnv map[string]string) []string {
	env := os.Environ()
	if cfg.ContainerCommand != "" {
		env = append(env, "CONTAINER_COMMAND="+cfg.ContainerCommand)
	}
	for k, v := range cfg.ComposeEnv {
		env = append(env, k+"="+v)
	}
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	env = withImageModelDefaults(env)
	return env
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func sortedMapKeys(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	if parsed < 0 {
		return fallback
	}
	return parsed
}

func withImageModelDefaults(env []string) []string {
	current := envToMap(env)

	prefix := strings.TrimSpace(current["PREFIX"])

	imageTag := strings.TrimSpace(current["IMAGE_TAG"])
	if imageTag == "" {
		imageTag = "local"
		env = append(env, "IMAGE_TAG="+imageTag)
	}

	compilerImageName := strings.TrimSpace(current["COMPILER_IMAGE_NAME"])
	if compilerImageName == "" {
		compilerImageName = "website-compiler-cli"
		env = append(env, "COMPILER_IMAGE_NAME="+compilerImageName)
	}

	compilerWatchImageName := strings.TrimSpace(current["COMPILER_WATCH_IMAGE_NAME"])
	if compilerWatchImageName == "" {
		compilerWatchImageName = "website-compiler-watch"
		env = append(env, "COMPILER_WATCH_IMAGE_NAME="+compilerWatchImageName)
	}

	imageProvider := strings.TrimSpace(current["IMAGE_PROVIDER"])
	imagePrefix := prefix
	if imageProvider != "" {
		imagePrefix = imageProvider + "/" + prefix
	}

	if strings.TrimSpace(current["IMAGE_PREFIX"]) == "" {
		env = append(env, "IMAGE_PREFIX="+imagePrefix)
	}

	if strings.TrimSpace(current["IMAGE_ROOT"]) == "" {
		env = append(env, "IMAGE_ROOT="+imagePrefix)
	}

	imageRoot := strings.TrimSpace(current["IMAGE_ROOT"])
	if imageRoot == "" {
		imageRoot = imagePrefix
	}

	if strings.TrimSpace(current["WEBSITE_COMPILER_IMAGE"]) == "" {
		env = append(env, "WEBSITE_COMPILER_IMAGE="+imageRoot+"/"+compilerImageName+":"+imageTag)
	}

	if strings.TrimSpace(current["COMPILER_WATCH_IMAGE"]) == "" {
		env = append(env, "COMPILER_WATCH_IMAGE="+imageRoot+"/"+compilerWatchImageName+":"+imageTag)
	}

	if strings.TrimSpace(current["COMPILER_WATCH_RUNTIME_IMAGE"]) == "" {
		env = append(env, "COMPILER_WATCH_RUNTIME_IMAGE=docker.io/library/debian:bookworm-slim")
	}

	if strings.TrimSpace(current["PREVIEW_IMAGE"]) == "" {
		env = append(env, "PREVIEW_IMAGE=docker.io/library/nginx:alpine")
	}

	return env
}

func envToMap(env []string) map[string]string {
	result := make(map[string]string, len(env))
	for _, item := range env {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			continue
		}
		result[parts[0]] = parts[1]
	}
	return result
}

// runBuildsCmd handles upload-build, promote, and list-builds commands.
// Extracted to keep Run's cognitive complexity within limits.
func runBuildsCmd(ctx context.Context, logger *slog.Logger, cfg config.Config, cmd string, extra []string) int {
	var err error
	switch cmd {
	case "upload-build":
		sha, parseErr := parseSHAFlag(cmd, extra)
		if parseErr != nil {
			logger.Error(parseErr.Error())
			return 2
		}
		err = runUploadBuild(ctx, logger, cfg, sha)
	case "promote":
		sha, parseErr := parseSHAFlag(cmd, extra)
		if parseErr != nil {
			logger.Error(parseErr.Error())
			return 2
		}
		err = runPromote(ctx, logger, cfg, sha)
	case "list-builds":
		err = runListBuilds(ctx, logger, cfg)
	}
	if err != nil {
		logger.Error("command failed", "command", cmd, "error", err)
		return 1
	}
	return 0
}

func parseSHAFlag(cmd string, args []string) (string, error) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	sha := fs.String("sha", "", "git SHA of the build (full or abbreviated)")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if strings.TrimSpace(*sha) == "" {
		return "", fmt.Errorf("--sha is required for %s", cmd)
	}
	return *sha, nil
}

func buildsSHA7(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func buildsRegion(cfg config.Config) string {
	if cfg.Builds.Region != "" {
		return cfg.Builds.Region
	}
	return cfg.Publish.Region
}

var runUploadBuild = func(ctx context.Context, logger *slog.Logger, cfg config.Config, sha string) error {
	sha7 := buildsSHA7(sha)
	dest := fmt.Sprintf("s3://%s/%s/%s/", cfg.Builds.Bucket, cfg.Builds.Source, sha7)
	logger.Info("uploading build artifact", "sha", sha7, "dest", dest)
	return runAWS(ctx, logger, cfg, "s3", "sync", cfg.OutDir+"/", dest,
		flagAWSRegion, buildsRegion(cfg), "--delete")
}

var runPromote = func(ctx context.Context, logger *slog.Logger, cfg config.Config, sha string) error {
	sha7 := buildsSHA7(sha)
	src := fmt.Sprintf("s3://%s/%s/%s/", cfg.Builds.Bucket, cfg.Builds.Source, sha7)

	livePrefix := strings.TrimPrefix(strings.TrimPrefix(cfg.Publish.Prefix, "/"), "/")
	var dest string
	if livePrefix == "" {
		dest = fmt.Sprintf("s3://%s/", cfg.Publish.Bucket)
	} else {
		dest = fmt.Sprintf("s3://%s/%s/", cfg.Publish.Bucket, livePrefix)
	}

	logger.Info("promoting build", "sha", sha7, "src", src, "dest", dest)

	syncArgs := []string{"s3", "sync", src, dest, flagAWSRegion, buildsRegion(cfg)}
	if !cfg.Publish.NoDelete {
		syncArgs = append(syncArgs, "--delete")
	}
	if err := runAWS(ctx, logger, cfg, syncArgs...); err != nil {
		return fmt.Errorf("sync build to live bucket: %w", err)
	}

	if cfg.Publish.CloudFrontDistributionID == "" {
		return nil
	}
	paths := cfg.Publish.CloudFrontPaths
	if len(paths) == 0 {
		paths = []string{"/*"}
	}
	logger.Info("invalidating CloudFront cache", "distribution", cfg.Publish.CloudFrontDistributionID)
	cfArgs := append([]string{"cloudfront", "create-invalidation",
		"--distribution-id", cfg.Publish.CloudFrontDistributionID,
		"--paths"}, paths...)
	return runAWS(ctx, logger, cfg, cfArgs...)
}

var runListBuilds = func(ctx context.Context, logger *slog.Logger, cfg config.Config) error {
	prefix := fmt.Sprintf("s3://%s/%s/", cfg.Builds.Bucket, cfg.Builds.Source)
	logger.Info("listing builds", "prefix", prefix)
	return runAWS(ctx, logger, cfg, "s3", "ls", prefix, flagAWSRegion, buildsRegion(cfg))
}

var runAWS = func(ctx context.Context, logger *slog.Logger, cfg config.Config, args ...string) error {
	commandTimeout := getEnvDuration("SITEOPS_COMMAND_TIMEOUT", 15*time.Minute)
	shutdownGrace := getEnvDuration("SITEOPS_SHUTDOWN_GRACE", 10*time.Second)
	logger.Debug("running aws command", "args", strings.Join(args, " "))
	return runner.Run(ctx, logger, runner.Command{
		Name:   "aws",
		Args:   args,
		Env:    os.Environ(),
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}, runner.Options{
		Timeout:       commandTimeout,
		ShutdownGrace: shutdownGrace,
		MaxAttempts:   1,
	})
}

func loadConfig(cfgPath, inventoryPath, deploymentName string) (config.Config, error) {
	if inventoryPath != "" && cfgPath != "config/site.local.yaml" {
		return config.Config{}, fmt.Errorf("cannot use both -config and -inventory")
	}
	if deploymentName != "" && inventoryPath == "" {
		return config.Config{}, fmt.Errorf("-deployment requires -inventory")
	}
	if inventoryPath != "" {
		return config.LoadFromInventory(inventoryPath, deploymentName)
	}
	return config.Load(cfgPath)
}

func printUsage(appName string) {
	fmt.Printf(`%s

Usage:
  %s -config <file> <command> [extra args]

Commands:
  deploy             Build and publish to S3 + CloudFront (production)
  deploy-local       Start local dev server with watch and rebuild on change
  build              Build static website output
  build-inline       Build with inlined assets
  publish            alias of deploy
  watch              Bootstrap compiler image if needed, then start docker-compose watch + preview
  serve              Serve website locally (one-shot build + serve, no watch)
  validate-site-data Validate site data against the site contract
  validate-assets    Validate local CSS/JS assets against rendered pages
  clean              Remove output directory
  upload-build       Upload built output to the builds staging bucket (--sha required)
  promote            Promote a staged build to the live bucket (--sha required)
  list-builds        List available builds in the staging bucket
  compose-up         compose up --build
  compose-down       compose down
  compose-logs       compose logs -f
  compose-rebuild    compose up --build --force-recreate
  docker-up          alias of compose-up
  docker-down        alias of compose-down
  docker-logs        alias of compose-logs
  docker-rebuild     alias of compose-rebuild
`, appName, appName)
}
