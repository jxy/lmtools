# argo CLI

`argo` is a command-line client for the Argo API that provides AI model interactions including chat conversations, embeddings generation, and session management with branching support.

## Installation

Build the binary (requires Go 1.21+):

```bash
# Build using make (recommended)
make build

# Or build directly
go build -o ./bin/argo ./cmd/argo
```

## Usage examples

### Basic Usage

```bash
# Generate embeddings (sessions automatically disabled)
echo "Hello world" | argo -u yourname -e

# Chat mode with automatic session creation
echo "What is Argo?" | argo -u yourname

# Streaming chat
echo "Tell me a story" | argo -u yourname --stream

# Use specific model
echo "Explain quantum computing" | argo -u yourname -m gpt4o
```

### Session Management

```bash
# Show all conversation sessions
argo -u yourname -show-sessions

# Resume a previous session
echo "Continue from where we left off" | argo -u yourname -resume 0001

# Branch from a specific message
echo "Let me rephrase that" | argo -u yourname -branch 0001/0002

# Delete a session or message
argo -u yourname -delete 0001

# Show a specific conversation or message
argo -u yourname -show 0001
argo -u yourname -show 0001/0002
```

### Advanced Options

```bash
# Use custom sessions directory
echo "Test" | argo -u yourname -sessions-dir /tmp/my-sessions

# Custom environment/API endpoint
echo "Test" | argo -u yourname --env prod
echo "Test" | argo -u yourname --env https://custom.example/api

# Set system prompt
echo "Hello" | argo -u yourname -s "You are a helpful coding assistant"

# Disable session tracking
echo "One-off question" | argo -u yourname -no-session
```

## Command-line Flags

### Required
- `-u string`: User identifier (required for most operations)

### Model Selection
- `-m string`: Model to use (default: "gemini25pro" for chat, "v3large" for embed)
- `-e`: Enable embed mode for generating embeddings

### Chat Options
- `--stream`: Use streaming chat mode for real-time responses
- `-s string`: System prompt for chat mode (default: "You are a brilliant assistant.")

### Session Management
- `-resume string`: Resume session or branch by ID/path
- `-branch string`: Create branch from specific message ID
- `-show-sessions`: Display all conversation trees
- `-show string`: Show specific session or message by ID/path
- `-delete string`: Delete node (session/branch/message) and its descendants
- `-no-session`: Disable session creation (automatically set for embed mode)
- `-sessions-dir string`: Custom sessions directory (default: ~/.argo/sessions)

### Configuration
- `--env string`: Environment - "prod", "dev", or custom API URL (default: "dev")
- `--timeout duration`: HTTP request timeout (default: 10m)
- `--retries int`: Number of retry attempts for failed requests (default: 3)

## Data Storage

Argo stores its data in the `~/.argo` directory:

- `~/.argo/sessions/`: Conversation history with tree-based branching
- `~/.argo/logs/`: Process logs and request/response logs
  - Process logs: `YYYYMMDDTHHMMSS_argo_PID.log`
  - Request/response logs: `YYYYMMDDTHHMMSS_operation_*.json`

## Session Features

Argo provides sophisticated session management:

- **Automatic session tracking**: Each conversation is saved with a unique ID
- **Resume conversations**: Continue previous discussions with full context
- **Branching support**: Create alternative conversation paths from any message
- **Tree visualization**: View conversation history as a tree structure
- **Concurrent safety**: Multiple processes can safely access sessions simultaneously

## Development

```bash
# Run tests
make test

# Run linting
make lint

# Run integration tests
go test -tags=integration ./cmd/argo

# Run e2e tests
go test -tags=e2e ./cmd/argo
```

## Supported Models

### Chat Models
gpt35, gpt35large, gpt4, gpt4large, gpt4turbo, gpt4o, gpt4olatest, gpto1mini, gpto3mini, gpto1, gpto3, gpto4mini, gpt41, gpt41mini, gpt41nano, gemini25pro, gemini25flash, claudeopus4, claudesonnet4, claudesonnet37, claudesonnet35v2

### Embedding Models
ada002, v3large, v3small