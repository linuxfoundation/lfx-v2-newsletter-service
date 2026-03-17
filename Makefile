
# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT
# Makefile for LFX v2 Newsletter Service
.PHONY: all help deps apigen build run debug test test-verbose test-coverage auth-smoke deploy-quick fmt lint license-check check verify helm-install helm-install-local helm-templates helm-templates-local helm-uninstall clean docker-build docker-run

# Variables
# Binary/module
BINARY_NAME := newsletter-service
BINARY_PATH := bin/$(BINARY_NAME)
GO_MODULE := github.com/linuxfoundation/lfx-v2-newsletter-service
CMD_PATH := ./cmd/newsletter-api
DESIGN_MODULE := $(GO_MODULE)/design
GOA_VERSION := v3.16.2
GOA_INSTALL_TOOLCHAIN ?= go1.24.10
GO_FILES := $(shell find . -name '*.go' -not -path './gen/*' -not -path './vendor/*' -not -path './bin/*')

# Container
SERVICE_NAME := newsletter-service
DOCKER_IMAGE := lfx-v2-newsletter-service
VERSION ?= latest
DOCKER_TAG ?= $(VERSION)

# Helm/deploy
KIND_CLUSTER ?= newsletter-test
RELEASE_NAME ?= newsletter
NAMESPACE ?= lfx-v2-newsletter-service
CHART_DIR ?= charts/lfx-v2-newsletter-service
DEPLOYMENT_NAME ?= $(RELEASE_NAME)-lfx-v2-newsletter-service
DEPLOY_TAG ?= dev-$(shell date +%Y%m%d%H%M%S)
HELM_CHART_PATH ?= $(CHART_DIR)
HELM_RELEASE_NAME ?= $(RELEASE_NAME)
HELM_NAMESPACE ?= $(NAMESPACE)
HELM_LOCAL_VALUES_FILE ?= $(if $(wildcard $(HELM_CHART_PATH)/values.local.yaml),$(HELM_CHART_PATH)/values.local.yaml,$(HELM_CHART_PATH)/values.local.example.yaml)

# Build metadata
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.Version=$(BUILD_VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT)"

# Tests
TEST_FLAGS ?= -v -race
TEST_TIMEOUT ?= 5m
TEST_COVERAGE_PKGS := $(shell go list -f '{{if or (gt (len .TestGoFiles) 0) (gt (len .XTestGoFiles) 0)}}{{.ImportPath}}{{end}}' ./... | sed '/^$$/d' | grep -Ev '(^$(GO_MODULE)/design$$|/gen/)')
LINT_PATHS := ./cmd/... ./internal/... ./pkg/...

all: clean deps apigen fmt lint test build ## Full local pipeline

help: ## Display this help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

deps: ## Install dependencies
	@echo "Installing dependencies..."
	go mod download
	GOTOOLCHAIN=$(GOA_INSTALL_TOOLCHAIN) go install goa.design/goa/v3/cmd/goa@$(GOA_VERSION)
	@command -v golangci-lint >/dev/null 2>&1 || go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

apigen: ## Generate API code from Goa design
	@echo "Generating API code..."
	goa gen $(DESIGN_MODULE)

build: apigen ## Build the service
	@echo "Building $(SERVICE_NAME)..."
	go build -o $(BINARY_PATH) $(CMD_PATH)

run: build ## Run the service locally
	@echo "Running $(SERVICE_NAME)..."
	./$(BINARY_PATH)

debug: ## Run with debug logging
	@echo "Running $(SERVICE_NAME) with debug logging..."
	LOG_LEVEL=debug ./$(BINARY_PATH)

test: ## Run tests
	@echo "Running tests..."
	go test $(TEST_FLAGS) -timeout $(TEST_TIMEOUT) ./...

test-verbose: ## Run tests with verbose output
	@echo "Running tests (verbose)..."
	go test $(TEST_FLAGS) -timeout $(TEST_TIMEOUT) ./...

auth-smoke: ## Run end-to-end auth smoke test (strict JWT mode)
	@echo "Running auth smoke test..."
	bash scripts/auth_smoke.sh

deploy-quick: ## Quick local deploy to kind (build image, load, helm upgrade, rollout wait)
	@echo "Deploying $(DOCKER_IMAGE):$(DEPLOY_TAG) to kind cluster $(KIND_CLUSTER)..."
	@kind get clusters | grep -q '^$(KIND_CLUSTER)$$' || kind create cluster --name $(KIND_CLUSTER)
	@docker build -t $(DOCKER_IMAGE):$(DEPLOY_TAG) .
	@kind load docker-image $(DOCKER_IMAGE):$(DEPLOY_TAG) --name $(KIND_CLUSTER)
	@kubectl create namespace $(NAMESPACE) --dry-run=client -o yaml | kubectl apply -f - >/dev/null
	@set -a; source .env; set +a; \
		kubectl -n $(NAMESPACE) create secret generic ghost-api-credentials \
			--from-literal=api-key="$$GHOST_API_KEY" \
			--dry-run=client -o yaml | kubectl apply -f - >/dev/null
	@helm upgrade --install $(RELEASE_NAME) $(CHART_DIR) \
		--namespace $(NAMESPACE) \
		--create-namespace \
		--set image.repository=$(DOCKER_IMAGE) \
		--set image.tag=$(DEPLOY_TAG) \
		--set image.pullPolicy=IfNotPresent \
		--set autoscaling.enabled=false \
		--set app.environment.JWT_AUTH_DISABLED_MOCK_LOCAL_PRINCIPAL.value=local-dev-user
	@kubectl -n $(NAMESPACE) rollout status deploy/$(DEPLOYMENT_NAME) --timeout=180s
	@existing_pid=$$(lsof -tiTCP:18080 -sTCP:LISTEN || true); \
	if [ -n "$$existing_pid" ]; then \
		echo "Stopping existing listener on 18080 (PID $$existing_pid)..."; \
		kill $$existing_pid >/dev/null 2>&1 || true; \
	fi
	@nohup kubectl -n $(NAMESPACE) port-forward svc/$(DEPLOYMENT_NAME) 18080:80 >/tmp/$(RELEASE_NAME)-port-forward.log 2>&1 &
	@sleep 2
	@curl -sf http://127.0.0.1:18080/health >/dev/null \
		&& echo "Deployment ready. Local access: http://127.0.0.1:18080" \
		|| echo "Port-forward started; verify with: tail -f /tmp/$(RELEASE_NAME)-port-forward.log"

test-coverage: ## Run tests with coverage report
	@echo "Running tests with coverage..."
	go test -race -timeout $(TEST_TIMEOUT) -coverprofile=coverage.out $(TEST_COVERAGE_PKGS)
	go tool cover -html=coverage.out -o coverage.html

fmt: ## Format Go source files
	@echo "Formatting code..."
	go fmt ./...
	gofmt -s -w $(GO_FILES)

lint: ## Run linters
	@echo "Running linters..."
	@set -e; \
	if ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "golangci-lint not found; running go vet on $(LINT_PATHS)"; \
		go vet $(LINT_PATHS); \
		exit 0; \
	fi; \
	out=$$(mktemp); \
	if golangci-lint run $(LINT_PATHS) >$$out 2>&1; then \
		cat $$out; \
		rm -f $$out; \
		exit 0; \
	fi; \
	if grep -q "used to build golangci-lint is lower than the targeted Go version" $$out; then \
		cat $$out; \
		echo "golangci-lint binary is incompatible with module Go version; falling back to go vet on $(LINT_PATHS)"; \
		rm -f $$out; \
		go vet $(LINT_PATHS); \
		exit 0; \
	fi; \
	cat $$out; \
	rm -f $$out; \
	exit 1

license-check: ## Basic SPDX/copyright header check for Go files
	@echo "Checking license headers..."
	@failed=0; \
	for file in $$(git ls-files '*.go' ':!:gen/**' ':!:bin/**'); do \
		if ! head -n 5 $$file | grep -q "Copyright The Linux Foundation"; then \
			echo "Missing copyright header: $$file"; \
			failed=1; \
		fi; \
		if ! head -n 5 $$file | grep -q "SPDX-License-Identifier: MIT"; then \
			echo "Missing SPDX header: $$file"; \
			failed=1; \
		fi; \
	done; \
	if [ $$failed -ne 0 ]; then \
		echo "License check failed"; \
		exit 1; \
	fi

check: ## Non-mutating quality gate
	@echo "Running quality checks..."
	@if [ -n "$$(gofmt -l $(GO_FILES))" ]; then \
		echo "Unformatted Go files:"; \
		gofmt -l $(GO_FILES); \
		exit 1; \
	fi
	@$(MAKE) lint
	@$(MAKE) license-check

verify: apigen ## Verify generated code is up to date
	@echo "Verifying generated artifacts..."
	@if [ -n "$$(git status --porcelain gen/)" ]; then \
		echo "Generated code is out of date:"; \
		git status --porcelain gen/; \
		exit 1; \
	fi

clean: ## Clean build artifacts
	@echo "Cleaning..."
	rm -rf bin/ gen/ coverage.out coverage.html

docker-build: ## Build Docker image
	@echo "Building Docker image..."
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

docker-run: ## Run Docker container
	@echo "Running Docker container..."
	docker run -p 8080:8080 $(DOCKER_IMAGE):$(DOCKER_TAG)

helm-install: ## Install/upgrade Helm release
	helm upgrade --force --install $(HELM_RELEASE_NAME) $(HELM_CHART_PATH) --namespace $(HELM_NAMESPACE) --create-namespace

helm-install-local: ## Install/upgrade Helm release with local values override
	helm upgrade --force --install $(HELM_RELEASE_NAME) $(HELM_CHART_PATH) --namespace $(HELM_NAMESPACE) --create-namespace -f $(HELM_LOCAL_VALUES_FILE)

helm-templates: ## Render Helm templates
	helm template $(HELM_RELEASE_NAME) $(HELM_CHART_PATH) --namespace $(HELM_NAMESPACE)

helm-templates-local: ## Render Helm templates with local values override
	helm template $(HELM_RELEASE_NAME) $(HELM_CHART_PATH) --namespace $(HELM_NAMESPACE) -f $(HELM_LOCAL_VALUES_FILE)

helm-uninstall: ## Uninstall Helm release
	helm uninstall $(HELM_RELEASE_NAME) --namespace $(HELM_NAMESPACE)

.DEFAULT_GOAL := help
