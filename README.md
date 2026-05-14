# lmtools

`lmtools` provides two binaries:

- `lmc`: a command-line client for chat, embeddings, tools, and local session history.
- `apiproxy`: an HTTP proxy that exposes Anthropic-compatible, OpenAI Chat
  Completions-compatible, and OpenAI Responses-compatible endpoints over
  Anthropic, OpenAI, Google, or Argo backends.

## Install

Requires Go 1.21 or later.

```bash
make build
```

This builds:

- `./bin/lmc`
- `./bin/apiproxy`

## lmc

### Common Usage

```bash
# Chat with the default Argo provider.
echo "Tell me a story" | ./bin/lmc -argo-user "$USER"

# Stream the answer.
echo "Tell me a story" | ./bin/lmc -argo-user "$USER" -stream

# Use OpenAI.
echo "Explain quantum computing" | ./bin/lmc \
  -provider openai \
  -api-key-file "$HOME/.openai-key" \
  -model gpt-5

# Use OpenAI's Responses API instead of Chat Completions.
echo "Return three bullet points" | ./bin/lmc \
  -provider openai \
  -api-key-file "$HOME/.openai-key" \
  -openai-responses

# Print the equivalent curl command without sending the request.
echo "Explain quantum computing" | ./bin/lmc \
  -provider openai \
  -api-key-file "$HOME/.openai-key" \
  -print-curl

# Generate embeddings. Session tracking is disabled automatically.
echo "Hello world" | ./bin/lmc -argo-user "$USER" -e

# Enable command tool use.
echo "List files in this directory" | ./bin/lmc \
  -argo-user "$USER" \
  -tool
```

### Sessions

```bash
# Show saved sessions.
./bin/lmc -argo-user "$USER" -show-sessions

# Resume a session or branch.
echo "Continue" | ./bin/lmc -argo-user "$USER" -resume 0001

# Branch from a message.
echo "Try a different answer" | ./bin/lmc -argo-user "$USER" -branch 0001/0002

# Show a session or message.
./bin/lmc -argo-user "$USER" -show 0001
./bin/lmc -argo-user "$USER" -show 0001/0002

# Delete a session, branch, or message and its descendants.
./bin/lmc -argo-user "$USER" -delete 0001

# Use a one-off request without writing session files.
echo "One-off question" | ./bin/lmc -argo-user "$USER" -no-session
```

### Tool Use

```bash
# Auto-approve whitelisted commands.
printf '["ls"]\n["pwd"]\n' > whitelist.txt
echo "Show the working directory and files" | ./bin/lmc \
  -argo-user "$USER" \
  -tool \
  -tool-whitelist whitelist.txt \
  -tool-auto-approve

# Non-interactive scripted usage.
echo "Run the allowed checks" | ./bin/lmc \
  -argo-user "$USER" \
  -tool \
  -tool-non-interactive \
  -tool-whitelist whitelist.txt \
  -tool-auto-approve
```

Commands are checked against the blacklist first, then the whitelist and approval
settings. Without `-tool-non-interactive`, `lmc` prompts before running commands
that are not already auto-approved.

### lmc Flags

Credentials:

- `-argo-user string`: Argo user or API key.
- `-api-key-file string`: API key file for OpenAI, Google, Anthropic, or Argo.

Provider and model:

- `-provider string`: `argo` (default), `openai`, `google`, or `anthropic`.
- `-model string`: Model name. Defaults depend on the provider.
- `-provider-url string`: Custom provider URL.
- `-list-models`: List models when the selected provider exposes a models endpoint.
- `-argo-dev`, `-argo-test`: Use the Argo dev or test host.
- `-argo-legacy`: Use Argo legacy chat endpoints.

Chat output:

- `-stream`: Stream chat responses.
- `-openai-responses`: With `-provider openai`, send chat through OpenAI `/v1/responses`.
- `-print-curl`: Print the equivalent `curl` command and exit without sending the request.
  With `-resume` or `-branch`, session history is read only and session files are not changed.
  With `-resume -tool`, pending tool calls are represented by placeholder results.
- `-s string`: System prompt.
- `-effort string`: Reasoning effort hint: `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`.
- `-json`: Request JSON object output.
- `-json-schema path`: Request schema-constrained JSON output.

Tools:

- `-tool`: Enable the built-in `universal_command` tool.
- `-tool-timeout duration`: Per-command timeout.
- `-tool-whitelist path`: Allowed commands, one command or JSON command array per line.
- `-tool-blacklist path`: Blocked commands.
- `-tool-auto-approve`: Skip prompts for whitelisted commands.
- `-tool-non-interactive`: Deny unapproved commands instead of prompting.
- `-max-tool-rounds int`: Maximum tool-call rounds.
- `-max-tool-parallel int`: Maximum concurrent tool executions.
- `-tool-max-output-bytes int`: Maximum captured output per tool execution.

Sessions and logging:

- `-resume string`, `-branch string`, `-show-sessions`, `-show string`, `-delete string`
- `-no-session`
- `-sessions-dir string`: Default `~/.lmc/sessions`.
- `-log-dir string`: Default `~/.lmc/logs`.
- `-log-level string`: `DEBUG`, `INFO`, `WARN`, or `ERROR`.
- `-timeout duration`
- `-retries int`
- `-skip-flock-check`

## apiproxy

`apiproxy` listens locally by default and translates request and response formats at the API boundary.

### Start The Proxy

```bash
# OpenAI backend.
echo "sk-..." > ~/.openai-key
chmod 600 ~/.openai-key
./bin/apiproxy -provider openai -api-key-file "$HOME/.openai-key"

# Anthropic backend.
echo "sk-ant-..." > ~/.anthropic-key
chmod 600 ~/.anthropic-key
./bin/apiproxy -provider anthropic -api-key-file "$HOME/.anthropic-key"

# Google backend.
echo "AIza..." > ~/.google-key
chmod 600 ~/.google-key
./bin/apiproxy -provider google -api-key-file "$HOME/.google-key"

# Argo backend.
./bin/apiproxy -provider argo -argo-user "$USER"

# Argo backend with client-visible model aliases.
./bin/apiproxy -provider argo -argo-user "$USER" \
  -model-map '^gpt-4o$=gpt5' \
  -model-map '^claude-.*=claude-opus-4-1-20250805'

# Listen on another host and port.
./bin/apiproxy -host 0.0.0.0 -port 8080 \
  -provider openai \
  -api-key-file "$HOME/.openai-key"
```

By default the server binds to `127.0.0.1:8082`. Use `-host 0.0.0.0` only when you intend to expose it beyond localhost.

### Codex With Argo

Codex can use Argo through `apiproxy` by configuring a local OpenAI-compatible provider. Add a provider and profile to `.codex/config.toml`:

```toml
[model_providers.apiproxy]
name = "apiproxy to argo"
base_url = "http://127.0.0.1:8082/v1"

[profiles.apiproxy]
model_provider = "apiproxy"
```

Then start `apiproxy` with a model map from the Codex-requested model name to the Argo backend model:

```bash
./bin/apiproxy -provider argo -argo-user "$USER" \
  -model-map '^gpt-5\.5$=gpt55'
```

Run Codex with the `apiproxy` profile. Requests for `gpt-5.5` are sent to Argo as `gpt55`.

### apiproxy Flags

- `-provider string`: `argo` (default), `anthropic`, `openai`, or `google`.
- `-api-key-file string`: API key file for the selected provider.
- `-argo-user string`: Argo user or API key when using Argo.
- `-provider-url string`: Custom provider URL.
- `-model-map REGEX=MODEL_NAME`: Map matching request models to a backend model.
  Can be repeated; the first matching rule wins.
- `-argo-dev`, `-argo-test`, `-argo-legacy`
- `-host string`: Bind host. Default `127.0.0.1`.
- `-port int`: Bind port. Default `8082`.
- `-sessions-dir string`: Local Responses API state directory. Default `~/.apiproxy/sessions`.
- `-max-request-body-size int`: Request body limit in MB. Default `10`.
- `-log-level string`: `DEBUG`, `INFO`, `WARN`, or `ERROR`.
- `-log-format string`: `text` or `json`.

### Supported Endpoints

Anthropic-compatible:

- `POST /v1/messages`
- `POST /v1/messages/count_tokens`

OpenAI-compatible:

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/responses/input_tokens`
- `POST /v1/responses/compact`
- `GET /v1/responses/{id}`
- `POST /v1/responses/{id}/cancel`
- `DELETE /v1/responses/{id}`
- `GET /v1/responses/{id}/input_items`
- `POST /v1/conversations`
- `GET /v1/conversations/{id}`
- `POST /v1/conversations/{id}`
- `DELETE /v1/conversations/{id}`
- `GET /v1/conversations/{id}/items`
- `POST /v1/conversations/{id}/items`
- `GET /v1/conversations/{id}/items/{item_id}`
- `DELETE /v1/conversations/{id}/items/{item_id}`
- `GET /v1/models`

### Example API Calls

Anthropic Messages:

```bash
curl http://localhost:8082/v1/messages \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-haiku-20240307",
    "max_tokens": 1000,
    "messages": [
      {"role": "user", "content": "Hello"}
    ]
  }'
```

OpenAI Chat Completions:

```bash
curl http://localhost:8082/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5",
    "messages": [
      {"role": "user", "content": "Hello"}
    ]
  }'
```

OpenAI Responses:

```bash
curl http://localhost:8082/v1/responses \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5",
    "input": "Summarize this in one sentence."
  }'
```

Responses with a function tool:

```bash
curl http://localhost:8082/v1/responses \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5",
    "input": "What is the weather in Chicago?",
    "tools": [{
      "type": "function",
      "name": "get_weather",
      "description": "Get weather for a location",
      "parameters": {
        "type": "object",
        "properties": {
          "location": {"type": "string"}
        },
        "required": ["location"]
      }
    }]
  }'
```

### Using With Claude Code

```bash
export ANTHROPIC_BASE_URL=http://localhost:8082
claude
```

## Provider Routing

The selected `-provider` controls the backend. The proxy does not fall back to another provider automatically.

Model names are mapped before forwarding only when `-model-map` rules are configured:

- Each `-model-map REGEX=MODEL_NAME` rule matches against the client-requested model name.
- Rules are evaluated in command-line order, and the first match wins.
- If no rule matches, the requested model name is forwarded unchanged.

Example:

```bash
./bin/apiproxy -provider argo -argo-user "$USER" \
  -model-map '^gpt-4o-mini$=gpt5mini' \
  -model-map '^gpt-4o$=gpt5' \
  -model-map '^claude-3-haiku.*=claude-3-haiku-20240307' \
  -model-map '^claude-.*=claude-opus-4-1-20250805'
```

With `-provider argo` in native mode:

- Backend models starting with `claude` use Argo's Anthropic-compatible `/v1/messages` wire format.
- All other backend models use Argo's OpenAI-compatible `/v1/chat/completions` wire format.
- `-argo-legacy` forces the older Argo chat and streamchat endpoints.

## Responses API Compatibility

`apiproxy` supports `/v1/responses` in two modes.

### Direct OpenAI Backend

When started with `-provider openai`, `/v1/responses` requests are forwarded to
OpenAI's official Responses API backend after model mapping. This is the
highest-fidelity mode.

In this mode:

- OpenAI Responses request bodies are preserved, including valid OpenAI fields the compatibility layer does not understand.
- Non-stream and stream responses are passed back in OpenAI Responses format.
- Responses lifecycle and Conversations API calls are forwarded upstream.
- Returned model names are rewritten only where the proxy has enough request context to restore the client-visible model alias.

### Converted Backends

When started with `-provider anthropic`, `-provider google`, or `-provider argo`
for a model that is not using OpenAI Responses upstream, the proxy converts the
request into the closest portable backend call and synthesizes a Responses-shaped
result.

Request conversion:

- `input` strings become one user message.
- `input` arrays preserve message, function call, custom tool call, tool-result,
  reasoning, and compaction items where the target path has an equivalent.
- `instructions` become provider instruction text.
- `max_output_tokens`, `temperature`, `top_p`, `metadata`, `service_tier`, and
  `stream` map to the portable provider request where supported.
- `text.format` with `json_object` or `json_schema` maps to the existing JSON output controls.
- `reasoning.effort` maps to provider reasoning controls where supported.
- Function tools and custom tools map to portable tool definitions. Namespace
  tools are flattened for providers that require flat tool names and are
  restored in Responses output when possible.

Backend wire format:

- Anthropic backends receive Anthropic Messages API requests.
- Argo Claude-routed models receive Argo's Anthropic-compatible Messages requests.
- Argo non-Claude models receive Argo's OpenAI-compatible Chat Completions requests.
- Google backends receive Gemini requests through the proxy's compatibility converter.

Response conversion:

- Provider text becomes Responses `output` message content.
- Anthropic `tool_use` blocks and OpenAI Chat Completions `tool_calls` become Responses `function_call` or `custom_tool_call` output items.
- Tool-call arguments are preserved as JSON strings when possible.
- Anthropic thinking blocks become Responses `reasoning` output items with summary text.
- Usage is mapped when the backend reports usage.
- Converted responses store local state by default, so `previous_response_id`,
  response retrieval, input item listing, and conversation endpoints can work
  without OpenAI as the backend.

Streaming conversion:

- Direct OpenAI Responses streams are passed through.
- Converted streams emit Responses SSE events synthesized from upstream Anthropic, OpenAI Chat Completions, Google, or Argo stream chunks.
- Legacy Argo mode may simulate streaming from a non-streaming upstream response.

### Converted Responses Limitations

Converted Responses support is a compatibility layer, not a full replacement for OpenAI's official Responses API backend.

Known limitations when `-provider` is not `openai`:

- OpenAI prompt templates (`prompt`) are rejected because they do not have a portable provider representation.
- OpenAI-hosted tools such as web search and file search are not run by the
  proxy. Unsupported tool types are logged and dropped on converted paths.
- Custom tools are represented, but target providers may not enforce OpenAI custom-tool grammar or validation semantics.
- Some OpenAI-only controls have no portable effect, including
  `max_tool_calls`, `parallel_tool_calls`, `include`, `truncation`,
  `top_logprobs`, `prompt_cache_key`, `text.verbosity`, and
  `reasoning.summary`.
- Output images, files, audio, annotations, and logprobs are not synthesized by the converted response path.
- Local response and conversation state lives under `~/.apiproxy/sessions` or `-sessions-dir`; it is not OpenAI-hosted state.
- `store:false` disables local persistence for foreground converted requests, so
  response retrieval and `previous_response_id` are unavailable for those
  responses.
- Background execution, cancellation, compaction, and token counts are best-effort local compatibility features.
- Converted SSE event streams expose the common Responses event shape but do not
  include every event type or metadata field produced by OpenAI.

## Token Counting

- `/v1/messages/count_tokens` uses Anthropic count tokens for Anthropic and Argo Claude-routed models.
- Google uses Gemini token counting where available.
- Non-Claude Argo models, legacy Argo mode, OpenAI, and unsupported routes use local estimation.
- `/v1/responses/input_tokens` resolves local Responses state first on converted
  provider paths, then uses the same provider counting or estimation behavior.

## Data Locations

- `lmc` sessions: `~/.lmc/sessions`
- `lmc` logs: `~/.lmc/logs`
- `apiproxy` Responses state: `~/.apiproxy/sessions`
- Built binaries: `./bin/lmc` and `./bin/apiproxy`

## Troubleshooting

- Use `-log-level DEBUG` on either binary to inspect request routing and conversion warnings.
- Confirm that the selected `-provider` has credentials or a `-provider-url`.
- For Argo, verify whether native mode or `-argo-legacy` matches the model and endpoint you intend to use.
- If a converted Responses request loses an OpenAI-only field, use `-provider openai` for direct official Responses API passthrough.
