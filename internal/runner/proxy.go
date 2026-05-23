package runner

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// DevProxy fronts a locally-running compiler `serve` process and forwards
// configured API paths to a remote API Gateway. It rewrites the Origin header
// on outbound API requests so the dev Lambda's CORS_ALLOW_ORIGIN check passes
// for browsers running on localhost, and rewrites Access-Control-Allow-Origin
// on the response so the browser accepts it.
type DevProxy struct {
	FrontendURL   string
	APIGatewayURL string
	DevOrigin     string
	LocalOrigin   string
	ProxyPaths    []string
	Logger        *slog.Logger
}

// Handler returns the combined http.Handler.
func (d *DevProxy) Handler() (http.Handler, error) {
	frontendURL, err := url.Parse(d.FrontendURL)
	if err != nil {
		return nil, fmt.Errorf("parsing frontend URL %q: %w", d.FrontendURL, err)
	}
	apiURL, err := url.Parse(d.APIGatewayURL)
	if err != nil {
		return nil, fmt.Errorf("parsing api gateway URL %q: %w", d.APIGatewayURL, err)
	}

	frontendProxy := httputil.NewSingleHostReverseProxy(frontendURL)

	var apiProxy *httputil.ReverseProxy
	if strings.TrimSpace(d.APIGatewayURL) != "" {
		apiProxy = httputil.NewSingleHostReverseProxy(apiURL)
		baseDirector := apiProxy.Director
		apiProxy.Director = func(r *http.Request) {
			baseDirector(r)
			r.Host = apiURL.Host
			if d.DevOrigin != "" {
				r.Header.Set("Origin", d.DevOrigin)
				r.Header.Set("Referer", d.DevOrigin+"/")
			}
			d.Logger.Info(
				"proxying to api gateway",
				"method", r.Method,
				"path", r.URL.Path,
				"target_host", apiURL.Host,
				"origin", d.DevOrigin,
			)
		}
		apiProxy.ModifyResponse = func(resp *http.Response) error {
			if resp.Header.Get("Access-Control-Allow-Origin") != "" {
				resp.Header.Set("Access-Control-Allow-Origin", d.LocalOrigin)
				resp.Header.Set("Access-Control-Allow-Credentials", "true")
			}
			d.Logger.Info(
				"api gateway responded",
				"status", resp.StatusCode,
				"path", resp.Request.URL.Path,
			)
			return nil
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if apiProxy != nil && d.matchesAPI(r.URL.Path) {
			apiProxy.ServeHTTP(w, r)
			return
		}
		frontendProxy.ServeHTTP(w, r)
	}), nil
}

func (d *DevProxy) matchesAPI(p string) bool {
	for _, pat := range d.ProxyPaths {
		if pathMatches(pat, p) {
			return true
		}
	}
	return false
}

// pathMatches supports literal paths and trailing /* wildcard.
// "/ask" matches only "/ask"; "/api/*" matches "/api" and any "/api/..." child.
func pathMatches(pattern, target string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if strings.HasSuffix(pattern, "/*") {
		prefix := strings.TrimSuffix(pattern, "/*")
		return target == prefix || strings.HasPrefix(target, prefix+"/")
	}
	return pattern == target
}
