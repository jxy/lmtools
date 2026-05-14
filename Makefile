SHELL := /bin/bash

HOST_GOOS := $(shell go env GOOS)
HOST_GOARCH := $(shell go env GOARCH)

GOOS ?= $(HOST_GOOS)
GOARCH ?= $(HOST_GOARCH)
BIN_DIR ?= ./bin

EXE_SUFFIX := $(if $(filter windows,$(GOOS)),.exe,)
BUILD_SUBDIR ?= $(if $(filter $(HOST_GOOS)-$(HOST_GOARCH),$(GOOS)-$(GOARCH)),,$(GOOS)-$(GOARCH))
BUILD_OUT_DIR := $(BIN_DIR)$(if $(BUILD_SUBDIR),/$(BUILD_SUBDIR),)

.PHONY: all build test test-unit test-integration test-e2e test-all coverage lint lint-fix clean dev check verify-fixtures verify-fixtures-refresh help

# Default target
all: lint test build

# Build all binaries for the selected platform
build:
	@mkdir -p $(BUILD_OUT_DIR)
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(BUILD_OUT_DIR)/lmc$(EXE_SUFFIX) ./cmd/lmc
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(BUILD_OUT_DIR)/apiproxy$(EXE_SUFFIX) ./cmd/apiproxy

# Run unit tests for all packages
test: test-unit

# Run unit tests
test-unit:
	go test -race ./internal/... ./cmd/lmc ./cmd/apiproxy

# Run integration tests (requires built binaries)
test-integration: build
	go test -race -tags=integration ./cmd/lmc ./internal/...

# Run e2e tests (end-to-end with mock servers)
test-e2e: build
	go test -race -tags=e2e ./cmd/lmc ./internal/...

# Run all tests (unit, integration, e2e)
test-all: test-unit test-integration test-e2e

# Run tests with coverage for all packages
coverage:
	go test -race -coverprofile=coverage.out ./internal/... ./cmd/lmc ./cmd/apiproxy
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report saved to coverage.html"
	@go tool cover -func=coverage.out | grep "total:" | awk '{print "Total coverage: " $$3}'

# Run linting (both unit-only and with build tags)
lint:
	golangci-lint run
	golangci-lint run --build-tags=integration
	golangci-lint run --build-tags=e2e

# Auto-fix linting issues (unit-only); then check tagged build
lint-fix:
	golangci-lint run --fix
	golangci-lint run --build-tags=integration --fix
	golangci-lint run --build-tags=e2e --fix

# Clean build artifacts
clean:
	rm -rf ./bin coverage.out coverage.html

# Development workflow - lint, test, build
dev: lint test build

# Quick check before commit
check: lint-fix test

# Verify checked-in API fixture corpus and targeted fixture tests
verify-fixtures:
	bash ./scripts/api_fixtures_verify.sh

# Refresh live captures, then fail if fixture diffs need review
verify-fixtures-refresh:
	bash ./scripts/api_fixtures_verify.sh --refresh

# Help message
help:
	@echo "Available targets:"
	@echo ""
	@echo "  make                 - Run lint, test, and build (default)"
	@echo "  make build           - Build all binaries for GOOS/GOARCH, defaulting to the host"
	@echo "  make build GOOS=linux GOARCH=amd64 - Cross-build all binaries"
	@echo "  make test            - Run all unit tests"
	@echo "  make test-integration - Run integration tests (requires binaries)"
	@echo "  make test-e2e        - Run end-to-end tests with mock servers"
	@echo "  make test-all        - Run all tests (unit, integration, e2e)"
	@echo "  make coverage        - Generate test coverage report for all packages"
	@echo "  make lint            - Run linting checks"
	@echo "  make lint-fix        - Auto-fix linting issues"
	@echo "  make clean           - Remove build artifacts"
	@echo "  make dev             - Full development cycle (lint, test, build)"
	@echo "  make check           - Quick check before commit (lint-fix, test)"
	@echo "  make verify-fixtures - Verify the API fixture corpus and targeted fixture tests"
	@echo "  make verify-fixtures-refresh - Refresh fixture captures and fail if diffs need review"
	@echo "  make help            - Show this help message"
