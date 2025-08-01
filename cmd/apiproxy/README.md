# API Proxy - Anthropic to OpenAI/Google/Argo

A pure Go implementation of an API proxy that translates between Anthropic's Messages API format and OpenAI, Google Gemini, or Argo APIs.

## Features

- **Protocol Translation**: Seamlessly converts between Anthropic, OpenAI, Google Gemini, and Argo API formats
- **Model Mapping**: Automatically maps Claude model names (haiku/sonnet) to configured models
- **Multi-Provider Support**: Route requests to OpenAI, Google Gemini, or Argo based on configuration
- **Pure Go**: No external dependencies, uses only Go standard library
- **Streaming Support**: Server-Sent Events for real-time responses
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

Configure the proxy using command-line flags:

```bash
# API Key configuration (pass files containing keys, not the keys directly)
--openai-api-key-file       Path to file containing OpenAI API key
--gemini-api-key-file       Path to file containing Gemini API key  
--anthropic-api-key-file    Path to file containing Anthropic API key

# Other configuration flags
--argo-user            Argo user
--argo-env             Argo environment (default: "dev")
--preferred-provider   Preferred provider: openai, google, argo (default: "argo")
--provider-url         Custom URL for the selected provider (overrides default)
--big-model            Model for "sonnet" requests (default: "claudeopus4")
--small-model          Model for "haiku" requests (default: "claudesonnet4")

# Server configuration
--host                 Host to bind (default: "127.0.0.1")
--port                 Port to bind (default: 8082)

# Other options
--max-request-body-size   Maximum request body size in MB (default: 10)
--log-level              Log level: DEBUG, INFO, WARN, ERROR (default: "INFO")
--log-format             Log format: text, json (default: "text")
--no-color               Disable colored output
```

### Running the Proxy

```bash
# First, save your API keys to files (one key per file)
echo "sk-..." > ~/.openai-key
echo "AIza..." > ~/.gemini-key
chmod 600 ~/.openai-key ~/.gemini-key  # Secure the files

# Start with minimal configuration (OpenAI as default provider)
./apiproxy --openai-api-key-file="$HOME/.openai-key"

# Use Google Gemini as provider
./apiproxy --gemini-api-key-file="$HOME/.gemini-key" --preferred-provider="google"

# Use Argo with custom models
./apiproxy --argo-user="username" --preferred-provider="argo" \
  --big-model="claudesonnet4" --small-model="gemini25flash"

# Custom host and port with debug logging
./apiproxy --host="0.0.0.0" --port="8080" --log-level="DEBUG" \
  --openai-api-key-file="$HOME/.openai-key"

# Note: By default, the server binds to 127.0.0.1 (localhost only).
# Use --host="0.0.0.0" to allow external connections.

# Use custom provider URLs
./apiproxy --openai-api-key-file="$HOME/.openai-key" --preferred-provider="openai" \
  --provider-url="https://custom-openai-proxy.com/v1/chat/completions"

./apiproxy --gemini-api-key-file="$HOME/.gemini-key" --preferred-provider="google" \
  --provider-url="https://custom-gemini-endpoint.com/v1beta/models"

./apiproxy --argo-user="username" --preferred-provider="argo" \
  --provider-url="https://custom-argo-server.com/api/v1/resource"
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

When using the default models (`claudeopus4` and `claudesonnet4`), the proxy automatically selects provider-specific models based on your `--preferred-provider`:

| Provider | Big Model (sonnet) | Small Model (haiku) |
|----------|-------------------|---------------------|
| argo (default) | claudeopus4 | claudesonnet4 |
| openai | o3-mini | gpt-4o-mini |
| google | gemini-2.5-pro-preview-03-25 | gemini-2.0-flash |

**Note**: These automatic mappings only apply when using the default model values. If you specify custom models via `--big-model` or `--small-model`, those will be used exactly as specified.

## Configuration Examples

### Use Argo (Default)

```bash
# Run proxy with Argo (no API key file needed, just username)
./apiproxy \
  --argo-user="username" \
  --argo-env="prod"
# Uses default models: claudeopus4 for big, claudesonnet4 for small
```

### Use OpenAI

```bash
# Save API key to file
echo "sk-..." > ~/.openai-key
chmod 600 ~/.openai-key

# Run proxy
./apiproxy \
  --openai-api-key-file="$HOME/.openai-key" \
  --preferred-provider="openai" \
  --big-model="gpt-4o" \
  --small-model="gpt-4o-mini"
```

### Use Google Gemini

```bash
# Save API key to file
echo "AIza..." > ~/.gemini-key
chmod 600 ~/.gemini-key

# Run proxy
./apiproxy \
  --gemini-api-key-file="$HOME/.gemini-key" \
  --preferred-provider="google" \
  --big-model="gemini-2.5-pro-preview-03-25" \
  --small-model="gemini-2.0-flash"
```


## Supported Models

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

## Architecture

The proxy is built with a modular architecture:

- **Configuration**: Command-line flag based configuration with validation
- **Model Mapper**: Handles model name mapping and provider selection
- **Converters**: Transform between API formats while preserving functionality
- **HTTP Server**: Routes requests to appropriate handlers
- **Streaming**: Server-Sent Events for real-time responses

## Streaming

The proxy supports full Server-Sent Events (SSE) streaming:
- Native streaming for OpenAI and Gemini providers
- Simulated streaming for Argo (converts non-streaming responses)
- Automatic ping keep-alive events to prevent timeouts
- Provider-specific response parsing and formatting

## Limitations

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

1. **"API key required" errors**: Ensure the appropriate API key file flags are provided and the files exist
2. **Model not found**: Check that the model name is in the supported lists
3. **Connection errors**: Verify network connectivity and proxy settings
4. **Invalid responses**: Enable debug logging to see raw requests/responses

### Debug Mode

Set log level for more verbose output:

```bash
# Enable debug logging
./apiproxy --log-level="DEBUG" --openai-api-key-file="$HOME/.openai-key"

# JSON formatted logs for parsing
./apiproxy --log-format="json" --log-level="DEBUG" --openai-api-key-file="$HOME/.openai-key"
```

## License

This project is part of the lmtools suite and follows the same licensing terms.