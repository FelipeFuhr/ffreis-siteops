package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const siteDDirName = "site.d"

// DevSpec configures a single-site local dev run.
type DevSpec struct {
	CompilerCommand string
	WebsiteRoot     string
	DataRoot        string
	Lang            string
	PreviewPort     int

	APIGatewayURL string
	DevOrigin     string
	ProxyPaths    []string
}

// RunDev injects data, spawns the compiler `serve` subprocess on an internal
// port, and runs a path-routing reverse proxy on the user-facing port that
// forwards API paths to the dev API Gateway. Blocks until ctx is canceled or
// either subprocess exits.
func RunDev(ctx context.Context, logger *slog.Logger, spec DevSpec) error {
	if err := injectData(logger, spec.DataRoot, spec.Lang, spec.WebsiteRoot); err != nil {
		return fmt.Errorf("injecting data: %w", err)
	}

	userPort, err := allocatePort(spec.PreviewPort)
	if err != nil {
		return fmt.Errorf("allocating user port: %w", err)
	}
	internalPort, err := allocatePort(userPort + 1)
	if err != nil {
		return fmt.Errorf("allocating internal port: %w", err)
	}
	logger.Info("dev ports allocated", "user_port", userPort, "compiler_port", internalPort)

	compilerCtx, compilerCancel := context.WithCancel(ctx)
	defer compilerCancel()

	compilerErr := make(chan error, 1)
	go func() {
		compilerErr <- Run(compilerCtx, logger, Command{
			Name: spec.CompilerCommand,
			Args: []string{
				"serve",
				"-website-root", spec.WebsiteRoot,
				"-addr", fmt.Sprintf(":%d", internalPort),
			},
			Env:    os.Environ(),
			Stdin:  nil,
			Stdout: os.Stdout,
			Stderr: os.Stderr,
		}, Options{
			Timeout:       0,
			ShutdownGrace: 5 * time.Second,
			MaxAttempts:   1,
		})
	}()

	if err := waitForPort(ctx, "127.0.0.1", internalPort, 15*time.Second); err != nil {
		compilerCancel()
		<-compilerErr
		return fmt.Errorf("compiler serve failed to start on :%d: %w", internalPort, err)
	}

	localOrigin := fmt.Sprintf("http://localhost:%d", userPort)
	proxy := &DevProxy{
		FrontendURL:   fmt.Sprintf("http://127.0.0.1:%d", internalPort),
		APIGatewayURL: spec.APIGatewayURL,
		DevOrigin:     spec.DevOrigin,
		LocalOrigin:   localOrigin,
		ProxyPaths:    spec.ProxyPaths,
		Logger:        logger,
	}
	handler, err := proxy.Handler()
	if err != nil {
		compilerCancel()
		<-compilerErr
		return fmt.Errorf("building dev proxy: %w", err)
	}

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", userPort),
		Handler:           handler,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
	}

	proxyErr := make(chan error, 1)
	go func() {
		logger.Info(
			"dev mode ready",
			"url", localOrigin,
			"lang", spec.Lang,
			"api_gateway", spec.APIGatewayURL,
			"proxy_paths", strings.Join(spec.ProxyPaths, ","),
		)
		proxyErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-compilerErr:
		// Bounded shutdown: the ctx.Done branch below uses 10s; mirror that
		// here so a hung HTTP connection on the proxy can't trap the caller.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = srv.Shutdown(shutdownCtx)
		cancel()
		<-proxyErr
		if err == nil {
			return fmt.Errorf("compiler serve exited unexpectedly")
		}
		return fmt.Errorf("compiler serve exited: %w", err)
	case err := <-proxyErr:
		compilerCancel()
		<-compilerErr
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("dev proxy server: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutdown signal received, stopping dev mode")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		<-proxyErr
		compilerCancel()
		<-compilerErr
		return nil
	}
}

// injectData mirrors the manual data-staging procedure used by local deploys:
// copies <data_root>/<lang>/site.yaml plus <data_root>/<lang>/site.d/*.yaml and
// <data_root>/shared/site.d/*.yaml into <website_root>/src/data/. The compiler's
// site-data loader does not recurse into <data_root>/<lang>/site.d/ when pointed
// at a top-level dir, so we materialize the merge ahead of serving.
func injectData(logger *slog.Logger, dataRoot, lang, websiteRoot string) error {
	if dataRoot == "" {
		return fmt.Errorf("data_root is required")
	}
	if lang == "" {
		return fmt.Errorf("lang is required")
	}
	if websiteRoot == "" {
		return fmt.Errorf("website_root is required")
	}
	dataDir := filepath.Join(websiteRoot, "src", "data")
	destSiteD := filepath.Join(dataDir, siteDDirName)
	if err := os.MkdirAll(destSiteD, 0o755); err != nil {
		return fmt.Errorf("creating data dir: %w", err)
	}

	srcSiteYAML := filepath.Join(dataRoot, lang, "site.yaml")
	if _, err := os.Stat(srcSiteYAML); err != nil {
		return fmt.Errorf("locating %s: %w", srcSiteYAML, err)
	}
	if err := copyFile(srcSiteYAML, filepath.Join(dataDir, "site.yaml")); err != nil {
		return fmt.Errorf("copying site.yaml: %w", err)
	}

	if entries, err := os.ReadDir(destSiteD); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
				_ = os.Remove(filepath.Join(destSiteD, e.Name()))
			}
		}
	}

	if err := copyYAMLDir(filepath.Join(dataRoot, lang, siteDDirName), destSiteD); err != nil {
		return fmt.Errorf("copying per-lang site.d: %w", err)
	}
	if err := copyYAMLDir(filepath.Join(dataRoot, "shared", siteDDirName), destSiteD); err != nil {
		return fmt.Errorf("copying shared site.d: %w", err)
	}

	logger.Info("data injected", "data_root", dataRoot, "lang", lang, "data_dir", dataDir)
	return nil
}

func copyYAMLDir(srcDir, destDir string) error {
	info, err := os.Stat(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		if err := copyFile(filepath.Join(srcDir, e.Name()), filepath.Join(destDir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

// allocatePort tries `start`, then the next 49 ports, returning the first one
// that binds. start <= 0 means "use 8088".
func allocatePort(start int) (int, error) {
	if start <= 0 {
		start = 8088
	}
	for port := start; port < start+50; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err == nil {
			_ = ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free port in [%d, %d)", start, start+50)
}

func waitForPort(ctx context.Context, host string, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for %s", addr)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
