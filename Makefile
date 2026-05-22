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

##@ Helm/Kubernetes

.PHONY: helm-install-local
helm-install-local:
	helm upgrade --force --install $(HELM_RELEASE_NAME) $(HELM_CHART_PATH) \
		--namespace $(HELM_NAMESPACE) --create-namespace \
		--values $(HELM_VALUES_FILE)

.PHONY: helm-templates
helm-templates:
	helm template $(HELM_RELEASE_NAME) $(HELM_CHART_PATH) --namespace $(HELM_NAMESPACE)
