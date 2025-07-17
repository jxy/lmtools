SHELL := /bin/bash

.PHONY: all build test lint lint-fix clean coverage help

# Default target
all: lint test build

# Build the argo binary
build:
	go build -o ./bin/argo ./cmd/argo

# Run all tests
test:
	go test -v -race ./argolib ./cmd/argo

# Run integration tests (cross-process tests)
test-integration: build
	go test -v -race -tags=integration ./cmd/argo

# Run e2e tests (end-to-end with mock server)
test-e2e: build
	go test -v -race -tags=e2e ./cmd/argo

# Run all tests including integration and e2e
test-all: test test-integration test-e2e

# Run tests with coverage
coverage:
	go test -race -coverprofile=coverage.out ./argolib ./cmd/argo
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
	@echo "  make              - Run lint, test, and build (default)"
	@echo "  make build        - Build the argo binary"
	@echo "  make test         - Run unit tests"
	@echo "  make test-integration - Run cross-process integration tests"
	@echo "  make test-e2e     - Run end-to-end tests with mock server"
	@echo "  make test-all     - Run all tests (unit, integration, e2e)"
	@echo "  make coverage     - Generate test coverage report"
	@echo "  make lint         - Run linting"
	@echo "  make lint-fix     - Auto-fix linting issues"
	@echo "  make clean        - Remove build artifacts"
	@echo "  make dev          - Full development cycle (lint, test, build)"
	@echo "  make check        - Quick check before commit (lint-fix, test)"
	@echo "  make help         - Show this help message"
