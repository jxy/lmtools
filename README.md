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
echo "Hello world" | lmc -argo-user yourname -e

# Chat mode with automatic session creation
echo "What is LMC?" | lmc -argo-user yourname

# Streaming chat
echo "Tell me a story" | lmc -argo-user yourname -stream

# Use specific model
echo "Explain quantum computing" | lmc -argo-user yourname -model gpt4o

# Enable tool execution capabilities
echo "List files in current directory" | lmc -argo-user yourname -tool
```

### Session Management

```bash
# Show all conversation sessions
lmc -argo-user yourname -show-sessions

# Resume a previous session
echo "Continue from where we left off" | lmc -argo-user yourname -resume 0001

# Branch from a specific message
echo "Let me rephrase that" | lmc -argo-user yourname -branch 0001/0002

# Delete a session or message
lmc -argo-user yourname -delete 0001

# Show a specific conversation or message
lmc -argo-user yourname -show 0001
lmc -argo-user yourname -show 0001/0002
```

### Advanced Options

```bash
# Use custom sessions directory
echo "Test" | lmc -argo-user yourname -sessions-dir /tmp/my-sessions

# Custom environment/API endpoint
echo "Test" | lmc -argo-user yourname -argo-env prod
echo "Test" | lmc -argo-user yourname -argo-env https://custom.example/api

# Set system prompt
echo "Hello" | lmc -argo-user yourname -s "You are a helpful coding assistant"

# Disable session tracking
echo "One-off question" | lmc -argo-user yourname -no-session
```

### Tool Execution

```bash
# Enable tool execution with auto-approval for safe commands
echo "Show current directory" | lmc -argo-user yourname -tool -tool-auto-approve

# Use tool with custom timeout
echo "Run a long process" | lmc -argo-user yourname -tool -tool-timeout 5m

# Use tool with whitelist for auto-approval (each line must be a JSON array)
echo '["ls"]' > whitelist.txt
echo '["pwd"]' >> whitelist.txt
echo "List files" | lmc -argo-user yourname -tool -tool-whitelist whitelist.txt -tool-auto-approve
```

## Command-line Flags

### Required
- `-argo-user string`: User identifier (required for Argo provider)
- `-api-key-file string`: Path to API key file (required for non-Argo providers)

### Model Selection
- `-model string`: Model to use (defaults: gpt5 for Argo, gpt-5 for OpenAI, gemini-2.5-pro for Google, claude-opus-4-1-20250805 for Anthropic)
- `-e`: Enable embed mode for generating embeddings
- `-list-models`: List available models from provider

### Chat Options
- `-stream`: Use streaming chat mode for real-time responses
- `-s string`: System prompt for chat mode (default: "You are a brilliant assistant.")

### Tool Execution
- `-tool`: Enable built-in universal_command tool for system command execution
  - **Note**: Direct Google provider supports tool execution
  - **Note**: Google models accessed through Argo provider do not support tool execution (Argo limitation)
- `-tool-timeout duration`: Timeout for tool execution (default: 30s)
- `-tool-whitelist string`: Path to whitelist file where each line is a JSON array of command parts
- `-tool-blacklist string`: Path to blacklist file (commands always rejected)
- `-tool-auto-approve`: Skip manual approval for whitelisted commands
- `-tool-non-interactive`: Run in non-interactive mode (deny unapproved commands)
- `-tool-max-output-bytes int`: Maximum output size per tool execution (default: 1MB, max: 100MB)
- `-max-tool-rounds int`: Maximum rounds of tool execution (default: 32)
- `-max-tool-parallel int`: Maximum parallel tool executions (default: 4)

### Session Management
- `-resume string`: Resume session or branch by ID/path
- `-branch string`: Create branch from specific message ID
- `-show-sessions`: Display all conversation trees
- `-show string`: Show specific session or message by ID/path
- `-delete string`: Delete node (session/branch/message) and its descendants
- `-no-session`: Disable session creation (automatically set for embed mode)
- `-sessions-dir string`: Custom sessions directory (default: ~/.lmc/sessions)
- `-skip-flock-check`: Skip file locking check

### Provider Configuration
- `-provider string`: Provider: argo (default), openai, google, anthropic
- `-provider-url string`: Custom provider API endpoint
- `-argo-env string`: Environment for Argo - "prod", "dev", or custom URL (default: "dev")

### Other Options
- `-timeout duration`: HTTP request timeout (default: 10m)
- `-retries int`: Number of retry attempts for failed requests (default: 3)
- `-log-dir string`: Custom log directory (default: ~/.lmc/logs)
- `-log-level string`: Log level (DEBUG, INFO, WARN, ERROR) (default: INFO)

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
- **Automatic session forking**: When resuming a session with different system prompts:
  - Sessions fork whenever the effective system prompt differs from the original
  - This includes using `-tool` flag or explicit `-s` flag with different content
  - Original sessions are preserved unchanged
  - User is notified when forking occurs

### Session Atomicity

LMC ensures atomic session operations through careful file ordering:

- **Message files**: Each message consists of `.txt` (content), `.json` (metadata), and optionally `.tools.json` (tool interactions)
- **Atomic commit**: The JSON metadata file is written last, serving as the commit point
- **Consistency rule**: A message exists if and only if its JSON file exists
- **Rollback safety**: If interrupted, partial messages are automatically ignored
- **Tool persistence**: Tool calls and results are stored in `.tools.json` files alongside messages

#### The .json Commit Point Invariant

The `.json` file serves as the definitive commit point for all session operations:

- **Write order**: Files are always written in the order: `.txt` → `.tools.json` → `.json`
- **Existence check**: Only messages with a `.json` file are considered valid
- **Orphan cleanup**: On each commit, any `.txt` or `.tools.json` files without a corresponding `.json` file are automatically removed
- **Crash recovery**: This design ensures that interrupted operations leave no partial data
- **Atomic guarantee**: Either all files for a message exist (with `.json` present), or none are visible to the system

This invariant is enforced throughout the codebase and ensures data consistency even in the presence of crashes, concurrent access, or filesystem errors.

### Session Forking

When resuming a session with a different system prompt, LMC automatically creates a fork to preserve the original conversation:

- **Automatic detection**: Forks occur when the effective system prompt differs from the original
- **Common triggers**: Using `-tool` flag or explicit `-s` flag with different content
- **Full copy**: The entire conversation history is copied to the new fork
- **Performance considerations**: For large sessions (hundreds of messages), forking may take several seconds

**Important for large sessions**: Session forking creates a complete copy of the conversation history. For sessions with many messages or large content:
- Fork operations may take 1-10 seconds depending on session size
- Disk usage doubles for the forked portion
- Consider creating a new session instead if you don't need the full history
- Use `-no-session` for one-off queries that don't require context

### Tool Execution Behavior

When tools are enabled with `-tool`:

- **Approval flow**: Commands require approval based on whitelist/blacklist/auto-approve settings
- **Execution tracking**: All tool calls and results are persisted in session history
- **Continuation**: Tool execution can span multiple rounds (up to `-max-tool-rounds`)
- **Error handling**: Failed commands are recorded with error details
- **Output truncation**: Large outputs are truncated to prevent memory issues
- **Non-interactive mode**: Use `-tool-non-interactive` for scripted usage (requires whitelist or auto-approve)

#### Non-interactive Scripts

When running `lmc` as a background job or in environments with closed stdin (such as CI/CD pipelines), tool execution behaves differently:

- **EOF detection**: When stdin is closed or EOF is encountered, interactive approval prompts are not possible
- **Automatic denial**: Without `-tool-non-interactive`, commands will be denied when stdin is unavailable
- **Required flags**: For background/scripted usage, you must use `-tool-non-interactive` along with either:
  - `-tool-whitelist` with a file containing allowed commands
  - `-tool-auto-approve` to approve all non-blacklisted commands
- **Example for scripts**:
  ```bash
  # Create whitelist
  echo '["ls", "-la"]' > allowed.txt
  echo '["pwd"]' >> allowed.txt

  # Run in background with proper flags
  echo "List files and show working directory" | \
    lmc -argo-user bot -tool -tool-non-interactive \
    -tool-whitelist allowed.txt -tool-auto-approve &
  ```

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
gpt35, gpt35large, gpt4, gpt4large, gpt4turbo, gpt4o, gpt4olatest, gpto1mini, gpto3mini, gpto1, gpto3, gpto4mini, gpt41, gpt41mini, gpt41nano, gpt5, gpt5mini, gpt5nano, gemini25pro, gemini25flash, claudeopus4, claudesonnet4, claudesonnet37, claudesonnet35v2

### Embedding Models
ada002, v3large, v3small

---

# API Proxy - Anthropic to OpenAI/Google/Argo

The lmtools suite includes an API proxy that translates between Anthropic's Messages API format and OpenAI, Google AI, or Argo APIs.

## API Proxy Features

- **Protocol Translation**: Seamlessly converts between Anthropic, OpenAI, Google AI, and Argo API formats
- **Model Mapping**: Automatically maps Claude model names (haiku/sonnet) to configured models
- **Multi-Provider Support**: Route requests to OpenAI, Google AI, or Argo based on configuration
- **Pure Go**: No external dependencies, uses only Go standard library
- **Streaming Support**: Server-Sent Events for real-time responses
- **Token Counting**: Estimate token usage for requests

## API Proxy Quick Start

### Prerequisites

- Go 1.19 or later
- API keys for the providers you want to use:
  - OpenAI API key
  - Google AI Studio API key
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
-google-api-key-file       Path to file containing Google API key  
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
echo "AIza..." > ~/.google-key
chmod 600 ~/.openai-key ~/.google-key  # Secure the files

# Start with minimal configuration (OpenAI as default provider)
./bin/apiproxy -openai-api-key-file="$HOME/.openai-key"

# Use Google AI as provider
./bin/apiproxy -google-api-key-file="$HOME/.google-key" -provider="google"

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

### Supported Endpoints

The proxy supports both Anthropic and OpenAI API formats:

#### Anthropic-Compatible Endpoints
- `POST /v1/messages` - Chat completions (Anthropic format)
- `POST /v1/messages/count_tokens` - Token counting

#### OpenAI-Compatible Endpoints (NEW)
- `POST /v1/chat/completions` - Chat completions (OpenAI format)
- `GET /v1/models` - List available models

### With Claude Code

```bash
# Set the base URL to your proxy
export ANTHROPIC_BASE_URL=http://localhost:8082

# Use Claude Code normally
claude
```

### Direct API Calls

#### Anthropic Format

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

#### OpenAI Format

```bash
# Basic chat completion (OpenAI format)
curl -X POST http://localhost:8082/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "gpt-4",
    "messages": [
      {"role": "user", "content": "Hello, how are you?"}
    ],
    "max_tokens": 100
  }'

# With tools/functions
curl -X POST http://localhost:8082/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [
      {"role": "user", "content": "What is the weather in New York?"}
    ],
    "tools": [{
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "Get the weather for a location",
        "parameters": {
          "type": "object",
          "properties": {
            "location": {
              "type": "string",
              "description": "The city and state"
            }
          },
          "required": ["location"]
        }
      }
    }],
    "max_tokens": 100
  }'

# List available models
curl -X GET http://localhost:8082/v1/models
```

### Testing the API Proxy

A test script is available to verify the proxy is working correctly:

```bash
# Run the test script (requires proxy to be running on port 8082)
./scripts/test_apiproxy.sh
```

## Model Mapping

The proxy automatically maps model names to appropriate providers:

### Anthropic Models
| Anthropic Model | Mapped To | Provider |
|----------------|-----------|----------|
| *haiku* | `SMALL_MODEL` | Based on config |
| *sonnet*, *opus* | `BIG_MODEL` | Based on config |

### OpenAI Models
| OpenAI Model | Behavior |
|--------------|----------|
| gpt-4, gpt-4-turbo, gpt-5 | Pass-through when provider=openai |
| gpt-3.5-turbo, gpt-4-mini | Pass-through when provider=openai |
| o1-preview, o1-mini | Pass-through when provider=openai |

When using Argo provider, OpenAI model names are canonicalized:
- `gpt-5` → `gpt5`
- `gpt-4.1` → `gpt41`
- `gpt-4o` → `gpt4o`
| Other models | Direct passthrough | Auto-detected from provider model lists |

### Provider Selection Logic

Provider selection is simple and explicit:
- Requests are always routed to the provider set by the `-provider` flag (default: argo)
- If the selected provider lacks credentials (API key or ProviderURL), the request fails with an error
- No automatic fallback to other providers occurs

Model name mapping:
- Anthropic models (starting with "claude-"):
  - Models containing "haiku" → map to configured `SMALL_MODEL`
  - All other Claude models (opus, sonnet, etc.) → map to configured `BIG_MODEL`
- All non-Anthropic model names → pass through unchanged to the provider

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

### Use Google AI

```bash
# Save API key to file
echo "AIza..." > ~/.google-key
chmod 600 ~/.google-key

# Run proxy
./bin/apiproxy \
  -google-api-key-file="$HOME/.google-key" \
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

### Google AI Models
- gemini-2.5-pro-preview-03-25
- gemini-2.0-flash

### Argo Models
- All models from argolib including:
  - GPT variants: gpt35, gpt4, gpt4o, etc.
  - Google AI variants: gemini25pro, gemini25flash
  - Claude variants: claudeopus4, claudesonnet4, etc.

## API Proxy Architecture

The proxy is built with a modular architecture:

- **Configuration**: Command-line flag based configuration with validation
- **Model Mapper**: Handles model name mapping and provider selection
- **Converters**: Transform between API formats while preserving functionality
- **HTTP Server**: Routes requests to appropriate handlers
- **Streaming**: Server-Sent Events for real-time responses

## API Proxy Streaming

The proxy supports full Server-Sent Events (SSE) streaming for both Anthropic and OpenAI formats:

### Anthropic Format (`/v1/messages`)
- Native streaming for all providers
- Automatic format conversion from OpenAI/Google/Argo to Anthropic SSE
- Content blocks with text and tool use support
- Ping keep-alive events to prevent timeouts

### OpenAI Format (`/v1/chat/completions`)
- Native pass-through for OpenAI provider with minimal overhead
- Real-time conversion from Anthropic/Google/Argo streams
- Delta-based streaming with proper tool call support
- Automatic model name mapping and preservation
- Compatible with OpenAI SDK and tools expecting OpenAI SSE format

### Streaming Features
- Automatic ping keep-alive events to prevent timeouts
- Provider-specific response parsing and formatting
- Graceful fallback to simulated streaming when native streaming unavailable
- Tool call streaming with incremental argument updates
- Token usage reporting when available from provider

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