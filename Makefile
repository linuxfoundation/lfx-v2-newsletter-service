# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT

APP_NAME := lfx-v2-newsletter-service/newsletter-api
VERSION := $(shell git describe --tags --always 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GIT_COMMIT := $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")

DOCKER_REGISTRY := ghcr.io/linuxfoundation
DOCKER_IMAGE := $(DOCKER_REGISTRY)/$(APP_NAME)
DOCKER_TAG := latest

HELM_CHART_PATH := ./charts/lfx-v2-newsletter-service
HELM_RELEASE_NAME := lfx-v2-newsletter-service
HELM_NAMESPACE := lfx
HELM_VALUES_FILE := ./charts/lfx-v2-newsletter-service/values.local.yaml

CNPG_NAMESPACE := cnpg-system
CNPG_VERSION := 0.28.0
KUBE_CONTEXT ?= orbstack
KO_LOCAL_REPO := ko.local
KO_LOCAL_TAG := local

GO_VERSION := 1.25.0
GOLANGCI_LINT_VERSION := v2.12.2
LINT_TIMEOUT := 10m
LINT_TOOL := $(shell go env GOPATH)/bin/golangci-lint
GO_FILES := $(shell find . -name '*.go' -not -path './gen/*' -not -path './vendor/*')

##@ Development

.PHONY: setup
setup:
	go mod download
	go mod tidy

.PHONY: deps
deps: setup

.PHONY: build
build:
	go build \
		-ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT)" \
		-o bin/$(APP_NAME) ./cmd/newsletter-api

.PHONY: run
run: build
	./bin/$(APP_NAME)

.PHONY: test
test:
	go test -v -race -coverprofile=coverage.out ./...

.PHONY: fmt
fmt:
	go fmt ./...
	gofmt -s -w $(GO_FILES)

.PHONY: lint
lint:
	@which golangci-lint >/dev/null 2>&1 || go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	golangci-lint run ./...

.PHONY: license-check
license-check:
	@missing_files=$$(find . \( -name "*.go" \) \
		-not -path "./vendor/*" \
		-exec sh -c 'head -10 "$$1" | grep -q "Copyright The Linux Foundation" || echo "$$1"' _ {} \;); \
	if [ -n "$$missing_files" ]; then \
		echo "Files missing license headers:"; echo "$$missing_files"; exit 1; \
	fi

.PHONY: check
check: fmt lint license-check
	go vet ./...

##@ Docker

.PHONY: docker-build
docker-build:
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .
	docker tag $(DOCKER_IMAGE):$(DOCKER_TAG) $(DOCKER_IMAGE):latest

.PHONY: ko-build
ko-build:
	@which ko >/dev/null 2>&1 || (echo "ko not found — install from https://ko.build" && exit 1)
	KO_DOCKER_REPO=$(KO_LOCAL_REPO) \
		VERSION=$(VERSION) BUILD_TIME=$(BUILD_TIME) GIT_COMMIT=$(GIT_COMMIT) \
		ko build --local --bare --tags=$(KO_LOCAL_TAG) ./cmd/newsletter-api

##@ Helm/Kubernetes

# helm-install-operators installs the CloudNativePG operator cluster-wide. Run
# this once per cluster before helm-install-external, helm-install-cnpg, or
# helm-install-local. The operator lives in its own namespace so its lifecycle
# is decoupled from this chart.
.PHONY: helm-install-operators
helm-install-operators:
	@echo "==> Installing CloudNativePG operator..."
	helm repo add cnpg https://cloudnative-pg.github.io/charts >/dev/null 2>&1 || true
	helm repo update cnpg >/dev/null
	helm upgrade --install cnpg cnpg/cloudnative-pg \
		--version $(CNPG_VERSION) \
		--namespace $(CNPG_NAMESPACE) --create-namespace \
		--kube-context $(KUBE_CONTEXT)

# helm-install-external installs the chart in "external" Postgres mode. The
# Kubernetes Secret referenced by database.external.secretName must exist in
# $(HELM_NAMESPACE) and contain a DATABASE_URL under database.external.secretKey
# (default: "url"). See README "Quick Start" for the expected secret format.
.PHONY: helm-install-external
helm-install-external:
	@echo "==> Installing $(HELM_RELEASE_NAME) in external Postgres mode..."
	helm upgrade --install $(HELM_RELEASE_NAME) $(HELM_CHART_PATH) \
		--namespace $(HELM_NAMESPACE) --create-namespace \
		--set database.mode=external \
		--kube-context $(KUBE_CONTEXT)

# helm-install-cnpg installs the chart in "cluster+database" mode — the chart
# provisions both a CloudNativePG Cluster and a Database CR. Requires the CNPG
# operator (helm-install-operators) to be installed first.
.PHONY: helm-install-cnpg
helm-install-cnpg:
	@echo "==> Installing $(HELM_RELEASE_NAME) in CloudNativePG mode..."
	helm upgrade --install $(HELM_RELEASE_NAME) $(HELM_CHART_PATH) \
		--namespace $(HELM_NAMESPACE) --create-namespace \
		--set database.mode=cluster+database \
		--set database.cloudNativePG.clusterName=lfx-newsletter-db \
		--kube-context $(KUBE_CONTEXT)

# helm-install-local installs the chart using values.local.yaml (defaults to
# cluster+database mode against the ko.local image). Run helm-install-operators
# first. Copy charts/lfx-v2-newsletter-service/values.local.yaml.example to
# values.local.yaml before running.
.PHONY: helm-install-local
helm-install-local:
	helm upgrade --force --install $(HELM_RELEASE_NAME) $(HELM_CHART_PATH) \
		--namespace $(HELM_NAMESPACE) --create-namespace \
		--values $(HELM_VALUES_FILE)

# helm-uninstall removes the chart release. Run helm-uninstall-cnpg afterwards
# (in cluster+database mode) if you want to remove the Postgres data as well.
.PHONY: helm-uninstall
helm-uninstall:
	@echo "==> Uninstalling $(HELM_RELEASE_NAME)..."
	helm uninstall $(HELM_RELEASE_NAME) --namespace $(HELM_NAMESPACE) --kube-context $(KUBE_CONTEXT)

.PHONY: helm-templates
helm-templates:
	helm template $(HELM_RELEASE_NAME) $(HELM_CHART_PATH) --namespace $(HELM_NAMESPACE)
