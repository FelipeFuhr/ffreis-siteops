SHELL := /bin/bash

CONFIG      ?= config/site.local.yaml
SITEOPS_BIN := .bin/siteops
SITEOPS     := $(SITEOPS_BIN) -config $(CONFIG)
CONTAINER_COMMAND ?= podman
WEBSITE_COMPILER_BIN ?= website-compiler

GOFMT ?= gofmt
GOLANGCI_LINT ?= golangci-lint
GITLEAKS ?= gitleaks
GOVULNCHECK ?= govulncheck
COVERAGE_MIN ?= 90

LEFTHOOK_VERSION ?= 1.7.10

MUTATION_PACKAGES ?= ./internal/...
MUTATION_THRESHOLD ?= 60
LEFTHOOK_DIR ?= $(CURDIR)/.bin
LEFTHOOK_BIN ?= $(LEFTHOOK_DIR)/lefthook

.PHONY: mutation-test help info siteops-build deploy deploy-local \
	build build-inline serve validate-site-data validate-assets clean \
	compose-up compose-down compose-logs compose-rebuild publish \
	docker-up docker-down docker-logs docker-rebuild \
	fmt-check lint test test-race coverage-gate smoke-check secrets-scan-staged quality-gates hook-generated-drift \
	lefthook-bootstrap lefthook-install lefthook-run lefthook

# ── Binary build ──────────────────────────────────────────────────────────────
# Rebuilds only when sources, go.mod, or go.sum change.
$(SITEOPS_BIN): go.mod go.sum $(shell find cmd internal -name '*.go' 2>/dev/null)
	@mkdir -p .bin
	go build -o $(SITEOPS_BIN) ./cmd/siteops

siteops-build: $(SITEOPS_BIN) ## (Re)compile the siteops binary to .bin/siteops

## mutation-test: run mutation testing with gremlins (slow — intended for CI/weekly)
mutation-test: ## Run mutation testing with gremlins (slow — CI only)
	@which gremlins >/dev/null 2>&1 || go install github.com/go-gremlins/gremlins/cmd/gremlins@latest
	gremlins unleash --threshold-efficacy $(MUTATION_THRESHOLD) $(MUTATION_PACKAGES)

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

publish: ## Alias of deploy
	$(SITEOPS) publish

serve: ## Serve website using YAML config
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
	go test ./...

test-race: ## Run tests with race detector
	go test -race ./...

coverage-gate: ## Run tests with coverage and fail if below COVERAGE_MIN
	@COVERAGE_MIN="$(COVERAGE_MIN)" ./scripts/hooks/check_coverage_gate.sh

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
	go run ./cmd/siteops -config "$$tmp_dir/smoke.yaml" build; \
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

lefthook-bootstrap: ## Download lefthook binary into ./.bin
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
