SHELL := /bin/bash

.PHONY: all build test test-unit test-integration test-e2e test-all coverage lint lint-fix clean dev check help

# Default target
all: lint test build

# Build all binaries
build:
	go build -o ./bin/lmc ./cmd/lmc
	go build -o ./bin/apiproxy ./cmd/apiproxy

# Run unit tests for all packages
test: test-unit

# Run unit tests
test-unit:
	go test -v -race ./internal/... ./cmd/lmc ./cmd/apiproxy

# Run integration tests (requires built binaries)
test-integration: build
	go test -v -race -tags=integration ./cmd/lmc ./internal/...

# Run e2e tests (end-to-end with mock servers)
test-e2e: build
	go test -v -race -tags=e2e ./cmd/lmc ./internal/...

# Run all tests (unit, integration, e2e)
test-all: test-unit test-integration test-e2e

# Run tests with coverage for all packages
coverage:
	go test -race -coverprofile=coverage.out ./internal/... ./cmd/lmc ./cmd/apiproxy
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report saved to coverage.html"
	@go tool cover -func=coverage.out | grep "total:" | awk '{print "Total coverage: " $$3}'

# Run linting
lint:
	golangci-lint run

# Auto-fix linting issues
lint-fix:
	golangci-lint run --fix

# Clean build artifacts
clean:
	rm -rf ./bin coverage.out coverage.html

# Development workflow - lint, test, build
dev: lint test build

# Quick check before commit
check: lint-fix test

# Help message
help:
	@echo "Available targets:"
	@echo ""
	@echo "  make                 - Run lint, test, and build (default)"
	@echo "  make build           - Build all binaries (lmc and apiproxy)"
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
	@echo "  make help            - Show this help message"
