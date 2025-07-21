# API Proxy - Anthropic to OpenAI/Google/Argo

A pure Go implementation of an API proxy that translates between Anthropic's Messages API format and OpenAI, Google Gemini, or Argo APIs.

## Features

- **Protocol Translation**: Seamlessly converts between Anthropic, OpenAI, Google Gemini, and Argo API formats
- **Model Mapping**: Automatically maps Claude model names (haiku/sonnet) to configured models
- **Multi-Provider Support**: Route requests to OpenAI, Google Gemini, or Argo based on configuration
- **Pure Go**: No external dependencies, uses only Go standard library
- **Streaming Support**: Planned support for Server-Sent Events streaming
- **Token Counting**: Estimate token usage for requests

## Quick Start

### Prerequisites

- Go 1.19 or later
- API keys for the providers you want to use:
  - OpenAI API key
  - Google AI Studio (Gemini) API key
  - Argo user credentials

### Installation

```bash
# Build the proxy
go build -o apiproxy ./cmd/apiproxy

# Or install directly
go install ./cmd/apiproxy
```

### Configuration

Set environment variables for the providers you want to use:

```bash
# API Keys
export OPENAI_API_KEY="sk-..."
export GEMINI_API_KEY="..."
export ANTHROPIC_API_KEY="..." # Only needed if proxying TO Anthropic

# Argo Configuration
export ARGO_USER="your-username"
export ARGO_ENV="prod" # or "dev" or custom URL

# Provider Configuration
export PREFERRED_PROVIDER="openai" # Options: openai, google, argo
export BIG_MODEL="gpt-4.1"         # Model for "sonnet" requests
export SMALL_MODEL="gpt-4.1-mini"  # Model for "haiku" requests
```

### Running the Proxy

```bash
# Start with default settings (port 8082)
./apiproxy

# Custom host and port
./apiproxy -host 0.0.0.0 -port 8080
```

## Usage Examples

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

## Model Mapping

The proxy automatically maps Anthropic model names to appropriate providers:

| Anthropic Model | Mapped To | Provider |
|----------------|-----------|----------|
| *haiku* | `SMALL_MODEL` | Based on model/config |
| *sonnet* | `BIG_MODEL` | Based on model/config |
| Other models | Direct mapping | Auto-detected |

### Provider Selection Logic

1. If model contains "haiku" → maps to `SMALL_MODEL`
2. If model contains "sonnet" → maps to `BIG_MODEL`
3. Otherwise, checks if model exists in known model lists
4. Provider is determined by:
   - Model's presence in provider-specific lists
   - `PREFERRED_PROVIDER` setting
   - Availability of API keys

## Configuration Examples

### Use OpenAI (Default)

```bash
export OPENAI_API_KEY="sk-..."
export PREFERRED_PROVIDER="openai"
export BIG_MODEL="gpt-4o"
export SMALL_MODEL="gpt-4o-mini"
```

### Use Google Gemini

```bash
export GEMINI_API_KEY="..."
export PREFERRED_PROVIDER="google"
export BIG_MODEL="gemini-2.5-pro-preview-03-25"
export SMALL_MODEL="gemini-2.0-flash"
```

### Use Argo

```bash
export ARGO_USER="username"
export ARGO_ENV="prod"
export PREFERRED_PROVIDER="argo"
export BIG_MODEL="claudesonnet4"
export SMALL_MODEL="gemini25flash"
```

## Supported Models

### OpenAI Models
- o3-mini, o1, o1-mini, o1-pro
- gpt-4.5-preview, gpt-4o, gpt-4o-mini
- gpt-4.1, gpt-4.1-mini

### Google Gemini Models
- gemini-2.5-pro-preview-03-25
- gemini-2.0-flash

### Argo Models
- All models from argolib including:
  - GPT variants: gpt35, gpt4, gpt4o, etc.
  - Gemini variants: gemini25pro, gemini25flash
  - Claude variants: claudeopus4, claudesonnet4, etc.

## Architecture

The proxy is built with a modular architecture:

- **Configuration**: Environment-based configuration with validation
- **Model Mapper**: Handles model name mapping and provider selection
- **Converters**: Transform between API formats while preserving functionality
- **HTTP Server**: Routes requests to appropriate handlers
- **Streaming**: (Planned) Server-Sent Events for real-time responses

## Limitations

- Streaming support is not yet implemented
- Token counting is estimated, not exact
- Some advanced features may not be fully supported across all providers
- Error messages from providers are passed through with minimal modification

## Development

```bash
# Run tests
go test ./...

# Build for different platforms
GOOS=linux GOARCH=amd64 go build -o apiproxy-linux ./cmd/apiproxy
GOOS=darwin GOARCH=amd64 go build -o apiproxy-mac ./cmd/apiproxy
GOOS=windows GOARCH=amd64 go build -o apiproxy.exe ./cmd/apiproxy
```

## Troubleshooting

### Common Issues

1. **"API key required" errors**: Ensure the appropriate environment variables are set
2. **Model not found**: Check that the model name is in the supported lists
3. **Connection errors**: Verify network connectivity and proxy settings
4. **Invalid responses**: Enable debug logging to see raw requests/responses

### Debug Mode

Set log level for more verbose output:

```bash
# Coming soon: debug logging support
```

## License

This project is part of the lmtools suite and follows the same licensing terms.