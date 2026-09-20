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
KUSTOMIZE_VERSION     := v5.6.0
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
	go install sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION)
	go install github.com/yannh/kubeconform/cmd/kubeconform@$(KUBECONFORM_VERSION)
	go install golang.stackrox.io/kube-linter/cmd/kube-linter@$(KUBE_LINTER_VERSION)
	go install github.com/google/osv-scanner/v2/cmd/osv-scanner@$(OSV_SCANNER_VERSION)

.PHONY: web-install
web-install: ## Install web dependencies
	cd web && npm ci

##@ Container images
.PHONY: images test-e2e-stack
images: ## Build the Core, web and Matrix bot images (tag :dev); needs Docker
	docker build -f deploy/docker/Dockerfile.core -t notekeeper/core:dev .
	docker build -f deploy/docker/Dockerfile.web -t notekeeper/web:dev .
	docker build -f deploy/docker/Dockerfile.bot -t notekeeper/matrix-bot:dev .

test-e2e-stack: ## Browser tests against the built images behind nginx (proxy routes, CSP, share host); needs Docker
	e2e/run-stack.sh

##@ Performance
.PHONY: test-perf
test-perf: ## Latency at the documented size: 50 000 notes for one user (NFR-P1..P3); needs Docker
	cd core && NK_PERF_NOTES=$${NK_PERF_NOTES:-50000} go test -tags "integration perf" ./internal/server -run PerformanceAtTheDocumentedSize -v -timeout 15m

##@ Build and generate
.PHONY: generate
generate: ## Regenerate code from the OpenAPI specs and SQL queries (Go server, bot client, sqlc, TypeScript types)
	cd core && sqlc generate
	oapi-codegen -config api/gen-config/userapi.yaml api/user.yaml
	oapi-codegen -config api/gen-config/botapi.yaml api/bot.yaml
	oapi-codegen -config api/gen-config/publicapi.yaml api/public.yaml
	oapi-codegen -config api/gen-config/botclient.yaml api/bot.yaml
	cd web && npx --no-install openapi-typescript ../api/user.yaml -o src/api/user.schema.ts
	cd web && npx --no-install openapi-typescript ../api/public.yaml -o src/api/public.schema.ts

.PHONY: generate-check
generate-check: generate ## Fail if regenerating changes committed files
	git diff --exit-code -- core/internal/gen core/internal/store/dbq bots/sdk/botclient web/src/api

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
test-e2e: ## Browser end-to-end tests: real Core and PostgreSQL, the built web app, Playwright (Docker required)
	cd e2e && npm ci --silent && npx playwright install chromium
	e2e/run.sh

##@ Quality
.PHONY: lint
lint: check-libolm ## golangci-lint, ESLint, manifest checks
	@for m in $(GO_MODULES); do (cd $$m && golangci-lint run ./...) || exit 1; done
	cd web && npm run lint
	kustomize build deploy/k8s/overlays/example > $(BIN)/k8s.rendered.yaml
	kube-linter lint $(BIN)/k8s.rendered.yaml
	kubeconform -strict -ignore-missing-schemas -summary $(BIN)/k8s.rendered.yaml

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
# Local development values. These are NOT secrets: they only work against the throwaway
# development database started by `make up`.
DEV_ENV := NK_DATABASE_URL=postgres://nk_app:nk_app_dev@localhost:5432/notekeeper?sslmode=disable \
  NK_TOKEN_KEYS=dev:ZGV2LW9ubHkta2V5LWRldi1vbmx5LWtleS1kZXYtb25seS1rZXk= \
  NK_APP_URL=http://localhost:5173 NK_LOG_LEVEL=debug NK_ARGON2_MEMORY_KIB=8192

.PHONY: dev-core
dev-core: up ## Run Core against the development database (migrates first)
	cd core && NK_MIGRATE_DATABASE_URL=postgres://nk_migrate:nk_migrate_dev@localhost:5432/notekeeper?sslmode=disable $(DEV_ENV) go run ./cmd/core migrate && $(DEV_ENV) go run ./cmd/core serve

.PHONY: dev-admin
dev-admin: ## Run an operator command against the development database: make dev-admin ARGS="bootstrap alice"
	cd core && $(DEV_ENV) go run ./cmd/core admin $(ARGS)

.PHONY: dev-web
dev-web: ## Run the web app with hot reload (proxies /api to Core on :8080)
	cd web && npm run dev

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
