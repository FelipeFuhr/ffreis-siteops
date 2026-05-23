package cli

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"strings"

	"ffreis-siteops/internal/config"
	"ffreis-siteops/internal/runner"
)

// runDev wires the `dev` subcommand: parses --lang, then delegates to
// runner.RunDev for orchestration.
func runDev(ctx context.Context, logger *slog.Logger, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("dev", flag.ContinueOnError)
	lang := fs.String("lang", "", "language under data_root to inject (e.g. pt, en, jp); falls back to default_lang")
	if err := fs.Parse(args); err != nil {
		return err
	}

	selectedLang := strings.TrimSpace(*lang)
	if selectedLang == "" {
		selectedLang = strings.TrimSpace(cfg.DefaultLang)
	}
	if selectedLang == "" {
		return fmt.Errorf("--lang is required (or set default_lang in the config)")
	}

	spec := runner.DevSpec{
		CompilerCommand: cfg.CompilerCommand,
		WebsiteRoot:     cfg.WebsiteRoot,
		DataRoot:        cfg.DataRoot,
		Lang:            selectedLang,
		PreviewPort:     cfg.PreviewPort,
		APIGatewayURL:   cfg.API.GatewayURL,
		DevOrigin:       cfg.API.DevOrigin,
		ProxyPaths:      cfg.API.ProxyPaths,
	}
	return runner.RunDev(ctx, logger, spec)
}
