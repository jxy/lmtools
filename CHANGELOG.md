# Changelog

All notable changes to this project are documented in this file.

## [Unreleased]

### Added
- Extracted flags (`flags.go`), request builder (`request.go`), logger helpers (`logger.go`), HTTP client (`client.go`), and response handler (`response.go`).
- Comprehensive unit tests for each helper package.
- Streaming and non-streaming chat, embedding, and prompt-chat modes preserved.
- `-env` flag for selecting prod/dev/custom API URLs.
- `-log-level` flag with info/debug logging wrapper.
- Integration test with `httptest.Server`.
- CI workflow (`.github/workflows/ci.yml`) and `Makefile` (lint/test/build).
- README usage examples and `CHANGELOG.md` scaffolded.

### Changed
- Refactored `lmc.go` into `lmc/cmd/main.go` calling into extracted packages.
  Core logic unchanged; behavior preserved.

### CI/Tooling
- Added `golangci-lint` config (enabling `gofumpt`).
- CI runs `go fmt`, `go test`, `golangci-lint run`, `go vet` on `lmc` packages.
  Root-level files (other mains) are not linted to avoid conflicts.

### Formatting
- Applied `gofmt` via `go fmt ./lmc` in CI; additional stricter formatting available via `gofumpt`.

## [0.2.0] - TBD
- Release prepared for refactor changes.