# Notekeeper: entry point for build, test and checks. `make help` lists targets.
# Test tiers and their targets are fixed by NFR-Q5 (docs/requirements.md).

SHELL := /bin/bash
.DEFAULT_GOAL := help

BIN := $(CURDIR)/.bin
export PATH := $(BIN):$(PATH)
export GOBIN := $(BIN)

# Pinned tool versions (installed by `make tools` into .bin/)
SQLC_VERSION          := v1.31.1
GOOSE_VERSION         := v3.28.0
OAPI_CODEGEN_VERSION  := v2.8.0
GOVULNCHECK_VERSION   := v1.8.0
GOLANGCI_LINT_VERSION := v2.13.2
GITLEAKS_VERSION      := v8.30.1
KUBECONFORM_VERSION   := v0.8.0
KUBE_LINTER_VERSION   := v0.8.3
OSV_SCANNER_VERSION   := v2.6.0

GO_MODULES := core bots/sdk bots/matrix
COMPOSE    := docker compose -f deploy/docker-compose.yml

##@ Help
.PHONY: help
help: ## List targets
	@awk 'BEGIN {FS = ":.*## "} /^##@/ {printf "\n%s\n", substr($$0, 5)} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

##@ Setup
.PHONY: tools
tools: ## Install pinned developer tools into .bin/
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
	go install github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)
	go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	go install github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION)
	go install github.com/yannh/kubeconform/cmd/kubeconform@$(KUBECONFORM_VERSION)
	go install golang.stackrox.io/kube-linter/cmd/kube-linter@$(KUBE_LINTER_VERSION)
	go install github.com/google/osv-scanner/v2/cmd/osv-scanner@$(OSV_SCANNER_VERSION)

.PHONY: web-install
web-install: ## Install web dependencies
	cd web && npm ci

##@ Build and generate
.PHONY: generate
generate: ## Regenerate code from the OpenAPI specs (Go server, bot client, TypeScript types)
	oapi-codegen -config api/gen-config/userapi.yaml api/user.yaml
	oapi-codegen -config api/gen-config/botapi.yaml api/bot.yaml
	oapi-codegen -config api/gen-config/publicapi.yaml api/public.yaml
	oapi-codegen -config api/gen-config/botclient.yaml api/bot.yaml
	cd web && npx --no-install openapi-typescript ../api/user.yaml -o src/api/user.schema.ts
	cd web && npx --no-install openapi-typescript ../api/public.yaml -o src/api/public.schema.ts

.PHONY: generate-check
generate-check: generate ## Fail if regenerating changes committed files
	git diff --exit-code -- core/internal/gen bots/sdk/botclient web/src/api

.PHONY: build
build: check-libolm ## Build Core, the Matrix bot and the web app
	cd core && go build -o ../.bin/core ./cmd/core
	cd bots/matrix && go build -o ../../.bin/matrix-bot ./cmd/matrix-bot
	cd web && npm run build

##@ Tests (tiers)
.PHONY: test
test: test-core test-bot test-web ## Unit tests of every component (no Docker, no network)

.PHONY: test-core
test-core: ## Core unit tests
	cd core && go test -race ./...

.PHONY: check-libolm
check-libolm: ## Verify libolm (and its headers) is installed; the Matrix bot needs it
	@pkg-config --exists olm || { echo "libolm not found. Install it: Arch 'sudo pacman -S libolm', Debian/Ubuntu 'sudo apt install libolm-dev'"; exit 1; }

.PHONY: test-bot
test-bot: check-libolm ## Bot and bot SDK unit tests
	cd bots/sdk && go test -race ./...
	cd bots/matrix && go test -race ./...

.PHONY: test-web
test-web: ## Web unit tests and type check
	cd web && npm run typecheck && npm test

.PHONY: test-integration
test-integration: check-libolm ## Integration tests against real PostgreSQL and Synapse (Docker required)
	cd core && go test -race -tags integration -count=1 ./...
	cd bots/matrix && go test -race -tags integration -count=1 -timeout 10m ./...

.PHONY: test-e2e
test-e2e: ## Whole stack in docker-compose driven by Playwright (Docker required)
	@echo "test-e2e: no end-to-end tests yet (added from milestone M1 on)"

##@ Quality
.PHONY: lint
lint: check-libolm ## golangci-lint, ESLint, manifest checks
	@for m in $(GO_MODULES); do (cd $$m && golangci-lint run ./...) || exit 1; done
	cd web && npm run lint
	@if [ -d deploy/k8s ]; then kube-linter lint deploy/k8s && kubeconform -strict -ignore-missing-schemas -summary deploy/k8s; fi

.PHONY: vuln
vuln: ## Dependency vulnerability scans
	@for m in $(GO_MODULES); do (cd $$m && govulncheck ./...) || exit 1; done
	osv-scanner scan source --lockfile web/package-lock.json

.PHONY: secrets
secrets: ## Scan the repository for committed secrets
	gitleaks dir --no-banner --redact .

.PHONY: docs-check
docs-check: ## Regenerate the traceability table and fail on drift
	python3 docs/design/tools/traceability.py
	git diff --exit-code -- docs/design/09-traceability.md

.PHONY: check
check: generate-check lint test test-integration docs-check secrets vuln ## Everything CI runs

##@ Development stack
.PHONY: up
up: ## Start the development stack (PostgreSQL)
	$(COMPOSE) up -d --wait

.PHONY: down
down: ## Stop the development stack
	$(COMPOSE) down

.PHONY: db-reset
db-reset: ## Recreate the development database
	$(COMPOSE) down -v
	$(COMPOSE) up -d --wait
