# lmtools

`lmtools` builds two binaries:

- `lmc`: a command-line client for chat, embeddings, tool calls, and local session history.
- `apiproxy`: an HTTP proxy that serves Anthropic-compatible, OpenAI Chat
  Completions-compatible, and OpenAI Responses-compatible endpoints over
  Anthropic, OpenAI, Google, or Argo backends.

**Disclaimer**: LLMs (GPT, Claude, Gemini, and others) "vibe-coded" almost all
code in this repository. Use it at your own risk and review it before you deploy
to production.

## Install

Requires Go 1.21.13 or later.

```bash
make build
```

`make build` writes `./bin/lmc` and `./bin/apiproxy`. Set `GOOS` and `GOARCH` to
cross-compile; those builds land in `./bin/<goos>-<goarch>/`.

## lmc

For chat and embeddings, `lmc` reads the input from stdin and writes the result
to stdout.

### Chat

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

# Use Argo's Responses API, which serves gpt* models.
echo "Return three bullet points" | ./bin/lmc \
  -argo-user "$USER" \
  -model gpt5 \
  -openai-responses

# Print the equivalent curl command instead of sending the request.
echo "Explain quantum computing" | ./bin/lmc \
  -provider openai \
  -api-key-file "$HOME/.openai-key" \
  -print-curl

# Generate embeddings. Embed mode turns off session tracking.
echo "Hello world" | ./bin/lmc -argo-user "$USER" -e
```

### Sessions

`lmc` stores each exchange in a session tree under `~/.lmc/sessions`. A path
addresses either a session (`0001`) or one message inside it (`0001/0002`).

```bash
# List saved sessions.
./bin/lmc -show-sessions

# Show a session or a single message.
./bin/lmc -show 0001
./bin/lmc -show 0001/0002

# Continue a session.
echo "Continue" | ./bin/lmc -argo-user "$USER" -resume 0001

# Branch from a message.
echo "Try a different answer" | ./bin/lmc -argo-user "$USER" -branch 0001/0002

# Delete a session, branch, or message together with its descendants.
./bin/lmc -delete 0001

# Answer once without writing session files.
echo "One-off question" | ./bin/lmc -argo-user "$USER" -no-session
```

`-show-sessions`, `-show`, and `-delete` touch only local files, so they need no
provider credentials.

### Tool Use

`-tool` enables the built-in `universal_command` tool, which runs the shell
commands the model asks for. `lmc` checks each command against the blacklist
first, then the whitelist and the approval settings. Without
`-tool-non-interactive`, `lmc` prompts before it runs a command that is not
already approved. `-tool-non-interactive` requires `-tool-auto-approve` or
`-tool-whitelist`.

```bash
# Run whitelisted commands without prompting.
printf '["ls"]\n["pwd"]\n' > whitelist.txt
echo "Show the working directory and files" | ./bin/lmc \
  -argo-user "$USER" \
  -tool \
  -tool-whitelist whitelist.txt \
  -tool-auto-approve

# Deny everything unapproved instead of prompting, for scripts.
echo "Run the allowed checks" | ./bin/lmc \
  -argo-user "$USER" \
  -tool \
  -tool-non-interactive \
  -tool-whitelist whitelist.txt \
  -tool-auto-approve
```

### lmc Flags

Provider and credentials:

- `-provider string`: `argo` (default), `openai`, `google`, or `anthropic`.
- `-argo-user string`: Argo user or API key.
- `-api-key-file string`: API key file for OpenAI, Google, Anthropic, or Argo.
- `-provider-url string`: Custom provider URL.
- `-argo-dev`, `-argo-test`: Use the Argo dev or test host.
- `-argo-legacy`: Use the Argo legacy chat endpoints.

Mode and model:

- `-model string`: Model name. The default depends on the provider.
- `-e`: Generate embeddings instead of chat. Embed mode turns off sessions and
  rejects `-stream`, `-tool`, `-resume`, `-branch`, `-openai-responses`, and the
  output controls.
- `-list-models`: List the models the provider reports.
- `-stream`: Stream the chat response.
- `-openai-responses`: Send the chat through the provider's Responses API
  instead of chat completions. Requires `-provider openai` or `-provider argo`,
  and does not combine with `-argo-legacy`. On Argo it applies to `gpt*` models,
  which are the models Argo serves at `/v1/responses`; other Argo models keep
  their usual routing.
- `-print-curl`: Print the equivalent `curl` command and exit without sending
  the request. With `-resume` or `-branch`, `lmc` reads session history and
  writes nothing back. With `-resume -tool`, it substitutes placeholder results
  for pending tool calls.

Output controls:

- `-s string`: System prompt.
- `-effort string`: Reasoning effort hint: `none`, `minimal`, `low`, `medium`,
  `high`, `xhigh`, or `max`.
- `-reasoning-mode string`: Responses API reasoning mode: `standard` or `pro`
  (`pro` applies to GPT-5.6 only). `lmc` emits it only on an effective
  `-openai-responses` path and otherwise warns and ignores it.
- `-reasoning-context string`: Responses API reasoning context: `auto`,
  `current_turn`, or `all_turns`. Same `-openai-responses` restriction as
  `-reasoning-mode`.
- `-max-tokens int`: Maximum output tokens. `0` (the default) uses the provider
  default. Anthropic-wire requests — the `anthropic` provider and Argo `claude*`
  models — always send a limit: `128000` for Opus models, `64000` for other
  Claude models.
- `-json`: Request JSON object output.
- `-json-schema path`: Request schema-constrained JSON output. Does not combine with `-json`.

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

Sessions:

- `-resume string`, `-branch string`, `-show-sessions`, `-show string`, `-delete string`
- `-no-session`: Send the request without writing session files.
- `-sessions-dir string`: Default `~/.lmc/sessions`.
- `-skip-flock-check`: Skip the session file lock check.

Requests and logging:

- `-timeout duration`, `-retries int`
- `-log-dir string`: Default `~/.lmc/logs`.
- `-log-level string`: `DEBUG`, `INFO`, `WARN`, or `ERROR`.

## apiproxy

`apiproxy` binds `127.0.0.1:8082` and translates request and response formats at
the API boundary. Pass `-host 0.0.0.0` only when you intend to reach it from
beyond localhost.

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

# Listen on another host and port.
./bin/apiproxy -host 0.0.0.0 -port 8080 \
  -provider openai \
  -api-key-file "$HOME/.openai-key"
```

### apiproxy Flags

- `-provider string`: `argo` (default), `anthropic`, `openai`, or `google`.
- `-api-key-file string`: API key file for the selected provider.
- `-argo-user string`: Argo user or API key when using Argo.
- `-provider-url string`: Custom provider URL.
- `-model-map REGEX=MODEL_NAME`: Map matching request models to a backend model.
  Repeat it to add rules; the first match wins.
- `-argo-dev`, `-argo-test`, `-argo-legacy`
- `-openai-responses`: Forward `/v1/responses` to Argo's own Responses API for
  `gpt*` models instead of converting it. Requires `-provider openai` or
  `-provider argo`, and does not combine with `-argo-legacy`. `-provider openai`
  forwards `/v1/responses` upstream with or without this flag.
- `-host string`: Bind host. Default `127.0.0.1`.
- `-port int`: Bind port. Default `8082`.
- `-sessions-dir string`: Local Responses API state directory. Default `~/.apiproxy/sessions`.
- `-max-request-body-size int`: Request body limit in MB. Default `512`, matching
  OpenAI's documented payload limit so the proxy never rejects a request the
  provider would have accepted. Anthropic caps Messages requests at 32MB and
  Argo enforces its own limit; those rejections pass through untouched. Requests
  over the proxy's own limit get `413 request_too_large`; when `Content-Length`
  is known, the message includes the observed size and exact whole-MB setting.
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

Health check:

- `GET /` returns `{"status":"ok","name":"lmtools-proxy"}`.

Streaming `/v1/messages` responses emit Anthropic-format SSE. Once the upstream
streaming request returns HTTP 200, `apiproxy` sends an `event: ping` keepalive
whenever it has written no downstream SSE event for 15 seconds.

`apiproxy` sets no absolute timeout on outbound provider requests, but its HTTP
server applies 15-minute read and write timeouts, which bound a single request.
A request also ends when the client disconnects, the request context is
cancelled, the provider closes the connection, or an external proxy or client
timeout fires.

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

### Using With Codex

Codex reaches any `apiproxy` backend through a custom model provider. Since
Codex 0.134.0 a profile lives in its own file beside `config.toml` rather than
in a `[profiles.NAME]` table inside it, so the setup takes two files.

Define the provider in `~/.codex/config.toml`. It must be user-level: a
project-local `.codex/config.toml` ignores `model_provider` and `model_providers`
and warns at startup.

```toml
[model_providers.apiproxy]
name = "apiproxy"
base_url = "http://127.0.0.1:8082/v1"
```

Put the profile in `~/.codex/apiproxy.config.toml`, using top-level keys rather
than a `[profiles.apiproxy]` table:

```toml
model = "gpt-5.5"
model_provider = "apiproxy"
```

Start the proxy with a model map from the Codex-requested model name to the
backend model:

```bash
./bin/apiproxy -provider argo -argo-user "$USER" \
  -model-map '^gpt-5\.5$=gpt55'
```

Then run Codex with the profile:

```bash
codex --profile apiproxy
codex exec --profile apiproxy "summarize this repository"
```

The proxy now sends `gpt-5.5` requests to Argo as `gpt55`. Codex speaks the
Responses API (`wire_api` defaults to `responses` and takes no other value), so
its traffic arrives at `POST /v1/responses`. Against Argo that is the converted
path, with the [limitations](#converted-responses-limitations) that implies.

`apiproxy` authenticates to the backend with its own credentials and ignores
client ones, so `env_key` is unnecessary; if your Codex version requires it,
point it at any variable holding a placeholder value. The same two files work
against any `-provider`.

## Provider Routing

`-provider` selects the backend. `apiproxy` never falls back to another provider.

`apiproxy` rewrites model names before forwarding only when you configure
`-model-map` rules:

- Each `-model-map REGEX=MODEL_NAME` rule matches the client-requested model name.
- The proxy evaluates rules in command-line order and stops at the first match.
- When no rule matches, the requested model name goes upstream unchanged.

```bash
./bin/apiproxy -provider argo -argo-user "$USER" \
  -model-map '^gpt-4o-mini$=gpt5mini' \
  -model-map '^gpt-4o$=gpt5' \
  -model-map '^claude-3-haiku.*=claude-3-haiku-20240307' \
  -model-map '^claude-.*=claude-opus-4-1-20250805'
```

Both binaries route native Argo requests by the prefix of the model that reaches
Argo, which for `apiproxy` is the model after `-model-map`:

- Models starting with `claude` use Argo's Anthropic-compatible `/v1/messages` wire format.
- All other models use Argo's OpenAI-compatible `/v1/chat/completions` wire format.
- `-argo-legacy` forces the older Argo chat and streamchat endpoints.

## Responses API Compatibility

`apiproxy` serves `/v1/responses` two ways: it passes the request through to a
backend that speaks the Responses API, or it converts the request to another
wire format.

### Direct OpenAI Backend

With `-provider openai`, the proxy maps the model and forwards `/v1/responses`
to OpenAI's own Responses API. In this mode it:

- Preserves OpenAI Responses request bodies, including valid OpenAI fields the compatibility layer does not understand.
- Returns non-stream and stream responses in OpenAI Responses format.
- Forwards Responses lifecycle and Conversations API calls upstream.
- Rewrites returned model names only where it holds enough request context to restore the client-visible alias.

### Direct Argo Backend

Argo serves an OpenAI-compatible Responses API at
`<argo-host>/argoapi/v1/responses` for its `gpt*` models. That backend is still
experimental, so the proxy uses it only when you pass `-openai-responses`:

```bash
./bin/apiproxy -provider argo -argo-user "$USER" -openai-responses
```

In this mode the proxy:

- Forwards `/v1/responses` requests for `gpt*` backend models to Argo with the
  request body preserved byte-for-byte, apart from `-model-map` rewriting, and
  returns Argo's response body unchanged apart from restoring the client-visible
  model alias. Streams pass through the same way.
- Still converts requests for every other Argo model, so one proxy serves both
  paths. `-openai-responses` does not combine with `-argo-legacy`.
- Resolves lifecycle routes (`GET`/`DELETE` `/v1/responses/{id}`, `cancel`,
  `input_items`) and Conversations routes against local state first, then falls
  back to Argo, so converted responses stay retrievable while passed-through IDs
  resolve upstream. `POST /v1/conversations` carries no ID to look up, so it goes
  straight to Argo.
- Forwards `/v1/responses/input_tokens` and `/v1/responses/compact` to Argo for
  `gpt*` models and converts them otherwise.

### Converted Backends

With `-provider anthropic`, `-provider google`, or `-provider argo` on a model
that does not use OpenAI Responses upstream, the proxy rewrites the request as a
portable backend call and builds a Responses-shaped result from the reply.

Request conversion:

- `input` strings become one user message.
- `input` arrays keep message, function call, custom tool call, tool-result,
  reasoning, and compaction items wherever the target path has an equivalent.
- `instructions` become provider instruction text.
- `max_output_tokens`, `temperature`, `top_p`, `metadata`, `service_tier`, and
  `stream` map to the portable provider request where the provider supports them.
- `text.format` with `json_object` or `json_schema` maps to the existing JSON output controls.
- `reasoning.effort` maps to provider reasoning controls where they exist.
- Function tools and custom tools map to portable tool definitions. The proxy
  flattens namespace tools for providers that require flat tool names and
  restores them in Responses output where it can.

Backend wire format:

- Anthropic backends receive Anthropic Messages API requests.
- Google backends receive Gemini requests through the proxy's compatibility converter.
- Argo backends follow the prefix routing under
  [Provider Routing](#provider-routing): Messages requests for Claude-routed
  models, Chat Completions requests for the rest.

Response conversion:

- Provider text becomes Responses `output` message content.
- Anthropic `tool_use` blocks and OpenAI Chat Completions `tool_calls` become
  Responses `function_call` or `custom_tool_call` output items, keeping tool-call
  arguments as JSON strings where possible.
- Anthropic thinking blocks become Responses `reasoning` output items with summary text.
- Usage carries over when the backend reports it.
- Converted responses store local state by default, so `previous_response_id`,
  response retrieval, input item listing, and conversation endpoints work without
  OpenAI as the backend.

Streaming conversion:

- Direct OpenAI and direct Argo Responses streams pass through.
- Converted streams emit Responses SSE events built from upstream Anthropic,
  OpenAI Chat Completions, Google, or Argo stream chunks.
- Legacy Argo mode may simulate streaming from a non-streaming upstream response.

### Converted Responses Limitations

Conversion approximates OpenAI's Responses API rather than replacing it. It
applies to every path except `-provider openai` and
`-provider argo -openai-responses` on a `gpt*` model. Known gaps:

- The proxy rejects OpenAI prompt templates (`prompt`), which have no portable provider representation.
- The proxy does not run OpenAI-hosted tools such as web search and file search.
  It logs and drops unsupported tool types on converted paths.
- Custom tools carry over, but target providers may not enforce OpenAI
  custom-tool grammar or validation semantics.
- Some OpenAI-only controls have no portable effect, including
  `max_tool_calls`, `parallel_tool_calls`, `include`, `truncation`,
  `top_logprobs`, `prompt_cache_key`, `reasoning.summary`, `reasoning.mode`,
  and `reasoning.context`. The proxy preserves `reasoning.mode` and
  `reasoning.context` on the direct OpenAI Responses path but warns and drops
  them on converted provider paths. It forwards `text.verbosity` on
  OpenAI-compatible converted routes and warns and drops it for providers
  without an equivalent field.
- The converted response path does not synthesize output images, files, audio,
  annotations, or logprobs.
- Local response and conversation state lives under `~/.apiproxy/sessions` or
  `-sessions-dir`, not on OpenAI's servers.
- `store:false` turns off local persistence for foreground converted requests,
  so response retrieval and `previous_response_id` do not work for those
  responses.
- Background execution, cancellation, compaction, and token counts run locally
  and approximate the upstream behavior.
- Converted SSE streams expose the common Responses event shape but omit some
  event types and metadata fields that OpenAI produces.

## Token Counting

- `/v1/messages/count_tokens` uses Anthropic token counting for Anthropic and Argo Claude-routed models.
- Google backends use Gemini token counting where Gemini offers it.
- Non-Claude Argo models, legacy Argo mode, OpenAI, and unsupported routes fall back to local estimation.
- `/v1/responses/input_tokens` resolves local Responses state first on converted
  provider paths, then applies the same counting or estimation behavior.

## Data Locations

- `lmc` sessions: `~/.lmc/sessions`
- `lmc` logs: `~/.lmc/logs`
- `apiproxy` Responses state: `~/.apiproxy/sessions`
- Built binaries: `./bin/lmc` and `./bin/apiproxy`

## Troubleshooting

- Pass `-log-level DEBUG` to either binary to inspect request routing and conversion warnings.
  DEBUG wire logs include complete, unredacted request, response, and stream
  bytes; they can contain credentials and grow without bound on long streams.
- Confirm that the selected `-provider` has credentials or a `-provider-url`.
- For Argo, check whether native mode or `-argo-legacy` matches the model and endpoint you intend to use.
- When a converted Responses request loses an OpenAI-only field, switch to
  `-provider openai`, or to `-provider argo -openai-responses` on a `gpt*` model,
  for direct Responses API passthrough.
- When Codex ignores your profile and still talks to OpenAI, move the profile
  into `~/.codex/<name>.config.toml` with top-level keys and delete the old
  `[profiles.<name>]` table. Codex 0.134.0 and later read neither that table nor
  a top-level `profile` selector from `config.toml`.

### Large Requests And Truncated Streams

Sessions that carry large attachments, such as page images of a PDF or a long
build log, grow every turn because clients resend the whole transcript. Three
things can go wrong, and each reports itself differently:

- `413 request_too_large` from `apiproxy` means the body passed
  `-max-request-body-size`. When the client supplied `Content-Length`, the
  message states the observed size and the value that would admit it. Otherwise,
  it names the setting without guessing how large the body was.
- A `413` or a size complaint whose body came from the provider means the
  provider's own limit was hit. Raising `-max-request-body-size` will not help;
  shrink the attachment or start a new session.
- A stream that ends with `response.failed`, an Anthropic `error` event, or an
  error chunk followed by `[DONE]` means generation or stream processing failed
  part way through. While the downstream connection remains writable,
  `apiproxy` closes a started stream with the appropriate terminal marker. If a
  client still reports "stream closed before completion," inspect the proxy logs
  and the client-to-proxy connection; cancellation or a write failure can prevent
  a final marker from reaching the client. A connection that drops *after* the
  model finished its turn is not reported as a failure.

Every path buffers the request body, and converted or stored paths create
additional representations of it. Peak memory depends on payload shape and
concurrency rather than a fixed multiple of body size.
`-max-request-body-size` defaults to 512MB to match OpenAI's own payload limit;
treat that as an admission ceiling, not a per-request memory estimate, and lower
it if the machine cannot safely absorb concurrent large requests.
