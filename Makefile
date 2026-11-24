# This Makefile runs under BSD make and GNU make, including the GNU make 3.81
# that ships with macOS. Keep it that way: no $(shell ...), $(if ...),
# $(filter ...), the != assignment, or bash-only recipe syntax. Anything that
# has to be computed is computed by POSIX sh inside the recipe that needs it.
#
# Cross-compilation: set GOOS and GOARCH on the command line or in the
# environment; they default to the host. Every build lands in
# BIN_DIR/<goos>-<goarch>, a host build included, so builds for different
# platforms never overwrite each other. BUILD_SUBDIR names that subdirectory
# explicitly, and BUILD_SUBDIR=. writes the binaries into BIN_DIR itself.
GOOS ?=
GOARCH ?=
BIN_DIR ?= ./bin
BUILD_SUBDIR ?=

.PHONY: all build test test-unit test-integration test-e2e test-all test-context coverage lint lint-fix clean dev check verify-fixtures verify-fixtures-refresh help

# Default target
all: lint test build

# Build all binaries for the selected platform
build:
	@set -e; \
	goos="$(GOOS)"; [ -n "$$goos" ] || goos=$$(go env GOOS); \
	goarch="$(GOARCH)"; [ -n "$$goarch" ] || goarch=$$(go env GOARCH); \
	exe=; [ "$$goos" != windows ] || exe=.exe; \
	subdir="$(BUILD_SUBDIR)"; [ -n "$$subdir" ] || subdir="$$goos-$$goarch"; \
	out="$(BIN_DIR)"; [ "$$subdir" = . ] || out="$$out/$$subdir"; \
	mkdir -p "$$out"; \
	for cmd in lmc apiproxy alcfauth notebook; do \
	    echo "GOOS=$$goos GOARCH=$$goarch go build -o $$out/$$cmd$$exe ./cmd/$$cmd"; \
	    GOOS=$$goos GOARCH=$$goarch go build -o "$$out/$$cmd$$exe" "./cmd/$$cmd"; \
	done

# Run unit tests for all packages
test: test-unit

# Run unit tests
test-unit:
	go test -race ./internal/... ./cmd/lmc ./cmd/apiproxy ./cmd/apifixtures ./cmd/alcfauth ./cmd/notebook

# Run integration tests (requires built binaries)
test-integration: build
	go test -race -tags=integration ./cmd/lmc ./internal/...

# Run e2e tests (end-to-end with mock servers)
test-e2e: build
	go test -race -tags=e2e ./cmd/lmc ./internal/...

# Run all tests (unit, integration, e2e)
test-all: test-unit test-integration test-e2e

# Offline notebook and session context regressions.
test-context:
	go test -race ./internal/workingcontext ./internal/notebook ./internal/oldsession ./internal/config ./cmd/notebook ./cmd/lmc
	go test -race -tags=integration ./cmd/lmc -run '^TestContextNotebook'

# Run tests with coverage for all packages
coverage:
	go test -race -coverprofile=coverage.out ./internal/... ./cmd/lmc ./cmd/apiproxy ./cmd/apifixtures ./cmd/alcfauth
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
	@echo "  make build           - Build all binaries for the host into ./bin/<goos>-<goarch>"
	@echo "  make build GOOS=linux GOARCH=amd64 - Cross-build all binaries into ./bin/linux-amd64"
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
	@echo "  make test-context    - Run the notebook and session context regression tests"
	@echo "  make help            - Show this help message"
