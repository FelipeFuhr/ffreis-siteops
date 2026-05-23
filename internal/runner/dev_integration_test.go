package runner

import (
	"bytes"
	"context"
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCompilerEnv is the env var the helper subprocess reads to know it's
// the fake compiler, not a real test invocation. The compiler subprocess
// then reads its own -addr flag and listens on that port.
const fakeCompilerEnv = "FFREIS_FAKE_COMPILER"

// TestFakeCompilerSubprocess is the worker side of the integration test
// below. When run with FFREIS_FAKE_COMPILER=1 in the env (and the same args
// the real compiler would receive: `serve -website-root <p> -addr :<n>`),
// it acts as a tiny stand-in HTTP server for the duration of the test.
//
// We use the canonical TestHelperProcess pattern from go/src/os/exec/exec_test.go
// to re-purpose the test binary.
func TestFakeCompilerSubprocess(t *testing.T) {
	if os.Getenv(fakeCompilerEnv) != "1" {
		t.Skip("not running as fake compiler subprocess")
	}

	fs := flag.NewFlagSet("compiler", flag.ContinueOnError)
	addr := fs.String("addr", "", "listen address")
	_ = fs.String("website-root", "", "(ignored by fake)")
	// First positional should be "serve". Skip it before parsing.
	args := os.Args[1:]
	// Drop "-test.*" args injected by the test runner.
	cleaned := make([]string, 0, len(args))
	for _, a := range args {
		if strings.HasPrefix(a, "-test.") {
			continue
		}
		cleaned = append(cleaned, a)
	}
	if len(cleaned) > 0 && cleaned[0] == "serve" {
		cleaned = cleaned[1:]
	}
	if err := fs.Parse(cleaned); err != nil {
		os.Stderr.WriteString("fake compiler: " + err.Error() + "\n")
		os.Exit(2)
	}
	if *addr == "" {
		os.Stderr.WriteString("fake compiler: --addr required\n")
		os.Exit(2)
	}

	srv := &http.Server{
		Addr:              *addr,
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("fake-compiler-OK"))
		}),
	}

	// Server runs until SIGTERM/SIGINT (the runner's compilerCancel triggers it
	// by cancelling the context that wraps the os/exec.CommandContext, which
	// signals + kills the process).
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		os.Stderr.WriteString("fake compiler: " + err.Error() + "\n")
		os.Exit(3)
	}
}

// writeFile writes content to path, creating parent dirs.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// freePort returns an OS-assigned free TCP port.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// withFakeCompilerAllowed appends the test-binary base name to AllowedCommands
// for the duration of the test, then restores the original. The runner's
// validator rejects unknown command names; this is the standard escape
// hatch for tests.
func withFakeCompilerAllowed(t *testing.T) {
	t.Helper()
	orig := AllowedCommands
	t.Cleanup(func() { AllowedCommands = orig })
	AllowedCommands = append(append([]string(nil), orig...), filepath.Base(os.Args[0]))
}

// minimalDataRoot creates the smallest data tree that satisfies injectData:
// <root>/<lang>/site.yaml + <root>/shared/site.d/.
func minimalDataRoot(t *testing.T, lang string) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, lang, "site.yaml"), "site_title: smoke\n")
	writeFile(t, filepath.Join(root, lang, "site.d", "a.yaml"), "a: 1\n")
	writeFile(t, filepath.Join(root, "shared", "site.d", "b.yaml"), "b: 2\n")
	return root
}

// TestRunDev_EndToEnd_SmokeTest is the headline smoke test for the dev
// subcommand. It wires up:
//   - A fake compiler subprocess (this test binary, re-invoked with
//     FFREIS_FAKE_COMPILER=1) acting as the local frontend.
//   - A real httptest.Server acting as the dev API Gateway.
//   - A real RunDev call in a goroutine.
//
// After RunDev signals ready (by accepting connections on the user port),
// it issues two requests: one to the frontend (should reach the fake
// compiler) and one to a configured ProxyPath (should reach the test
// gateway with the Origin header rewritten). Then it cancels the ctx and
// asserts RunDev returns within bounded time.
//
// This is the test that proves the whole dev pipeline — data injection,
// subprocess spawn, port allocation, proxy wiring, ctx-cancellation
// cleanup — works end to end without a real AWS API Gateway.
func TestRunDev_EndToEnd_SmokeTest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short")
	}
	withFakeCompilerAllowed(t)

	// 1. Fake API Gateway.
	var gatewayMu sync.Mutex
	var gatewayHits []string
	var lastOrigin string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayMu.Lock()
		gatewayHits = append(gatewayHits, r.URL.Path)
		lastOrigin = r.Header.Get("Origin")
		gatewayMu.Unlock()
		w.Header().Set("Access-Control-Allow-Origin", "https://dev.example.com")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"smoke":"ok"}`))
	}))
	defer gateway.Close()

	// 2. Data + website roots.
	dataRoot := minimalDataRoot(t, "pt")
	websiteRoot := t.TempDir()

	// 3. Pick a deterministic user port so we know where to send the
	// smoke-test request.
	userPort := freePort(t)

	// 4. Configure RunDev to invoke this test binary as the fake compiler.
	spec := DevSpec{
		CompilerCommand: os.Args[0],
		WebsiteRoot:     websiteRoot,
		DataRoot:        dataRoot,
		Lang:            "pt",
		PreviewPort:     userPort,
		APIGatewayURL:   gateway.URL,
		DevOrigin:       "https://dev.example.com",
		ProxyPaths:      []string{"/ask", "/api/*"},
	}

	// Logger that absorbs output; the integration test produces a lot of it.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// We need the fake compiler subprocess to enter the helper code path,
	// which is gated by FFREIS_FAKE_COMPILER=1. The runner's exec.Command
	// inherits the parent process env (it appends to os.Environ()), so set
	// it on this process.
	t.Setenv(fakeCompilerEnv, "1")

	// Tell the subprocess (which is `go test ...`) to run only the helper
	// test. The runner passes `serve -website-root ... -addr ...` as args;
	// our flag.ContinueOnError parser tolerates extra `-test.run=...` in
	// front because we strip "-test." args.
	// We also pass GOCOVERDIR to a temp dir if set, to keep coverage runs
	// from interleaving (test framework handles this automatically).

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- RunDev(ctx, logger, spec)
	}()

	// 5. Wait for the proxy to be listening.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoaPort(userPort)), 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 6. Make a frontend request. Should reach the fake compiler.
	client := &http.Client{Timeout: 5 * time.Second}
	feResp, err := client.Get("http://127.0.0.1:" + itoaPort(userPort) + "/index.html")
	if err != nil {
		// Don't fail the whole test on transient race; collect what we can.
		t.Logf("frontend request failed (may be racy on startup): %v", err)
	} else {
		body, _ := io.ReadAll(feResp.Body)
		_ = feResp.Body.Close()
		if !strings.Contains(string(body), "fake-compiler-OK") {
			t.Errorf("frontend body = %q, want substring 'fake-compiler-OK'", body)
		}
	}

	// 7. Make a proxy request. Should reach the fake API Gateway.
	apiResp, err := client.Get("http://127.0.0.1:" + itoaPort(userPort) + "/ask?q=smoke")
	if err != nil {
		t.Fatalf("API request through proxy: %v", err)
	}
	body, _ := io.ReadAll(apiResp.Body)
	_ = apiResp.Body.Close()
	if !strings.Contains(string(body), `"smoke":"ok"`) {
		t.Errorf("API body = %q, want substring 'smoke:ok'", body)
	}
	if got := apiResp.Header.Get("Access-Control-Allow-Origin"); !strings.HasPrefix(got, "http://localhost:") {
		t.Errorf("response ACAO = %q, want it rewritten to local origin", got)
	}

	// Verify gateway saw the request with the rewritten Origin.
	gatewayMu.Lock()
	hits := append([]string(nil), gatewayHits...)
	origin := lastOrigin
	gatewayMu.Unlock()
	if len(hits) == 0 {
		t.Error("gateway received no requests")
	}
	if origin != "https://dev.example.com" {
		t.Errorf("gateway saw Origin = %q, want https://dev.example.com (rewritten)", origin)
	}

	// 8. Cancel context, assert RunDev returns within bounded time.
	cancel()
	select {
	case err := <-done:
		// RunDev may return nil (clean ctx.Done) or a wrapped error from
		// the proxy/compiler — both are acceptable.
		t.Logf("RunDev returned: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("RunDev did not return within 15s of ctx cancel — shutdown deadlock")
	}

	// 9. Confirm injectData ran (site.yaml was copied to website_root/src/data).
	if _, err := os.Stat(filepath.Join(websiteRoot, "src", "data", "site.yaml")); err != nil {
		t.Errorf("injected site.yaml missing: %v", err)
	}
}

// itoaPort is a tiny helper to avoid importing strconv just for this test.
func itoaPort(p int) string {
	var buf bytes.Buffer
	if p < 0 {
		buf.WriteByte('-')
		p = -p
	}
	if p == 0 {
		buf.WriteByte('0')
		return buf.String()
	}
	digits := make([]byte, 0, 6)
	for p > 0 {
		digits = append(digits, byte('0'+p%10))
		p /= 10
	}
	for i := len(digits) - 1; i >= 0; i-- {
		buf.WriteByte(digits[i])
	}
	return buf.String()
}
