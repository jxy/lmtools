# API Support Matrix

Use the section for the endpoint your client calls. The field lists describe
what `apiproxy` decodes. The configured backend and mapped model determine
what goes upstream. For an unknown field path the proxy logs WARN the first
time it sees it from a given source, then DEBUG on repeats. Direct Responses
requests preserve unknown fields in the raw body. Chat and Messages handlers
omit unknown top-level fields when they encode the parsed request.
[Endpoint list and examples](../README.md#supported-endpoints)

For a required tool call, Messages and Responses conversions send `"any"`
to OpenAI Chat and `AUTO` to Google. Streamed Chat requests to Google also
use `AUTO` for `tool_choice: "required"`, allowing a reply without a tool call.

## Canonical Internal Request Fields

`TypedRequest` carries these fields between provider formats.

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

Individual routes also handle fields outside this list. For example, Messages
passes `top_k` separately to the Google renderer, and Responses handles
`previous_response_id` while preparing the conversation.

## `/v1/messages`

**Recognized request fields**

- `model`: required
- `messages`: required, nonempty array
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

**Provider behavior**

- With `-provider anthropic`, or native Argo and a mapped `claude*` model,
  the proxy forwards the parsed Messages request and the `anthropic-beta`
  header. Both `temperature` and `top_p` reach the backend if supplied.
  Set a positive `max_tokens` here. Omitting it sends `0`. The Claude
  defaults described below apply when the proxy converts another API to Messages.
- Direct Messages routes preserve `container` (a string), `context_management`,
  `inference_geo`, `speed`, `cache_control`, and `mcp_servers`. The OpenAI
  and Google conversions omit those fields. `top_k` passes through to
  Anthropic and becomes `generationConfig.topK` on Google. Google omits
  request `metadata` and `service_tier`. Legacy Argo sends `metadata` in
  the request even though it logs a warning about dropping it.
- Direct Messages routes preserve server tool definitions and tool fields
  such as `type`, `cache_control`, `strict`, and `defer_loading`. When
  converting to OpenAI or Google, a server tool without an `input_schema`
  is omitted. A named tool with a schema can become a client function,
  losing its server behavior. Conversion omits tool cache and deferred
  loading settings and warns once per request if any tool defers loading.
  Legacy Argo runs the per-tool OpenAI drop warnings for non-Claude models.
- Direct Messages routes preserve `thinking.type`, `thinking.display`, and
  `output_config.effort`. The proxy accepts `summarized` or `omitted` for
  `display`. Providing a display with disabled thinking or a nonzero
  `budget_tokens` with adaptive thinking returns HTTP 400.
- The proxy accepts `low`, `medium`, `high`, `xhigh`, and `max` for
  `output_config.effort`. OpenAI and native Argo Chat receive these values
  as `reasoning_effort`. For `xhigh` and `max`, a Google model name containing
  `gemini-2.5` gets a thinking budget of 24576. Other Google names get
  `thinkingLevel: "high"`. A supported JSON object or schema in
  `output_config.format` maps to OpenAI or Google JSON output controls.
- For a function tool, a named Messages `tool_choice` selects that
  function on Chat and sets Google's mode to `ANY` with that one allowed
  function. Messages `{"type":"any"}` reaches Chat as the literal string
  `"any"` and Google as `AUTO`.
- Streaming responses use Anthropic SSE events. On native streaming routes
  the proxy starts idle pings once the upstream returns HTTP 200. The
  default interval is 15 seconds. Legacy Argo streams text directly and
  simulates a stream from the buffered response when tools are present.
- Chat reasoning (`reasoning_content` or `reasoning`) reaches Messages
  clients as a thinking block before the answer. Its `lmtools-chat-reasoning:`
  signature lets the proxy replay it in the original Chat field. On a stream
  the proxy sends the signature before starting text or a tool call and drops
  reasoning received after that point. Conversion warns about unsupported
  blocks such as documents and server tool results.
- An image URL or base64 data URL can travel as an Anthropic `image` source,
  a Chat `image_url`, or a Responses `input_image`. On Google, base64 data
  becomes `inlineData` and a remote image becomes the text `[Image: url]`.
- For tool results, Anthropic `tool_result.content` and Responses
  `function_call_output.output` can contain text and image parts. Conversion
  joins the text and carries unrecognized structured parts as JSON text.
  Google places image bytes under `functionResponse.parts`. For Chat, a
  user message with the images follows the round's tool messages.
- On Messages and Messages token-count ingress, the proxy adds
  `"thinking":""` to a `thinking` block missing that field and logs the
  insertion. The signature travels with the block.

## `/v1/messages/count_tokens`

**Recognized request fields**

- `model`: optional for local estimation, required for native provider counting
- `messages`: required, nonempty array
- `system`
- `tools`

**Provider behavior**

- With a model, Anthropic and native Argo `claude*` routes call
  `/messages/count_tokens`. Google calls Gemini `countTokens` with a
  `generateContentRequest` and returns its `totalTokens` as `input_tokens`.
- Without a model, or on OpenAI, other Argo models, or legacy Argo, the
  proxy estimates locally. A native counter returning HTTP 404 also selects
  the local estimator. Other native errors reach the client.
- The Google route reports the tool fields that conversion drops. The
  local estimator counts deferred tools as loaded and warns when
  `defer_loading` is true.

## `/v1/chat/completions`

**Recognized request fields**

- `model`: required
- `messages`: required, nonempty array
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

**Provider behavior**

- Before routing, the proxy checks that each assistant tool call has a
  distinct ID within its batch and that subsequent tool messages answer
  every ID before another role appears. An orphan tool message, a missing
  result, or an unmatched ID returns HTTP 400.
- OpenAI and native Argo with a non-Claude model both forward the recognized
  Chat fields. These include `n`, penalties, `seed`, `parallel_tool_calls`,
  `extra_body`, cache controls, audio controls, and logprobs. The upstream
  decides which its model accepts. Both routes preserve the Chat reasoning
  fields `reasoning_content` and `reasoning` in replies and client history.
  Native Argo sends `developer` messages as `system`.
- On those two routes, the proxy removes `stop` from the upstream request
  and enforces it on returned text, including streamed text. This also
  applies to Messages requests sent to those Chat endpoints. Anthropic
  and Google receive `stop_sequences` and `stopSequences`, respectively.
- Direct Chat routes omit nonpositive token limits and forward positive
  limits under their supplied names. On conversion, a positive `max_tokens`
  takes precedence over `max_completion_tokens`. A converted request sent
  to Chat uses `max_completion_tokens`.
- Conversion to Anthropic or Google combines leading text instructions in
  the system field and sends later system or developer messages as user
  messages. On conversion to Anthropic, `temperature` takes precedence
  over `top_p`. For `claude-opus-4-7` and its dated IDs, the renderer
  drops `temperature` and forwards `top_p` only if no temperature was supplied.
- OpenAI and native Argo Chat forward `reasoning_effort`, `response_format`,
  and `verbosity`. On conversion to Anthropic, `minimal` maps to `low`,
  and `low`, `medium`, `high`, `xhigh`, and `max` keep their names.
  These set `output_config.effort` and adaptive thinking with summarized
  display. `none` omits both controls. Anthropic and Google omit `verbosity`.
- For `response_format.type` of `json_object` or `json_schema`, Anthropic
  receives `output_config.format`. In schema mode it carries the schema
  itself. OpenAI's wrapper name, description, and strict flag are omitted.
  Google receives `responseMimeType: "application/json"` and, for a supplied
  schema, `responseJsonSchema`.
- Google buffered Chat requests use the Google renderer directly. Streamed
  Chat requests first pass through the Anthropic renderer. For example, on
  a Gemini 2.5 model, buffered `reasoning_effort: "none"` sends a zero
  thinking budget. The streamed request omits `thinkingConfig`. With both
  sampling controls supplied, the buffered request carries both and the
  streamed request carries `temperature`. A Chat `tool_choice` of
  `"required"` selects `ANY` on buffered Google requests and `AUTO`
  on streamed requests.
- Direct Chat routes forward `input_audio` to the backend. The Google
  buffered route represents it as a text placeholder. Conversion to
  Anthropic, legacy Argo, or streaming Google Chat rejects audio input.
- Anthropic conversion maps `user`, or `safety_identifier` when `user` is
  empty, to `metadata.user_id`. It maps service tier `default` to
  `standard_only` and `priority` to `auto`. Google omits request metadata,
  user identifiers, and service tier. On converted Chat streams,
  `stream_options.include_usage` controls the final usage chunk.

## `/v1/responses`

**Recognized fields for creating a response**

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

### Forwarding and conversion

- OpenAI and native Argo with a mapped `gpt*` model forward create requests
  to the backend's Responses API by default. The original request bytes,
  including hosted tools and unknown fields, go upstream unless a model map
  or encrypted-state recovery changes them. Where the response carries a
  model name, the proxy restores the client's alias. `-chat-completions`
  selects conversion.
- `-strip-encrypted-reasoning` is an opt-in recovery exception on direct
  Responses paths. It drops every reasoning and compaction input item carrying
  `encrypted_content`. Both go whole, because `encrypted_content` is the item's
  content and the `id` left behind names state the backend could not resolve.
  Reasoning items without `encrypted_content`, all other input items, and
  unknown top-level fields remain present.
- Converted routes return a request error for `prompt` templates,
  `max_tool_calls`, or `top_logprobs`. Use direct Responses forwarding
  with a backend that implements these fields.
- On converted routes, `parallel_tool_calls`, `truncation`, and
  `prompt_cache_key` have no portable effect. An `include` request cannot
  add data the target never returned. The proxy does not generate output
  images, files, audio, annotations, or logprobs during conversion.
- Anthropic, Google, other Argo models, legacy Argo, and
  `-chat-completions` use conversion. A string `input` becomes a user
  message. Arrays can carry messages, function and custom calls, results,
  reasoning, and compaction items. The proxy skips unsupported item types
  with a warning.
- Direct Responses requests preserve the `developer` role. Conversion to
  native Argo Chat Completions sends developer messages as `system`. With
  `-provider openai -chat-completions`, the proxy combines leading system
  and developer text into the model's instruction message. Later developer
  messages become user messages.
- Responses `text.format` maps to the JSON output controls for `json_object`
  and `json_schema`. Direct Responses routes pass `text.verbosity` through.
  Native Argo Chat Completions receives it as `verbosity`. Other converted
  routes omit it, including `-provider openai -chat-completions`.
- Direct Responses routes preserve `reasoning.mode`, `reasoning.context`,
  and `reasoning.summary`. On conversion, those settings have no upstream
  equivalent. Native Argo Chat Completions receives `reasoning.effort` as
  supplied. The `-provider openai -chat-completions` path goes through the
  Anthropic renderer, so `minimal` becomes `low` and `none` is omitted.
- Converted Claude Anthropic-wire paths forward `max_output_tokens` when it is
  provided. When it is omitted, the proxy supplies the required
  Anthropic `max_tokens` field as `128000` for Opus models and `64000` for other
  Claude models.
- Converted requests support function, custom, and namespace tools. On
  routes that require functions, custom input is wrapped in an object and
  its grammar is not enforced. The proxy merges top-level `tools` with
  definitions from `additional_tools` input items, in that order. Duplicate
  names after namespace flattening return HTTP 400. Unsupported hosted
  tools such as `web_search_preview` and `file_search` are dropped with a warning.
- With a function tool present, converted `tool_choice: "required"` sends
  `{"type":"any"}` to Anthropic and `"required"` to native Argo Chat.
  On the OpenAI Chat conversion it sends `"any"`, and on Google it sends
  `AUTO`. This applies to both buffered and streamed Responses requests.

### Stored responses and conversations

- On converted provider paths, `store` defaults to true. Stored response and
  conversation state is written under `~/.apiproxy/sessions` unless apiproxy is
  started with `-sessions-dir`. Give each proxy process its own directory.
  For a foreground request, `"store":false` disables persistence. The
  returned ID then cannot be retrieved or used as `previous_response_id`.
  Continuing from a stored response with an existing successor starts a
  branch. Both responses remain retrievable.
- `background:true` returns a queued response and creates local state for
  polling and cancellation, including with `store:false`. Combining
  background execution with `stream:true` on a converted route returns
  HTTP 400.
- `previous_response_id` reloads a completed response's history, preserving
  `reasoning`, `function_call`, and `function_call_output` items in order for
  tool-call follow-ups. Stored Chat reasoning goes back to a Chat backend
  in the field it came from.
- Retrieval, deletion, cancellation, input item listing, and conversation
  operations use local records for converted responses. With direct
  Responses forwarding enabled, the proxy looks up the ID locally first
  and forwards a miss upstream. `POST /v1/conversations` has no ID and goes
  upstream in this configuration. The local `/v1/conversations` collection
  supports `POST` and returns 405 for `GET`. The process remembers model
  aliases by response ID for upstream retrievals. [Lifecycle endpoints](../README.md#supported-endpoints)
- Local input item and conversation item lists support `after`, `limit`
  (1–100, default 20), and `order` (`asc` or `desc`, default `desc`).
  The local list handler ignores `include`. Direct lifecycle forwarding
  passes the query string upstream.
- The mapped model also determines where `POST /v1/responses/input_tokens`
  and `POST /v1/responses/compact` go. Both accept a model from a locally
  stored `previous_response_id` when it is omitted. On converted routes
  both read any referenced history without advancing it.
- For converted `input_tokens`, Anthropic and native Argo `claude*` use the
  Anthropic counter, Google uses Gemini `countTokens`, and the other routes
  estimate locally. An error from a selected native counter, including HTTP
  404, reaches the client. The 404 fallback applies to `/v1/messages/count_tokens`.
- Converted `compact` asks the backend for a summary with tools disabled.
  Its output includes a `compaction` item whose `encrypted_content` field
  holds **plain text**. Send the item back as input to carry the summary
  into the next converted request.

### Output and streams

- Read converted answer text in `output[].content[]` with content type
  `output_text`.
- Converted streams emit Responses SSE events as upstream chunks arrive.
  A failure after the stream starts produces `response.failed` while the
  client connection is writable. Direct Responses streams forward provider
  events. An upstream `event: error` is terminal even if the data omits
  `"type":"error"`. If a started direct stream ends without a terminal
  Responses event, the proxy emits `response.failed` before `[DONE]` when
  that sentinel is present.
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
to run the function in your client. To continue a stored response, send
the model name in `model` and the response ID in `previous_response_id`.
Include a `function_call_output` item with the original `call_id` and
the result in `output`.

## Responses And Streams

The offline fixtures and proxy tests cover the following behavior.

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
