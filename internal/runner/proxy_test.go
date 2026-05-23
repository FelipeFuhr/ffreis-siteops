package runner

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeBackend records the last request it received and returns a canned
// response. Used to stand in for both the local compiler (frontend) and
// the remote API Gateway in DevProxy tests.
type fakeBackend struct {
	srv         *httptest.Server
	gotRequests []*http.Request
	status      int
	body        string
	respHeaders http.Header
}

func newFakeBackend(t *testing.T, status int, body string, respHeaders http.Header) *fakeBackend {
	t.Helper()
	b := &fakeBackend{status: status, body: body, respHeaders: respHeaders}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain body so we can examine it if needed.
		_, _ = io.Copy(io.Discard, r.Body)
		b.gotRequests = append(b.gotRequests, r.Clone(r.Context()))
		for k, vs := range b.respHeaders {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(b.status)
		_, _ = w.Write([]byte(b.body))
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func newSilentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestPathMatches_LiteralAndWildcard pins the matcher contract: literal
// patterns match exactly; trailing /* matches the prefix itself plus any
// child path.
func TestPathMatches_LiteralAndWildcard(t *testing.T) {
	cases := []struct {
		pattern, target string
		want            bool
	}{
		// Literal
		{"/ask", "/ask", true},
		{"/ask", "/asks", false},
		{"/ask", "/ask/", false},
		{"/ask", "/ask/foo", false},
		// Wildcard
		{"/api/*", "/api", true},
		{"/api/*", "/api/foo", true},
		{"/api/*", "/api/foo/bar", true},
		{"/api/*", "/apix", false},
		{"/api/*", "/", false},
		// Edge
		{"", "/anything", false},
		{"   ", "/anything", false},
	}
	for _, tc := range cases {
		got := pathMatches(tc.pattern, tc.target)
		if got != tc.want {
			t.Errorf("pathMatches(%q, %q) = %v, want %v", tc.pattern, tc.target, got, tc.want)
		}
	}
}

// TestDevProxy_FrontendRouteByDefault: requests whose path doesn't match any
// ProxyPath go to the local frontend (compiler serve).
func TestDevProxy_FrontendRouteByDefault(t *testing.T) {
	frontend := newFakeBackend(t, http.StatusOK, "<html>frontend</html>", nil)
	api := newFakeBackend(t, http.StatusOK, `{"from":"api"}`, nil)

	d := &DevProxy{
		FrontendURL:   frontend.srv.URL,
		APIGatewayURL: api.srv.URL,
		DevOrigin:     "https://example.com",
		LocalOrigin:   "http://localhost:8088",
		ProxyPaths:    []string{"/ask", "/api/*"},
		Logger:        newSilentLogger(),
	}
	h, err := d.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/some-page.html")
	if err != nil {
		t.Fatalf("GET frontend: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "frontend") {
		t.Errorf("expected frontend body, got %q", body)
	}
	if len(api.gotRequests) != 0 {
		t.Errorf("api gateway received unexpected request: %+v", api.gotRequests)
	}
}

// TestDevProxy_APIRouteForwardsAndRewritesOrigin: a request to a ProxyPath
// is forwarded to the API Gateway with the Origin header rewritten to
// DevOrigin (so the dev Lambda's CORS check passes).
func TestDevProxy_APIRouteForwardsAndRewritesOrigin(t *testing.T) {
	frontend := newFakeBackend(t, http.StatusOK, "frontend", nil)
	api := newFakeBackend(t, http.StatusOK, `{"ok":true}`, http.Header{
		"Access-Control-Allow-Origin": []string{"https://dev.example.com"},
		"Content-Type":                []string{"application/json"},
	})

	d := &DevProxy{
		FrontendURL:   frontend.srv.URL,
		APIGatewayURL: api.srv.URL,
		DevOrigin:     "https://dev.example.com",
		LocalOrigin:   "http://localhost:8088",
		ProxyPaths:    []string{"/ask", "/api/*"},
		Logger:        newSilentLogger(),
	}
	h, err := d.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/ask", strings.NewReader(`{"q":"hello"}`))
	req.Header.Set("Origin", "http://localhost:8088")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	// Frontend must NOT have been hit.
	if len(frontend.gotRequests) != 0 {
		t.Errorf("frontend received unexpected request: %+v", frontend.gotRequests)
	}

	// API must have been hit exactly once.
	if len(api.gotRequests) != 1 {
		t.Fatalf("api got %d requests, want 1", len(api.gotRequests))
	}

	// Origin must have been rewritten to DevOrigin.
	if got := api.gotRequests[0].Header.Get("Origin"); got != "https://dev.example.com" {
		t.Errorf("upstream Origin = %q, want https://dev.example.com", got)
	}
	if got := api.gotRequests[0].Header.Get("Referer"); got != "https://dev.example.com/" {
		t.Errorf("upstream Referer = %q, want https://dev.example.com/", got)
	}

	// Response ACAO must have been rewritten to LocalOrigin for the browser.
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:8088" {
		t.Errorf("response ACAO = %q, want http://localhost:8088", got)
	}
	// Credentials should be enabled when ACAO is rewritten.
	if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("response ACAC = %q, want true", got)
	}
}

// TestDevProxy_APIWildcardMatchesChildren: /api/* matches /api/foo and
// /api/foo/bar but not /apix.
func TestDevProxy_APIWildcardMatchesChildren(t *testing.T) {
	frontend := newFakeBackend(t, http.StatusOK, "frontend", nil)
	api := newFakeBackend(t, http.StatusOK, "api", nil)

	d := &DevProxy{
		FrontendURL:   frontend.srv.URL,
		APIGatewayURL: api.srv.URL,
		DevOrigin:     "https://example.com",
		LocalOrigin:   "http://localhost:8088",
		ProxyPaths:    []string{"/api/*"},
		Logger:        newSilentLogger(),
	}
	h, _ := d.Handler()
	srv := httptest.NewServer(h)
	defer srv.Close()

	cases := []struct {
		path       string
		wantTarget string
	}{
		{"/api", "api"},
		{"/api/foo", "api"},
		{"/api/foo/bar", "api"},
		{"/apix", "frontend"}, // does NOT match the wildcard
		{"/", "frontend"},
		{"/index.html", "frontend"},
	}
	for _, tc := range cases {
		resp, err := http.Get(srv.URL + tc.path)
		if err != nil {
			t.Errorf("GET %s: %v", tc.path, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if string(body) != tc.wantTarget {
			t.Errorf("path %s routed to %q, want %q", tc.path, body, tc.wantTarget)
		}
	}
}

// TestDevProxy_NoACAORewriteWhenUpstreamOmitsIt: if the API Gateway response
// has no Access-Control-Allow-Origin header, the proxy doesn't synthesize
// one. This matches CORS preflight semantics — the browser only requires
// ACAO when the request was a CORS request.
func TestDevProxy_NoACAORewriteWhenUpstreamOmitsIt(t *testing.T) {
	frontend := newFakeBackend(t, http.StatusOK, "frontend", nil)
	api := newFakeBackend(t, http.StatusOK, `{"ok":true}`, nil) // NO ACAO

	d := &DevProxy{
		FrontendURL:   frontend.srv.URL,
		APIGatewayURL: api.srv.URL,
		DevOrigin:     "https://dev.example.com",
		LocalOrigin:   "http://localhost:8088",
		ProxyPaths:    []string{"/ask"},
		Logger:        newSilentLogger(),
	}
	h, _ := d.Handler()
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ask")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("response ACAO = %q, want empty (upstream did not set it)", got)
	}
}

// TestDevProxy_HandlerRejectsBadFrontendURL surfaces malformed URLs as
// errors at Handler() build time, not at request time.
func TestDevProxy_HandlerRejectsBadFrontendURL(t *testing.T) {
	d := &DevProxy{
		FrontendURL:   "://not-a-url",
		APIGatewayURL: "http://api.example.com",
		Logger:        newSilentLogger(),
	}
	_, err := d.Handler()
	if err == nil {
		t.Fatal("expected error for malformed FrontendURL, got nil")
	}
	if !strings.Contains(err.Error(), "parsing frontend URL") {
		t.Errorf("err = %v, expected to mention 'parsing frontend URL'", err)
	}
}
