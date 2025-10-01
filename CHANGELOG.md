# Changelog

All notable changes to this project are documented in this file.

## [Unreleased]

### Added
- **Tool Execution Support**: New `-tool` flag enables built-in `universal_command` tool for system command execution
  - Commands executed directly via execvpe (not through shell)
  - Tool-specific system prompt automatically applied when `-tool` is enabled
  - Configurable timeout with `-tool-timeout` flag (default: 30s)
  - Whitelist/blacklist support for command approval
  - `-tool-auto-approve` flag for skipping manual approval on whitelisted commands
  - `-max-tool-rounds` flag to limit tool execution iterations (default: 32)
- **Centralized Prompts**: All system prompts and text constants now in `internal/prompts/prompts.go`
- **Improved Flag Detection**: Using Go's `flag.Visit()` for proper detection of explicitly-set flags
- **Session Tool Support**: Sessions now properly handle tool calls and results
  - Tool interactions saved with `.tools.json` files
  - Pending tool execution on session resume
  - Proper message sequencing for API compatibility
- **Session Forking**: Automatic session forking when resuming with different system prompts
  - Resuming with `-tool` flag creates a fork if original has DefaultSystemPrompt
  - Original sessions preserved unchanged
  - User notified when forking occurs

### Fixed
- **Critical Bug**: Sessions with pending tool calls now execute tools correctly on resume
- **System Prompt**: `-tool` flag now correctly applies tool-specific system prompt
- **Test Performance**: Consolidated binary building in tests (reduced from 16 builds to 1)
- **Flag Parsing**: Replaced manual argument parsing with idiomatic Go flag handling
- **E2E Tests**: Fixed build failures after TypedMessage refactoring

### Changed
- **Flag Naming**: Renamed `-tools` (plural) to `-tool` (singular) for consistency
- **Tool Integration**: Universal command tool now built into binary (removed external JSON requirement)
- **Documentation**: Updated README.md and CLAUDE.md to reflect all flag changes
- **System Prompt**: Enhanced tool system prompt with detailed command execution guidelines
- **Message Handling**: Major refactoring to use TypedMessage throughout codebase
  - Replaced SimpleChatMessage and SimpleChatRequest with TypedMessage
  - Introduced Block interface pattern (TextBlock, ToolUseBlock, ToolResultBlock)
  - Unified message format conversions with ToAnthropic(), ToOpenAI(), ToGoogle()
- **Default Models**: Updated default models for all providers
  - Argo: gpt5 (was claudeopus4)
  - OpenAI: gpt-5 (was gpt-4-turbo-preview)
  - Google: gemini-2.5-pro (was gemini-pro)
  - Anthropic: claude-opus-4-1-20250805 (was claude-3-opus-20240229)
- **Code Organization**: Restructured internal/core module
  - Added message_types.go for TypedMessage and Block types
  - Added models.go for model constants and defaults
  - Added errors.go for error handling utilities
  - Moved built-in tools to builtin_tools.go

### Technical Debt Resolved
- Eliminated redundant test binary builds
- Removed manual command-line argument parsing
- Centralized all prompts and text constants

### Code Simplicity Improvements
- **Consolidated Tool Execution**: Removed duplicate tool execution loop between `cmd/lmc/main.go` and `internal/core/tools.go`
  - All tool execution now flows through centralized `core.HandleToolExecution`
  - Eliminated ~200 lines of duplicate code
- **Eliminated Global State**: Replaced global `RequestCache` with explicit `ToolConversation` struct
  - State now passed explicitly as function parameters
  - Improved testability and reduced coupling
  - Eliminated concurrency risks from mutable global state
- **Unified File Placement**: Consolidated `tryPlaceMessage` and `tryPlaceMessageWithTools` functions
  - Single unified `tryPlaceMessageFiles` function handles all message types
  - Reduced code duplication from ~150 lines to ~80 lines
- **Centralized Pending Tools**: Extracted pending tools execution into reusable `executePendingTools` function
  - Eliminated ~40 lines of duplicate code
  - Single source of truth for pending tools handling
- **Documentation Consistency**: Fixed whitelist format documentation to match implementation
  - README and flag help now correctly specify JSON array format
- **Streaming Headers**: Fixed tool result requests to use appropriate non-streaming headers

### Previous Changes

#### Support for new models
- Support for new GPT-5 models in Argo provider: `gpt5`, `gpt5mini`, `gpt5nano`

#### Initial refactoring
- Extracted flags (`flags.go`), request builder (`request.go`), logger helpers (`logger.go`), HTTP client (`client.go`), and response handler (`response.go`).
- Comprehensive unit tests for each helper package.
- Streaming and non-streaming chat, embedding, and prompt-chat modes preserved.
- `-argo-env` flag for selecting prod/dev/custom API URLs.
- Integration test with `httptest.Server`.
- CI workflow (`.github/workflows/ci.yml`) and `Makefile` (lint/test/build).
- README usage examples and `CHANGELOG.md` scaffolded.
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