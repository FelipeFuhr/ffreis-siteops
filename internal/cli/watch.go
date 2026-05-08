package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ffreis-siteops/internal/config"
	"ffreis-siteops/internal/runner"
)

const (
	compilerGitHubURL  = "https://github.com/FelipeFuhr/ffreis-website-compiler.git"
	compilerCloneDir   = "/tmp/ffreis-website-compiler"
	defaultPreviewPort = 8088
	portSearchRange    = 10
)

func runWatch(ctx context.Context, logger *slog.Logger, cfg config.Config) error {
	if err := bootstrapCompilerImage(ctx, logger, cfg); err != nil {
		return err
	}

	cfg = resolveWorkspaceRoot(cfg)

	port := pickFreePort(previewStartPort(cfg))
	cfg = injectPort(cfg, port)
	logger.Info("preview will serve", "addr", fmt.Sprintf("http://localhost:%d", port))

	return runCompose(ctx, logger, cfg, "up", "--build")
}

// resolveWorkspaceRoot converts WORKSPACE_ROOT to an absolute path so that
// podman/docker compose resolves volume mounts correctly regardless of the
// docker-compose.yml file location.
func resolveWorkspaceRoot(cfg config.Config) config.Config {
	wsRoot := strings.TrimSpace(cfg.ComposeEnv["WORKSPACE_ROOT"])
	if wsRoot == "" {
		return cfg
	}
	abs, err := filepath.Abs(wsRoot)
	if err != nil {
		return cfg
	}
	env := make(map[string]string, len(cfg.ComposeEnv))
	for k, v := range cfg.ComposeEnv {
		env[k] = v
	}
	env["WORKSPACE_ROOT"] = abs
	cfg.ComposeEnv = env
	return cfg
}

func bootstrapCompilerImage(ctx context.Context, logger *slog.Logger, cfg config.Config) error {
	imageName := deriveCompilerImageName(cfg)

	logger.Info("checking compiler image", "image", imageName)
	if imageExistsLocally(ctx, logger, cfg, imageName) {
		logger.Info("compiler image found", "image", imageName)
		return nil
	}

	logger.Info("compiler image not found, building...", "image", imageName)

	src, err := ensureCompilerSrc(ctx, logger, cfg)
	if err != nil {
		return fmt.Errorf("obtaining compiler source: %w", err)
	}

	return buildCompilerImage(ctx, logger, cfg, imageName, src)
}

// deriveCompilerImageName returns the full image name (e.g. "ffreis/website-compiler-cli:local")
// by reading WEBSITE_COMPILER_IMAGE from compose_env, or constructing it from
// PREFIX / COMPILER_IMAGE_NAME : IMAGE_TAG — matching the logic in compose.sh.
func deriveCompilerImageName(cfg config.Config) string {
	if img := strings.TrimSpace(cfg.ComposeEnv["WEBSITE_COMPILER_IMAGE"]); img != "" {
		return img
	}

	imageRoot := strings.TrimSpace(cfg.ComposeEnv["IMAGE_ROOT"])
	if imageRoot == "" {
		imageRoot = strings.TrimSpace(cfg.ComposeEnv["IMAGE_PREFIX"])
	}
	if imageRoot == "" {
		imageRoot = strings.TrimSpace(cfg.ComposeEnv["PREFIX"])
	}

	compilerImageName := strings.TrimSpace(cfg.ComposeEnv["COMPILER_IMAGE_NAME"])
	if compilerImageName == "" {
		compilerImageName = "website-compiler-cli"
	}

	imageTag := strings.TrimSpace(cfg.ComposeEnv["IMAGE_TAG"])
	if imageTag == "" {
		imageTag = "local"
	}

	if imageRoot == "" {
		return compilerImageName + ":" + imageTag
	}
	return imageRoot + "/" + compilerImageName + ":" + imageTag
}

// imageExistsLocally returns true if the image is present in the local Docker image store.
// Output is discarded — this is a silent existence check with no retries.
func imageExistsLocally(ctx context.Context, logger *slog.Logger, cfg config.Config, imageName string) bool {
	err := runner.Run(ctx, logger, runner.Command{
		Name:   "docker",
		Args:   []string{"image", "inspect", "--format", "{{.Id}}", imageName},
		Env:    buildCompilerEnv(cfg),
		Stdin:  nil,
		Stdout: io.Discard,
		Stderr: io.Discard,
	}, runner.Options{
		Timeout:       30 * time.Second,
		ShutdownGrace: 5 * time.Second,
		MaxAttempts:   1,
	})
	return err == nil
}

// ensureCompilerSrc returns the compiler source directory, cloning from GitHub
// if the configured path is absent or no path is configured.
func ensureCompilerSrc(ctx context.Context, logger *slog.Logger, cfg config.Config) (string, error) {
	src := cfg.CompilerSrc
	if src != "" {
		if _, err := os.Stat(src); err == nil {
			logger.Info("using local compiler source", "path", src)
			return src, nil
		}
		logger.Info("compiler_src not found locally, will clone from GitHub", "configured_path", src)
	}
	return cloneCompilerFromGitHub(ctx, logger, cfg)
}

func cloneCompilerFromGitHub(ctx context.Context, logger *slog.Logger, cfg config.Config) (string, error) {
	if _, err := os.Stat(compilerCloneDir); err == nil {
		logger.Info("using cached GitHub clone", "path", compilerCloneDir)
		return compilerCloneDir, nil
	}

	logger.Info("cloning compiler from GitHub", "url", compilerGitHubURL, "dest", compilerCloneDir)
	if err := runner.Run(ctx, logger, runner.Command{
		Name:   "git",
		Args:   []string{"clone", "--depth=1", compilerGitHubURL, compilerCloneDir},
		Env:    buildCompilerEnv(cfg),
		Stdin:  nil,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}, runner.Options{
		Timeout:       5 * time.Minute,
		ShutdownGrace: 10 * time.Second,
		MaxAttempts:   1,
	}); err != nil {
		return "", fmt.Errorf("cloning compiler from GitHub: %w", err)
	}
	return compilerCloneDir, nil
}

func buildCompilerImage(ctx context.Context, logger *slog.Logger, cfg config.Config, imageName, src string) error {
	absSrc, err := filepath.Abs(src)
	if err != nil {
		return fmt.Errorf("resolving compiler source path: %w", err)
	}
	dockerfile := filepath.Join(absSrc, "containers", "Dockerfile.cli")

	// Use fully-qualified image names for podman compatibility (no short-name resolution).
	builderImage := cfg.ComposeEnv["COMPILER_BUILDER_IMAGE"]
	if builderImage == "" {
		builderImage = "docker.io/library/golang:1.25.8-bookworm"
	}
	runtimeImage := cfg.ComposeEnv["COMPILER_RUNTIME_IMAGE"]
	if runtimeImage == "" {
		runtimeImage = "docker.io/library/debian:bookworm-slim"
	}

	logger.Info("building compiler image", "image", imageName, "src", absSrc)
	return runner.Run(ctx, logger, runner.Command{
		Name: "docker",
		Args: []string{
			"build", "--no-cache", "-f", dockerfile,
			"--build-arg", "BUILDER_IMAGE=" + builderImage,
			"--build-arg", "RUNTIME_IMAGE=" + runtimeImage,
			"-t", imageName, absSrc,
		},
		Env:    buildCompilerEnv(cfg),
		Stdin:  nil,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}, runner.Options{
		Timeout:       getEnvDuration("SITEOPS_COMMAND_TIMEOUT", 15*time.Minute),
		ShutdownGrace: getEnvDuration("SITEOPS_SHUTDOWN_GRACE", 10*time.Second),
		MaxAttempts:   1,
	})
}

// pickFreePort returns the first available TCP port in [start, start+portSearchRange).
func pickFreePort(start int) int {
	for port := start; port < start+portSearchRange; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err == nil {
			_ = ln.Close()
			return port
		}
	}
	return start
}

func previewStartPort(cfg config.Config) int {
	if p := strings.TrimSpace(cfg.ComposeEnv["PREVIEW_PORT"]); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			return n
		}
	}
	return defaultPreviewPort
}

// injectPort returns a copy of cfg with PREVIEW_PORT set to the chosen port.
func injectPort(cfg config.Config, port int) config.Config {
	env := make(map[string]string, len(cfg.ComposeEnv)+1)
	for k, v := range cfg.ComposeEnv {
		env[k] = v
	}
	env["PREVIEW_PORT"] = strconv.Itoa(port)
	cfg.ComposeEnv = env
	return cfg
}
