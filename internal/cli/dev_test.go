package cli

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"ffreis-siteops/internal/config"
	"ffreis-siteops/internal/runner"
)

// captureDevSpec swaps the dev-orchestrator port for one that records the spec
// it was handed and returns the supplied result.
func captureDevSpec(t *testing.T, result error) *runner.DevSpec {
	t.Helper()
	var got runner.DevSpec
	orig := runnerRunDev
	t.Cleanup(func() { runnerRunDev = orig })
	runnerRunDev = func(_ context.Context, _ *slog.Logger, spec runner.DevSpec) error {
		got = spec
		return result
	}
	return &got
}

func TestRunDev_MapsConfigOntoTheDevSpec(t *testing.T) {
	spec := captureDevSpec(t, nil)
	cfg := config.Config{
		CompilerCommand: "website-compiler",
		WebsiteRoot:     "/srv/site",
		DataRoot:        "/srv/data",
		DefaultLang:     "pt",
		PreviewPort:     8090,
		API: config.APIConfig{
			GatewayURL: "https://api.example.com",
			DevOrigin:  "https://dev.example.com",
			ProxyPaths: []string{"/ask", "/api/*"},
		},
	}

	if err := runDev(context.Background(), discardLogger(), cfg, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := runner.DevSpec{
		CompilerCommand: "website-compiler",
		WebsiteRoot:     "/srv/site",
		DataRoot:        "/srv/data",
		Lang:            "pt",
		PreviewPort:     8090,
		APIGatewayURL:   "https://api.example.com",
		DevOrigin:       "https://dev.example.com",
		ProxyPaths:      []string{"/ask", "/api/*"},
	}
	if !reflect.DeepEqual(*spec, want) {
		t.Errorf("dev spec = %+v, want %+v", *spec, want)
	}
}

func TestRunDev_LangFlagOverridesDefaultLang(t *testing.T) {
	spec := captureDevSpec(t, nil)
	cfg := config.Config{DefaultLang: "pt"}

	if err := runDev(context.Background(), discardLogger(), cfg, []string{"--lang", "en"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if spec.Lang != "en" {
		t.Errorf("lang = %q, want the flag value %q", spec.Lang, "en")
	}
}

func TestRunDev_WhitespaceLangFlagFallsBackToDefaultLang(t *testing.T) {
	spec := captureDevSpec(t, nil)
	cfg := config.Config{DefaultLang: " ja "}

	if err := runDev(context.Background(), discardLogger(), cfg, []string{"--lang", "   "}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if spec.Lang != "ja" {
		t.Errorf("lang = %q, want the trimmed default %q", spec.Lang, "ja")
	}
}

func TestRunDev_NoLangAnywhereIsAnError(t *testing.T) {
	captureDevSpec(t, nil)

	err := runDev(context.Background(), discardLogger(), config.Config{}, nil)

	if err == nil {
		t.Fatal("expected an error when neither --lang nor default_lang is set")
	}
	if !strings.Contains(err.Error(), "--lang is required") {
		t.Errorf("error = %v, want it to name the missing --lang flag", err)
	}
}

func TestRunDev_UnknownFlagIsAnError(t *testing.T) {
	captureDevSpec(t, nil)

	err := runDev(context.Background(), discardLogger(), config.Config{DefaultLang: "pt"}, []string{"--bogus"})

	if err == nil {
		t.Fatal("expected a parse error for an unknown flag")
	}
}

func TestRunDev_OrchestratorErrorIsPropagated(t *testing.T) {
	wantErr := errors.New("compiler serve exited")
	captureDevSpec(t, wantErr)

	err := runDev(context.Background(), discardLogger(), config.Config{DefaultLang: "pt"}, nil)

	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want %v", err, wantErr)
	}
}
