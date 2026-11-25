# API Support Matrix

Use the section for the endpoint your client calls. Direct Responses requests
preserve unknown fields in the raw body. On converted routes the proxy
reports unknown fields at WARN and omits them from the provider request.
[Endpoint list and examples](../README.md#supported-endpoints)

## Canonical Internal Request Fields

The current canonical request model preserved by `TypedRequest` is:

- `system`
- `developer`
- `messages`
- `tools`
- `tool_choice`
- `max_tokens`
- `temperature`
- `top_p`
- `stop`
- `stream`
- `reasoning_effort`
- `reasoning_mode`
- `reasoning_context`
- `verbosity`
- `thinking`
- `output_config`
- `response_format`
- `metadata`
- `service_tier`

Anything outside that set must either be direct-provider-only or logged as a warning when it is dropped during conversion.

## `/v1/messages`

Accepted top-level fields:

- `model`: required
- `messages`: required
- `system`
- `max_tokens`
- `stop_sequences`
- `stream`
- `temperature`
- `top_p`
- `top_k`
- `metadata`
- `tools`
- `tool_choice`
- `thinking`
- `output_config`
- `container`
- `context_management`
- `service_tier`
- `inference_geo`
- `speed`
- `cache_control`
- `mcp_servers`

Behavior by configured provider:

- Preserved broadly: `model`, `messages`, `system`, `max_tokens`, `temperature`, `top_p`, `stream`, `tools`
- Streaming responses use Anthropic SSE events. After upstream streaming HTTP 200, the proxy emits `ping` keepalives when the downstream client stream is idle for the ping interval.
- Preserved or normalized on supported routes: `tool_choice`
- Anthropic or Google only: `top_k`; OpenAI/Argo OpenAI conversion logs a warning and drops it
- Anthropic only: `container`, `context_management`, `inference_geo`, `speed`, `cache_control`, and `mcp_servers`; other provider conversions log warnings when these are dropped
- Anthropic-wire routes (`-provider anthropic`, and `-provider argo` with a
  `claude*` model in native mode) forward the parsed Anthropic request directly,
  so tool entries keep `type`, `cache_control`, `strict`, and `defer_loading`,
  including server-tool definitions such as the tool-search tools. Converted
  routes rebuild the tool array from `TypedRequest` and carry only client
  function and custom tools; the dropped `tools[].<type>`,
  `tools[].cache_control`, and `tools[].defer_loading` are logged as warnings.
  `defer_loading` is warned once per request rather than once per tool, and only
  when a tool sets it to `true` — an explicit `false` is the API default and
  loses nothing. `/v1/messages/count_tokens` reports the same drops on its Google
  branch, and warns that its local estimator counts deferred tools as if loaded.
  Legacy Argo (`-argo-legacy`) is a converted route for every model and does not
  yet warn per tool field.
- `metadata` is preserved for Anthropic and OpenAI conversions, and warned/dropped for Google
- Anthropic-compatible routes preserve `thinking.type`, `thinking.display`, and
  `output_config.effort`. `thinking.display` accepts `summarized` and `omitted`;
  `display` is invalid with `thinking.type="disabled"`. The configured upstream
  remains authoritative about which thinking modes a particular model accepts.
- Portable OpenAI/Responses reasoning effort converted to Anthropic wire also
  emits `thinking:{"type":"adaptive","display":"summarized"}`. Effort and
  thinking activation are separate Anthropic controls, and the explicit display
  keeps newer models from returning signature-only thinking blocks by default.
- Accepted `output_config.effort` values are `low`, `medium`, `high`, `xhigh`,
  and `max`. For OpenAI/Argo OpenAI conversion, efforts map to `reasoning_effort`
  one-to-one, including `max`. For Google conversion, efforts map to Gemini
  thinking configuration where possible (`xhigh` and `max` both map to the
  highest Gemini thinking tier).
- `output_config.format` is converted to OpenAI `response_format` or Google JSON response configuration when it is a supported JSON schema/object shape.
- Anthropic request normalization prefers `temperature` when both `temperature` and `top_p` are present; `top_p` is omitted in that case because current Anthropic models reject both together
- `tool_choice` is converted to Google `functionCallingConfig` when tools are present.
- Content blocks without a target equivalent, such as Anthropic thinking blocks on OpenAI request output or server-tool/document/search-result blocks on non-Anthropic output, are logged as warnings when dropped. A thinking block whose signature starts with `lmtools-chat-reasoning:` is one apiproxy made from Chat Completions reasoning; on OpenAI output it goes back in the Chat field it came from (`reasoning_content`, or `reasoning`), with no warning.
- Chat Completions reasoning (`reasoning_content`, or `reasoning` from newer vLLM) reaches Anthropic clients as a thinking block ahead of the text, buffered or streamed, signed with that marker. On a stream, the block ends with a `signature_delta` before the text or the first tool call starts; reasoning that arrives after either has no block to go in and is dropped.
- Image blocks convert between the Anthropic `image` source, the OpenAI `image_url` part, and the Responses `input_image` part, as URL or base64 data URL. On Google output a base64 image becomes an `inlineData` part; a URL image has no Gemini request equivalent and is rendered as the text mention `[Image: url]` rather than dropped.
- Tool results carry images. On input, an Anthropic `tool_result` whose `content` is an array yields its text blocks joined as the result text and its image blocks as images, and a Responses `function_call_output` whose `output` is a content list yields its `input_image` parts as images; a block of any other type keeps its JSON in the text. On output the images render as the Anthropic `tool_result` content array, the Responses `function_call_output` content list, an `inlineData` part nested under the Google `functionResponse` (`parts`, accepted by Gemini 3 and later), and, on Chat Completions where a `tool` message is text only, a `user` message with `image_url` parts placed right after the round's tool messages.
- Assistant `thinking` content blocks that arrive with only a `signature` (no `thinking` field) are normalized to include an empty `"thinking":""` so Anthropic and Argo Anthropic-wire endpoints accept them, and each insertion is logged at WARN level. `redacted_thinking` blocks and blocks that already include `thinking` are left unchanged.

## `/v1/messages/count_tokens`

Accepted top-level fields:

- `model`: optional for local estimation, required for native provider counting
- `messages`: required
- `system`
- `tools`

Behavior by configured provider:

- Anthropic backends forward to Anthropic `count_tokens`. An HTTP 404 there,
  from an Anthropic-compatible server without the route such as the ALCF
  inference gateway, falls back to the local estimator with a WARN.
- Argo native mode forwards to Anthropic `count_tokens` only for Claude-routed
  models. Non-Claude Argo models and legacy Argo mode use the local estimator.
- Google backends convert the Anthropic-shaped request through `TypedRequest` and
  call Gemini `countTokens`.
- OpenAI and unsupported providers use the local estimator.

## `/v1/chat/completions`

Accepted top-level fields:

- `model`: required
- `messages`: required
- `temperature`
- `top_p`
- `n`
- `stream`
- `stop`
- `max_tokens`
- `max_completion_tokens`
- `presence_penalty`
- `frequency_penalty`
- `logit_bias`
- `user`
- `tools`
- `tool_choice`
- `response_format`
- `reasoning_effort`
- `stream_options`
- `metadata`
- `store`
- `service_tier`
- `seed`
- `verbosity`
- `modalities`
- `audio`
- `prediction`
- `web_search_options`
- `prompt_cache_key`
- `prompt_cache_retention`
- `safety_identifier`
- `parallel_tool_calls`
- `logprobs`
- `top_logprobs`
- `extra_body`

Behavior by configured provider:

- Preserved broadly: `model`, `messages`, `temperature`, `top_p`, `stream`, `stop`, `max_tokens`, `tools`, `tool_choice`, `reasoning_effort`
- For OpenAI-compatible Chat Completions upstreams (OpenAI and Argo non-Claude routes), client `stop` values are intentionally omitted from the upstream request and enforced locally by the proxy on returned text. This applies to direct `/v1/chat/completions` pass-through and to `/v1/messages` requests converted to OpenAI-compatible Chat Completions. Native Anthropic `stop_sequences` and Google `stopSequences` are still forwarded to those providers.
- OpenAI Chat Completions accepts `developer` messages. On Argo Chat
  Completions the proxy sends these as `system`. On Anthropic and Google
  it puts leading system and developer instructions in the system field.
- The proxy forwards `reasoning_effort`, `response_format`, and `verbosity`
  to OpenAI and native Argo Chat Completions. Portable effort levels are
  `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, and `max`. On Anthropic
  routes `minimal` maps to `low` and `none` omits the effort and thinking
  controls. Google uses `thinkingConfig` where supported. Conversion to
  Anthropic or Google drops `verbosity` with a warning.
- `response_format.json_schema` and `response_format.json_object` convert to Anthropic `output_config.format` and Google JSON response generation configuration.
- `metadata`, `service_tier`, and OpenAI `user`/`safety_identifier` are converted where the target has matching request metadata. `stream_options.include_usage` is preserved through the internal metadata flag used by OpenAI-format streaming responses.
- OpenAI-direct only, with warning on conversion: `n`, `presence_penalty`, `frequency_penalty`, `logit_bias`, `store`, `seed`, `modalities`, `audio`, `prediction`, `web_search_options`, `prompt_cache_key`, `prompt_cache_retention`, `parallel_tool_calls`, `logprobs`, `top_logprobs`, `stream_options.include_obfuscation`, `messages[].name`, response-only message metadata, and custom tool types without a target equivalent.
- Chat reasoning fields, `reasoning_content` and the `reasoning` that newer vLLM sends, stay on direct OpenAI-compatible routes: in buffered answers, in streams the proxy decodes to enforce stop sequences, and on the assistant messages of a client's history.
- Direct OpenAI and native Argo Chat Completions routes forward `input_audio`.
  The backend model determines whether it accepts audio. Conversion to
  Anthropic Messages or legacy Argo rejects audio input blocks.
- Unknown top-level fields are logged at WARN and ignored.

## `/v1/responses`

Accepted top-level fields:

- `model`: required
- `input`: required unless `previous_response_id` or `prompt` is set
- `instructions`
- `background`
- `conversation`
- `tools`
- `tool_choice`
- `text`
- `reasoning`
- `max_output_tokens`
- `max_tool_calls`
- `temperature`
- `top_p`
- `stop`
- `stream`
- `stream_options`
- `store`
- `metadata`
- `service_tier`
- `user`
- `safety_identifier`
- `prompt`
- `prompt_cache_key`
- `previous_response_id`
- `include`
- `parallel_tool_calls`
- `truncation`
- `top_logprobs`

Behavior by configured provider:

- Direct OpenAI provider requests are forwarded to `/v1/responses` unless
  `-chat-completions` is set, which converts them to OpenAI Chat Completions;
  non-stream responses are parsed only to restore the client-requested model name. Direct passthrough
  preserves valid OpenAI Responses fields that are not represented by the proxy's
  typed compatibility struct.
- `-strip-encrypted-reasoning` is an opt-in recovery exception on direct
  Responses paths. It drops every reasoning and compaction input item carrying
  `encrypted_content`. Both go whole, because `encrypted_content` is the item's
  content and the `id` left behind names state the backend could not resolve.
  Reasoning items without `encrypted_content`, all other input items, and
  unknown top-level fields remain present. The default leaves the original
  request untouched.
- With `-provider argo`, requests whose backend model starts with `gpt` are
  forwarded the same way to Argo's `/argoapi/v1/responses` using Argo bearer
  auth, unless `-chat-completions` or `-argo-legacy` is set. Argo serves the
  Responses API for those models only, so all other Argo models still convert.
- Converted routes return a request error for `prompt` templates,
  `max_tool_calls`, or `top_logprobs`. Use direct Responses forwarding
  with a backend that implements these fields.
- On converted routes, `parallel_tool_calls`, `truncation`, and
  `prompt_cache_key` have no portable effect. An `include` request cannot
  add data the target never returned. The proxy does not generate output
  images, files, audio, annotations, or logprobs during conversion.
- For Anthropic, Google, `-chat-completions`, and non-passthrough Argo routes, the proxy converts Responses
  `input` message/function-call/function-call-output items through `TypedRequest`, forwards
  through the existing provider path, and returns a Responses-shaped object.
  Converted provider paths preserve only fields with a supported compatibility
  mapping.
- Direct Responses requests preserve the `developer` role. Conversion to
  native Argo Chat Completions sends developer messages as `system`. With
  `-provider openai -chat-completions`, the proxy combines leading system
  and developer text into the model's instruction message. Later developer
  messages become user messages.
- Responses `text.format` maps to the JSON output controls for `json_object`
  and `json_schema`. Direct Responses routes pass `text.verbosity` through.
  Native Argo Chat Completions receives it as `verbosity`. Other converted
  routes omit it, including `-provider openai -chat-completions`.
- Converted Claude Anthropic-wire paths forward `max_output_tokens` when it is
  provided. When it is omitted, the proxy supplies the required
  Anthropic `max_tokens` field as `128000` for Opus models and `64000` for other
  Claude models.
- On converted provider paths, `store` defaults to true. Stored response and
  conversation state is written under `~/.apiproxy/sessions` unless apiproxy is
  started with `-sessions-dir`. Give each proxy process its own directory.
  For a foreground request, `"store":false` disables persistence. The
  returned ID then cannot be retrieved or used as `previous_response_id`.
  Continuing from a stored response with an existing successor starts a
  branch. Both responses remain retrievable.
- `previous_response_id` reloads the stored session history, preserving
  `reasoning`, `function_call`, and `function_call_output` items in order for
  tool-call follow-ups. OpenAI reasoning items and converted Anthropic thinking
  blocks are stored as `ReasoningBlock` sidecars rather than being flattened
  into text. Chat Completions reasoning is stored as such and goes back to an
  OpenAI-compatible backend in the field it came in, also through the Anthropic
  request the converted `-provider openai` path is built from.
- Local lifecycle endpoints are implemented for converted providers:
  `GET /v1/responses/{id}`, `POST /v1/responses/{id}/cancel`,
  `DELETE /v1/responses/{id}`, `GET /v1/responses/{id}/input_items`, and
  `POST /v1/responses/input_tokens`. `POST /v1/responses/compact` is
  implemented for direct pass-through and emulated for converted
  providers by asking the configured backend for a compact summary and returning
  a Responses-shaped `compaction` input item for the client to carry forward.
  Response and conversation item list endpoints support the official `after`,
  `limit`, and `order` pagination parameters; `include` is accepted but only
  affects fields already present in local state.
- Converted `/v1/responses/input_tokens` resolves stored response/conversation
  context without mutating state. Anthropic backends use native Anthropic
  `count_tokens`; Argo uses native Anthropic count only for Claude-routed
  models; Google uses Gemini `countTokens`; other converted paths use the local
  estimator.
- Local conversation endpoints are implemented for converted providers:
  create/retrieve/update/delete operations under `/v1/conversations`, plus
  create/retrieve/delete/list item operations under
  `/v1/conversations/{id}/items`. `GET /v1/conversations` is not implemented
  because it is not part of the official OpenAI Conversations API.
- When a direct pass-through backend is configured (`-provider=openai`, or
  `-provider=argo`, without `-chat-completions` or `-argo-legacy`), these
  lifecycle and conversation requests
  resolve local state first and are forwarded upstream on a miss, so
  passed-through and converted responses can coexist in one process.
  `POST /v1/conversations` carries no id and is always forwarded. Lifecycle
  retrieval returns the upstream model field unchanged when there is no client
  request model to restore.
- Responses function tools (`{"type":"function","name":...}`), custom tools,
  and namespace tools map to the portable `ToolDefinition` shape. Direct
  pass-through preserves OpenAI-only tool types. Converted provider paths log a
  warning and drop unsupported hosted Responses tool types such as
  `web_search_preview` and `file_search`, then continue without those tools.
- Responses tools are read from both the top-level `tools` field and any
  `additional_tools` input items (the Codex/GPT-5 mechanism that carries
  developer tools inside the `input` array). Tools from both sources are merged
  (top-level first) and converted through the same pipeline; the
  `additional_tools` container item itself contributes no message content.
- Read converted answer text in `output[].content[]` with content type
  `output_text`.
- `reasoning.effort` maps to portable `reasoning_effort`. Direct Responses
  routes preserve `reasoning.mode` (`standard`, `pro`) and `reasoning.context`
  (`auto`, `current_turn`, `all_turns`). Converted routes omit both controls.
- Converted streaming responses are emitted as Responses SSE events after the
  upstream provider returns. Direct Responses streams are passed through. An
  upstream SSE `event: error` is terminal even when its JSON data does not repeat
  `"type":"error"`; it is forwarded without a synthetic `response.failed`.
  Converted paths that render through OpenAI-compatible Chat Completions inherit
  the proxy's local stop enforcement for those Chat Completions streams.
- Converted `reasoning` output items come from Anthropic thinking and from Chat
  Completions reasoning. A stream sends their summary as
  `response.reasoning_summary_part.added`,
  `response.reasoning_summary_text.delta` and `.done`, and
  `response.reasoning_summary_part.done`. An item made from Chat reasoning
  carries `lmtools-chat-reasoning:<type>` as its `encrypted_content`, which the
  proxy reads when a client sends the item back as input.

### A function call over Responses

Start the proxy and give the model a function that your client can run.
This example uses the Argo model from the README.

```sh
curl -sS http://127.0.0.1:8082/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt5",
    "input": "What is the weather in Chicago?",
    "tools": [{
      "type": "function",
      "name": "get_weather",
      "description": "Get the weather for a location.",
      "parameters": {
        "type": "object",
        "properties": {"location": {"type": "string"}},
        "required": ["location"]
      }
    }]
  }'
```

If the response contains a `function_call`, use its `name` and `arguments`
to run the function in your client. To continue a stored response, send its
`id` as `previous_response_id` in the next request. Include an input item of
type `function_call_output` with the original `call_id` and the result in
`output`.

## Responses And Streams

The fixture corpus covers the following behavior.

- OpenAI JSON responses with tool calls
- OpenAI Responses JSON responses with text and function calls
- OpenAI Responses incomplete and failed response shapes
- Stateful Responses API fixtures for `previous_response_id`, retrieve/delete,
  background complete/cancel, `/input_items`, conversation item lifecycle,
  conversation response appends, reasoning/tool follow-up preservation,
  `/input_tokens`, compaction, item-list pagination, and direct OpenAI
  lifecycle pass-through. Capture-capable stateful fixtures can be refreshed
  against the official OpenAI Responses API with the `openai-responses` capture
  target.
- Anthropic JSON responses with `tool_use`
- Google JSON responses with `functionCall`
- Argo JSON responses with embedded tool calls recovered from loose JSON content
- OpenAI upstream streaming translated to Anthropic events, Chat reasoning as a
  thinking block
- Anthropic upstream streaming translated to OpenAI chunks
- The ALCF inference gateway's refusal of a streamed request, a 200 stream with
  one Chat chunk whose ID is `chatcmpl-api-error` and whose content starts with
  `[ERROR]`, reaches each client as a failure in its own shape: an error record
  before `[DONE]` on Chat Completions, `response.failed` on Responses, and an
  `error` event on Messages
- Anthropic thinking stream deltas are preserved on Anthropic-compatible output
  (`thinking_delta` and `signature_delta` fields remain in SSE JSON), and a
  thinking block goes on with the bytes it arrived with, so a
  `content_block_start` keeps its empty `thinking` and `signature` fields
- OpenAI response metadata fields such as `service_tier`, `system_fingerprint`,
  detailed usage token fields, `refusal`, `annotations`, `logprobs`, stream
  `obfuscation`, and Azure/Argo content-safety fields (`prompt_filter_results`,
  `content_filter_results`, `moderation`) are decoded. Only fields with
  target equivalents are emitted when converting formats.
- Anthropic response metadata fields such as `container`, `stop_details`,
  `context_management`, detailed usage cache/server-tool fields,
  `usage.output_tokens_details` (`thinking_tokens`, forwarded on Anthropic output
  and mapped to `reasoning_tokens` for OpenAI/Responses clients), citations deltas,
  and service/speed residency metadata are decoded. Only fields with target
  equivalents are emitted when converting formats.

The request fixture corpus supports live stream capture targets on
request cases such as `openai-stream`, `anthropic-stream`, `google-stream`, and
`argo-stream`. Those captures store raw upstream stream payloads under
`captures/<target>.stream.txt` for later parser and translation assertions.

Add new cases under `testdata/api-fixtures/` when expanding request or response handling.
