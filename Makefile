SHELL := /bin/bash

.PHONY: all build test lint lint-fix clean coverage help

# Default target
all: lint test build

# Build the argo binary
build:
	go build -o ./bin/argo ./cmd/argo

# Run all tests
test:
	go test -v ./argolib ./cmd/argo

# Run tests with coverage
coverage:
	go test -coverprofile=coverage.out ./argolib ./cmd/argo
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
	@echo "  make          - Run lint, test, and build (default)"
	@echo "  make build    - Build the argo binary"
	@echo "  make test     - Run all tests"
	@echo "  make coverage - Generate test coverage report"
	@echo "  make lint     - Run linting"
	@echo "  make lint-fix - Auto-fix linting issues"
	@echo "  make clean    - Remove build artifacts"
	@echo "  make dev      - Full development cycle (lint, test, build)"
	@echo "  make check    - Quick check before commit (lint-fix, test)"
	@echo "  make help     - Show this help message"
