# lmc CLI

`lmc` is a command-line client for the LMC API that provides AI model interactions including chat conversations, embeddings generation, and session management with branching support.

## Installation

Build the binary (requires Go 1.21+):

```bash
# Build using make (recommended)
make build

# Or build directly
go build -o ./bin/lmc ./cmd/lmc
```

## Usage examples

### Basic Usage

```bash
# Generate embeddings (sessions automatically disabled)
echo "Hello world" | lmc -u yourname -e

# Chat mode with automatic session creation
echo "What is LMC?" | lmc -u yourname

# Streaming chat
echo "Tell me a story" | lmc -u yourname -stream

# Use specific model
echo "Explain quantum computing" | lmc -u yourname -m gpt4o
```

### Session Management

```bash
# Show all conversation sessions
lmc -u yourname -show-sessions

# Resume a previous session
echo "Continue from where we left off" | lmc -u yourname -resume 0001

# Branch from a specific message
echo "Let me rephrase that" | lmc -u yourname -branch 0001/0002

# Delete a session or message
lmc -u yourname -delete 0001

# Show a specific conversation or message
lmc -u yourname -show 0001
lmc -u yourname -show 0001/0002
```

### Advanced Options

```bash
# Use custom sessions directory
echo "Test" | lmc -u yourname -sessions-dir /tmp/my-sessions

# Custom environment/API endpoint
echo "Test" | lmc -u yourname -env prod
echo "Test" | lmc -u yourname -env https://custom.example/api

# Set system prompt
echo "Hello" | lmc -u yourname -s "You are a helpful coding assistant"

# Disable session tracking
echo "One-off question" | lmc -u yourname -no-session
```

## Command-line Flags

### Required
- `-u string`: User identifier (required for most operations)

### Model Selection
- `-m string`: Model to use (default: "gemini25pro" for chat, "v3large" for embed)
- `-e`: Enable embed mode for generating embeddings

### Chat Options
- `-stream`: Use streaming chat mode for real-time responses
- `-s string`: System prompt for chat mode (default: "You are a brilliant assistant.")

### Session Management
- `-resume string`: Resume session or branch by ID/path
- `-branch string`: Create branch from specific message ID
- `-show-sessions`: Display all conversation trees
- `-show string`: Show specific session or message by ID/path
- `-delete string`: Delete node (session/branch/message) and its descendants
- `-no-session`: Disable session creation (automatically set for embed mode)
- `-sessions-dir string`: Custom sessions directory (default: ~/.lmc/sessions)

### Configuration
- `-env string`: Environment - "prod", "dev", or custom API URL (default: "dev")
- `-timeout duration`: HTTP request timeout (default: 10m)
- `-retries int`: Number of retry attempts for failed requests (default: 3)

## Data Storage

LMC stores its data in the `~/.lmc` directory:

- `~/.lmc/sessions/`: Conversation history with tree-based branching
- `~/.lmc/logs/`: Process logs and request/response logs
  - Process logs: `YYYYMMDDTHHMMSS_lmc_PID.log`
  - Request/response logs: `YYYYMMDDTHHMMSS_operation_*.json`

## Session Features

LMC provides sophisticated session management:

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
go test -tags=integration ./cmd/lmc

# Run e2e tests
go test -tags=e2e ./cmd/lmc
```

## Supported Models

### Chat Models
gpt35, gpt35large, gpt4, gpt4large, gpt4turbo, gpt4o, gpt4olatest, gpto1mini, gpto3mini, gpto1, gpto3, gpto4mini, gpt41, gpt41mini, gpt41nano, gemini25pro, gemini25flash, claudeopus4, claudesonnet4, claudesonnet37, claudesonnet35v2

### Embedding Models
ada002, v3large, v3small

---

# API Proxy - Anthropic to OpenAI/Google/Argo

The lmtools suite includes an API proxy that translates between Anthropic's Messages API format and OpenAI, Google Gemini, or Argo APIs.

## API Proxy Features

- **Protocol Translation**: Seamlessly converts between Anthropic, OpenAI, Google Gemini, and Argo API formats
- **Model Mapping**: Automatically maps Claude model names (haiku/sonnet) to configured models
- **Multi-Provider Support**: Route requests to OpenAI, Google Gemini, or Argo based on configuration
- **Pure Go**: No external dependencies, uses only Go standard library
- **Streaming Support**: Server-Sent Events for real-time responses
- **Token Counting**: Estimate token usage for requests

## API Proxy Quick Start

### Prerequisites

- Go 1.19 or later
- API keys for the providers you want to use:
  - OpenAI API key
  - Google AI Studio (Gemini) API key
  - Argo user credentials

### Installation

```bash
# Build both lmc and apiproxy
make build

# Or build apiproxy separately
go build -o ./bin/apiproxy ./cmd/apiproxy
```

### Configuration

Configure the proxy using command-line flags:

```bash
# API Key configuration (pass files containing keys, not the keys directly)
-openai-api-key-file       Path to file containing OpenAI API key
-gemini-api-key-file       Path to file containing Gemini API key  
-anthropic-api-key-file    Path to file containing Anthropic API key

# Other configuration flags
-argo-user            Argo user
-argo-env             Argo environment (default: "dev")
-provider             Provider: openai, google, argo (default: "argo")
-provider-url         Custom URL for the selected provider (overrides default)
-big-model            Model for "sonnet" requests (default: "claudeopus4")
-small-model          Model for "haiku" requests (default: "claudesonnet4")

# Server configuration
-host                 Host to bind (default: "127.0.0.1")
-port                 Port to bind (default: 8082)

# Other options
-max-request-body-size   Maximum request body size in MB (default: 10)
-log-level              Log level: DEBUG, INFO, WARN, ERROR (default: "INFO")
-log-format             Log format: text, json (default: "text")
-no-color               Disable colored output
```

### Running the Proxy

```bash
# First, save your API keys to files (one key per file)
echo "sk-..." > ~/.openai-key
echo "AIza..." > ~/.gemini-key
chmod 600 ~/.openai-key ~/.gemini-key  # Secure the files

# Start with minimal configuration (OpenAI as default provider)
./bin/apiproxy -openai-api-key-file="$HOME/.openai-key"

# Use Google Gemini as provider
./bin/apiproxy -gemini-api-key-file="$HOME/.gemini-key" -provider="google"

# Use Argo with custom models
./bin/apiproxy -argo-user="username" -provider="argo" \
  -big-model="claudesonnet4" -small-model="gemini25flash"

# Custom host and port with debug logging
./bin/apiproxy -host="0.0.0.0" -port="8080" -log-level="DEBUG" \
  -openai-api-key-file="$HOME/.openai-key"

# Note: By default, the server binds to 127.0.0.1 (localhost only).
# Use -host="0.0.0.0" to allow external connections.
```

## API Proxy Usage Examples

### With Claude Code

```bash
# Set the base URL to your proxy
export ANTHROPIC_BASE_URL=http://localhost:8082

# Use Claude Code normally
claude
```

### Direct API Calls

```bash
# Basic chat completion
curl -X POST http://localhost:8082/v1/messages \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-haiku-20240307",
    "max_tokens": 1000,
    "messages": [
      {"role": "user", "content": "Hello, how are you?"}
    ]
  }'

# Count tokens
curl -X POST http://localhost:8082/v1/messages/count_tokens \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-sonnet-20240229",
    "messages": [
      {"role": "user", "content": "Count my tokens"}
    ]
  }'
```

### Testing the API Proxy

A test script is available to verify the proxy is working correctly:

```bash
# Run the test script (requires proxy to be running on port 8082)
./scripts/test_apiproxy.sh
```

## Model Mapping

The proxy automatically maps Anthropic model names to appropriate providers:

| Anthropic Model | Mapped To | Provider |
|----------------|-----------|----------|
| *haiku* | `SMALL_MODEL` | Based on model/config |
| *sonnet* | `BIG_MODEL` | Based on model/config |
| Other models | Direct passthrough | Auto-detected from provider model lists |

### Provider Selection Logic

1. If model contains "haiku" → maps to `SMALL_MODEL`
2. If model contains "sonnet" → maps to `BIG_MODEL`
3. Otherwise, checks if model exists in known model lists
4. Provider is determined by:
   - Model's presence in provider-specific lists
   - `PREFERRED_PROVIDER` setting
   - Availability of API keys

### Dynamic Model Defaults

When using the default models (`claudeopus4` and `claudesonnet4`), the proxy automatically selects provider-specific models based on your `-provider`:

| Provider | Big Model (sonnet) | Small Model (haiku) |
|----------|-------------------|---------------------|
| argo (default) | claudeopus4 | claudesonnet4 |
| openai | o3-mini | gpt-4o-mini |
| google | gemini-2.5-pro-preview-03-25 | gemini-2.0-flash |

**Note**: These automatic mappings only apply when using the default model values. If you specify custom models via `-big-model` or `-small-model`, those will be used exactly as specified.

## API Proxy Configuration Examples

### Use Argo (Default)

```bash
# Run proxy with Argo (no API key file needed, just username)
./bin/apiproxy \
  -argo-user="username" \
  -argo-env="prod"
# Uses default models: claudeopus4 for big, claudesonnet4 for small
```

### Use OpenAI

```bash
# Save API key to file
echo "sk-..." > ~/.openai-key
chmod 600 ~/.openai-key

# Run proxy
./bin/apiproxy \
  -openai-api-key-file="$HOME/.openai-key" \
  -provider="openai" \
  -big-model="gpt-4o" \
  -small-model="gpt-4o-mini"
```

### Use Google Gemini

```bash
# Save API key to file
echo "AIza..." > ~/.gemini-key
chmod 600 ~/.gemini-key

# Run proxy
./bin/apiproxy \
  -gemini-api-key-file="$HOME/.gemini-key" \
  -provider="google" \
  -big-model="gemini-2.5-pro-preview-03-25" \
  -small-model="gemini-2.0-flash"
```

## API Proxy Supported Models

### OpenAI Models
- o3-mini, o1, o1-mini, o1-pro
- gpt-4.5-preview, gpt-4o, gpt-4o-mini
- gpt-4o-audio-preview, gpt-4o-mini-audio-preview
- chatgpt-4o-latest
- gpt-4.1, gpt-4.1-mini

### Google Gemini Models
- gemini-2.5-pro-preview-03-25
- gemini-2.0-flash

### Argo Models
- All models from argolib including:
  - GPT variants: gpt35, gpt4, gpt4o, etc.
  - Gemini variants: gemini25pro, gemini25flash
  - Claude variants: claudeopus4, claudesonnet4, etc.

## API Proxy Architecture

The proxy is built with a modular architecture:

- **Configuration**: Command-line flag based configuration with validation
- **Model Mapper**: Handles model name mapping and provider selection
- **Converters**: Transform between API formats while preserving functionality
- **HTTP Server**: Routes requests to appropriate handlers
- **Streaming**: Server-Sent Events for real-time responses

## API Proxy Streaming

The proxy supports full Server-Sent Events (SSE) streaming:
- Native streaming for OpenAI and Gemini providers
- Simulated streaming for Argo (converts non-streaming responses)
- Automatic ping keep-alive events to prevent timeouts
- Provider-specific response parsing and formatting

## Thinking Field Support

The proxy supports the `thinking` field for enhanced reasoning capabilities:

### For Claude Models (opus/sonnet)
The thinking structure is passed through as-is to enable native thinking mode:
```json
{
  "thinking": {
    "type": "enabled",
    "budget_tokens": 31999
  }
}
```

### For GPT/O3/O4 Models
The thinking structure is automatically converted to `reasoning_effort: "high"`:
```json
// Input:
{
  "thinking": {
    "type": "enabled",
    "budget_tokens": 31999
  }
}

// Converted to:
{
  "reasoning_effort": "high"
}
```

### Testing Thinking Field
Use the provided test script:
```bash
./scripts/test_thinking.sh
```

## API Proxy Limitations

- Token counting is estimated, not exact
- Some advanced features may not be fully supported across all providers
- Error messages from providers are passed through with minimal modification

## API Proxy Troubleshooting

### Common Issues

1. **"API key required" errors**: Ensure the appropriate API key file flags are provided and the files exist
2. **Model not found**: Check that the model name is in the supported lists
3. **Connection errors**: Verify network connectivity and proxy settings
4. **Invalid responses**: Enable debug logging to see raw requests/responses

### Debug Mode

Set log level for more verbose output:

```bash
# Enable debug logging
./bin/apiproxy -log-level="DEBUG" -openai-api-key-file="$HOME/.openai-key"

# JSON formatted logs for parsing
./bin/apiproxy -log-format="json" -log-level="DEBUG" -openai-api-key-file="$HOME/.openai-key"
```