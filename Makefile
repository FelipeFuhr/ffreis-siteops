SHELL := /bin/bash

CONFIG      ?= config/site.local.yaml
SITEOPS_BIN := .bin/siteops
SITEOPS     := $(SITEOPS_BIN) -config $(CONFIG)
CONTAINER_COMMAND ?= podman
# scan-fix(siteops:compiler-path): resolve to an absolute, symlink-resolved
# path when found on PATH.
#  1. siteops' own config resolver (internal/config/config.go resolvePath)
#     joins any non-absolute compiler_command against the config file's
#     directory (never does a PATH lookup) — smoke-check writes its
#     generated config into a fresh mktemp -d, so a bare "website-compiler"
#     default always resolved to "<tmpdir>/website-compiler" and failed
#     with "no such file".
#  2. website-compiler's own self-containerizing build derives its build
#     context (Dockerfile, go.mod/go.sum, source) from argv[0]'s directory,
#     not from the symlink target — invoking it via the `~/bin/website-
#     compiler -> .../ffreis-website-compiler/website-compiler` convenience
#     symlink (a common local dev-tool layout) resolves the build context to
#     ~/bin instead of the real repo, breaking `COPY go.mod go.sum ./`.
#     `readlink -f` follows the symlink so argv[0] lands in the real repo.
WEBSITE_COMPILER_BIN ?= $(shell command -v website-compiler >/dev/null 2>&1 && readlink -f "$$(command -v website-compiler)" || echo website-compiler)
# Matches website-compiler's own Makefile defaults (PREFIX=ffreis,
# COMPILER_IMAGE_NAME=website-compiler-cli, IMAGE_TAG=local). The compiled
# binary needs this in its env to construct a valid image tag when invoked
# standalone (not via its own `make`) — without it, IMAGE_ROOT resolves
# empty and it fails with "invalid reference format" on a leading "/".
WEBSITE_COMPILER_IMAGE ?= ffreis/website-compiler-cli:local

GOFMT ?= gofmt
GOLANGCI_LINT ?= golangci-lint
GITLEAKS ?= gitleaks
GOVULNCHECK ?= govulncheck
COVERAGE_MIN ?= 90

LEFTHOOK_VERSION ?= 1.7.10

MUTATION_PACKAGES ?= ./internal/...
MUTATION_THRESHOLD ?= 60
FUZZ_PACKAGES ?= ./internal/...
FUZZ_TIME     ?= 30s
FUZZ_TIME_EXT ?= 10m
LEFTHOOK_DIR ?= $(CURDIR)/.bin
LEFTHOOK_BIN ?= $(LEFTHOOK_DIR)/lefthook

.PHONY: mutation-test mutation help info siteops-build build-all deploy deploy-local dev \
	build build-inline watch serve validate-site-data validate-assets clean \
	compose-up compose-down compose-logs compose-rebuild publish \
	docker-up docker-down docker-logs docker-rebuild \
	fmt-check lint test test-race coverage-gate integration-coverage-gate smoke-check secrets-scan-staged quality-gates hook-generated-drift \
	fuzz fuzz-extended \
	lefthook-bootstrap lefthook-install lefthook-run lefthook

# ── Binary build ──────────────────────────────────────────────────────────────
# Rebuilds only when sources, go.mod, or go.sum change.
# scan-fix(go:buildvcs-worktree): -buildvcs=false — `go build`'s VCS auto-
# stamping runs `git status --porcelain` and fails with "error obtaining VCS
# status: exit status 128" when built from a linked git worktree (confirmed:
# same command succeeds in the canonical checkout, fails in every
# .worktrees/* checkout). Nothing here reads the embedded VCS build info.
$(SITEOPS_BIN): go.mod go.sum $(shell find cmd internal -name '*.go' 2>/dev/null)
	@mkdir -p .bin
	go build -buildvcs=false -o $(SITEOPS_BIN) ./cmd/siteops

siteops-build: $(SITEOPS_BIN) ## (Re)compile the siteops binary to .bin/siteops

## build-all: alias of siteops-build — single-binary CLI, no cross-compile matrix
## (required by the lefthook release tier's `cross-build` -> `make build-all`)
build-all: siteops-build

## mutation-test: run mutation testing with gremlins (slow — intended for CI/weekly)
mutation-test: ## Run mutation testing with gremlins (slow — CI only)
	@which gremlins >/dev/null 2>&1 || go install github.com/go-gremlins/gremlins/cmd/gremlins@latest
	gremlins unleash --threshold-efficacy $(MUTATION_THRESHOLD) $(MUTATION_PACKAGES)

# scan-fix(lefthook:release-tier): platform-standards' lefthook/go.yml release
# tier requires `make mutation` unconditionally (no graceful skip, since
# before this repo's pinned v1.6.0 — see ffreis-agents-runtime's AGENTS.md).
# Alias so the existing mutation-test target satisfies the contract.
mutation: mutation-test ## Alias for the lefthook release tier's `make mutation`

## fuzz: run all Fuzz* targets for FUZZ_TIME each (default 30s — quick CI smoke-test)
## (required by lefthook release tier; no-ops cleanly if no Fuzz* funcs exist)
fuzz:
	@for pkg in $$(go list $(FUZZ_PACKAGES)); do \
	  targets=$$(go test -list 'Fuzz.*' "$$pkg" 2>/dev/null | grep '^Fuzz' || true); \
	  for target in $$targets; do \
	    echo "→ $$pkg/$$target ($(FUZZ_TIME))"; \
	    go test -run='^$$' -fuzz="^$${target}$$" -fuzztime="$(FUZZ_TIME)" "$$pkg"; \
	  done; \
	done

## fuzz-extended: run all Fuzz* targets for FUZZ_TIME_EXT each (default 10m — deep weekly run)
fuzz-extended: FUZZ_TIME=$(FUZZ_TIME_EXT)
fuzz-extended: fuzz

help: ## Show siteops commands
	@awk 'BEGIN {FS = ":.*## "; printf "Usage: make <target> [CONFIG=path/to/config.yaml]\n\nTargets:\n"} /^[a-zA-Z0-9_.-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

info: ## Print effective variables
	@echo "CONFIG=$(CONFIG)"
	@echo "CONTAINER_COMMAND=$(CONTAINER_COMMAND)"
	@echo "COVERAGE_MIN=$(COVERAGE_MIN)"

build: ## Build website using YAML config
	$(SITEOPS) build

build-inline: ## Build with inlined assets using YAML config
	$(SITEOPS) build-inline

deploy: ## Build and publish to S3 + CloudFront (production)
	$(SITEOPS) deploy

deploy-local: ## Start local dev server — watch + rebuild on every change (requires compiler Docker image)
	$(SITEOPS) deploy-local

dev: ## Run dev env locally with /ask|/api/* proxied to dev API Gateway (CONFIG=config/<site>-dev.local.yaml [LANG=pt])
	$(SITEOPS) dev $(if $(LANG),--lang $(LANG))

publish: ## Alias of deploy
	$(SITEOPS) publish

watch: ## Build, serve, and rebuild on file changes (no Docker needed)
	$(SITEOPS) watch

serve: ## Serve website using YAML config (one-shot build + serve, no watch)
	$(SITEOPS) serve

validate-site-data: ## Validate configured site data against the site contract
	$(SITEOPS) validate-site-data

validate-assets: ## Validate configured local CSS/JS asset reachability
	$(SITEOPS) validate-assets

clean: ## Remove configured output
	$(SITEOPS) clean

compose-up: ## Start real-time compile + preview
	$(SITEOPS) compose-up

compose-down: ## Stop compose services
	$(SITEOPS) compose-down

compose-logs: ## Follow compose logs
	$(SITEOPS) compose-logs

compose-rebuild: ## Force rebuild/recreate compose services
	$(SITEOPS) compose-rebuild

docker-up: compose-up ## Backward-compatible alias

docker-down: compose-down ## Backward-compatible alias

docker-logs: compose-logs ## Backward-compatible alias

docker-rebuild: compose-rebuild ## Backward-compatible alias

fmt-check: ## Fail if Go files are not gofmt-formatted
	@./scripts/hooks/check_required_tools.sh $(GOFMT)
	@out="$$(find . -type f -name '*.go' -not -path './vendor/*' -not -path './.git/*' -print0 | xargs -0 -r $(GOFMT) -l)"; \
	if [ -n "$$out" ]; then \
		echo "Unformatted Go files:"; \
		echo "$$out"; \
		echo "Run: $(GOFMT) -w <files>"; \
		exit 1; \
	fi

lint: ## Run golangci-lint
	@./scripts/hooks/check_required_tools.sh $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run

test: ## Run unit tests
	go test -race -shuffle=on ./...

test-race: ## Run tests with race detector
	go test -race ./...

coverage-gate: ## Run tests with coverage and fail if below COVERAGE_MIN
	@COVERAGE_MIN="$(COVERAGE_MIN)" ./scripts/hooks/check_coverage_gate.sh

integration-coverage-gate: ## Run integration-tagged tests with coverage and fail if below COVERAGE_MIN (no-op if no //go:build integration files exist)
	@COVERAGE_MIN="$(COVERAGE_MIN)" ./scripts/hooks/check_integration_coverage_gate.sh

smoke-check: ## Build hello-world through siteops and assert output
	@set -euo pipefail; \
	./scripts/hooks/check_required_tools.sh go "$(WEBSITE_COMPILER_BIN)"; \
	tmp_dir="$$(mktemp -d)"; \
	trap 'rm -rf "$$tmp_dir"' EXIT; \
	mkdir -p "$$tmp_dir/site/src/templates/layout" "$$tmp_dir/site/src/templates/pages" "$$tmp_dir/site/src/assets/css" "$$tmp_dir/site/src/data"; \
	printf '%s\n' '{{ define "layout" }}<!doctype html><html><head><link rel="stylesheet" href="/css/main.css"></head><body>{{ template "content" . }}</body></html>{{ end }}' > "$$tmp_dir/site/src/templates/layout/base.gohtml"; \
	printf '%s\n' '{{ define "content" }}<h1>smoke</h1>{{ end }}' > "$$tmp_dir/site/src/templates/pages/index.gohtml"; \
	echo "body { margin: 0; }" > "$$tmp_dir/site/src/assets/css/main.css"; \
	: > "$$tmp_dir/site/src/data/site.contract.yaml"; \
	printf '%s\n' \
		'project_name: "smoke"' \
		"compiler_command: \"$(WEBSITE_COMPILER_BIN)\"" \
		"website_root: \"$$tmp_dir/site\"" \
		"out_dir: \"$$tmp_dir/dist\"" \
		'default_addr: ":18080"' \
		"container_command: \"$(CONTAINER_COMMAND)\"" \
		> "$$tmp_dir/smoke.yaml"; \
	WEBSITE_COMPILER_IMAGE="$(WEBSITE_COMPILER_IMAGE)" go run ./cmd/siteops -config "$$tmp_dir/smoke.yaml" build; \
	test -f "$$tmp_dir/dist/index.html"

secrets-scan-staged: ## Scan staged diff for secrets
	@./scripts/hooks/check_required_tools.sh $(GITLEAKS)
	$(GITLEAKS) protect --staged --redact

quality-gates: ## Run strict pre-push quality gates
	@./scripts/hooks/check_required_tools.sh $(GOVULNCHECK)
	$(MAKE) test
	$(MAKE) test-race
	$(MAKE) coverage-gate
	$(GOVULNCHECK) ./...
	$(MAKE) smoke-check

hook-generated-drift: ## Run generate target if present and fail on drift
	@set -euo pipefail; \
	if $(MAKE) -n generate >/dev/null 2>&1; then \
		$(MAKE) generate; \
		if ! git diff --quiet -- .; then \
			echo "Generated files are out of date. Run 'make generate' and commit updates."; \
			git status --short; \
			exit 1; \
		fi; \
	else \
		echo "No 'generate' target found; skipping generated drift check."; \
	fi


# v1.10.0
PLATFORM_STANDARDS_SHA := 273842219190739c6b462c21331b234271446b13
PLATFORM_STANDARDS_RAW := https://raw.githubusercontent.com/FelipeFuhr/ffreis-platform-standards
# NOTE: `VAR := sha  # comment` bakes the two spaces before `#` into the value (Make only
# strips from `#` onward, not the whitespace preceding it), which curl then rejects as
# "Malformed input to a URL function". Every reference below wraps in $(strip …) rather
# than reformatting the assignment, since expansion happens at use time.

HOOK_SCRIPTS := \
	check_merge_markers.sh \
	check_large_files.sh \
	check_binary_files.sh \
	check_commit_msg.sh \
	check_required_tools.sh

hook-scripts: ## Download bootstrap + hook scripts from ffreis-platform-standards
	@mkdir -p scripts/hooks
	@curl -fsSL "$(PLATFORM_STANDARDS_RAW)/$(strip $(PLATFORM_STANDARDS_SHA))/lefthook/bootstrap_lefthook.sh" \
		-o scripts/bootstrap_lefthook.sh && chmod +x scripts/bootstrap_lefthook.sh
	@for script in $(HOOK_SCRIPTS); do \
		curl -fsSL "$(PLATFORM_STANDARDS_RAW)/$(strip $(PLATFORM_STANDARDS_SHA))/lefthook/scripts/$$script" \
			-o "scripts/hooks/$$script" && chmod +x "scripts/hooks/$$script"; \
	done
	@echo "Hook scripts downloaded."

lefthook-bootstrap: hook-scripts ## Download lefthook binary into ./.bin
	LEFTHOOK_VERSION="$(LEFTHOOK_VERSION)" BIN_DIR="$(LEFTHOOK_DIR)" bash ./scripts/bootstrap_lefthook.sh

lefthook-install: lefthook-bootstrap ## Install git hooks if missing
	@if [ -x "$(LEFTHOOK_BIN)" ] && [ -x ".git/hooks/pre-commit" ] && [ -x ".git/hooks/pre-push" ] && [ -x ".git/hooks/commit-msg" ]; then \
		echo "lefthook hooks already installed"; \
		exit 0; \
	fi
	LEFTHOOK="$(LEFTHOOK_BIN)" "$(LEFTHOOK_BIN)" install

lefthook-run: lefthook-bootstrap ## Run hooks (pre-commit + commit-msg + pre-push)
	LEFTHOOK="$(LEFTHOOK_BIN)" "$(LEFTHOOK_BIN)" run pre-commit
	@tmp_msg="$$(mktemp)"; \
	echo "chore(hooks): validate commit-msg hook" > "$$tmp_msg"; \
	LEFTHOOK="$(LEFTHOOK_BIN)" "$(LEFTHOOK_BIN)" run commit-msg -- "$$tmp_msg"; \
	rm -f "$$tmp_msg"
	LEFTHOOK="$(LEFTHOOK_BIN)" "$(LEFTHOOK_BIN)" run pre-push

lefthook: lefthook-bootstrap lefthook-install lefthook-run ## Install hooks and run them

install-act: ## Download pinned act binary into .bin/
	@mkdir -p scripts
	@curl -fsSL "$(PLATFORM_STANDARDS_RAW)/$(strip $(PLATFORM_STANDARDS_SHA))/scripts/install_act.sh" \
		-o scripts/install_act.sh && chmod +x scripts/install_act.sh
	@bash ./scripts/install_act.sh

ci-local: ## Run workflows locally via act (GH Actions quota fallback). Args via ARGS=...
	@mkdir -p scripts
	@curl -fsSL "https://raw.githubusercontent.com/FelipeFuhr/ffreis-platform-ci-local/v1.0.0/scripts/run-ci-local.sh" \
		-o scripts/run-ci-local.sh && chmod +x scripts/run-ci-local.sh
	@CI_LOCAL_FINDINGS_REF=v1.0.0 PATH="$(CURDIR)/.bin:$(PATH)" bash ./scripts/run-ci-local.sh $(ARGS)
