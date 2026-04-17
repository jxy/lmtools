# Fixture Script Documentation

This directory contains the maintained shell wrappers for the checked-in API fixture corpus under `testdata/api-fixtures/`.

## Available Scripts

### `api_fixtures_list.sh`, `api_fixtures_verify.sh`, `api_fixtures_capture.sh`, `api_fixtures_capture_all.sh`, `api_fixtures_compare.sh`, `api_fixtures_compare_all.sh`

**Purpose**: Manage the shared request/response fixture corpus used by proxy rendering, parsing, and streaming tests.

**Usage**:
- `./api_fixtures_list.sh`
- `./api_fixtures_verify.sh`
- `./api_fixtures_verify.sh --provider anthropic`
- `./api_fixtures_verify.sh --refresh`
- `./api_fixtures_verify.sh --refresh --case anthropic-messages-basic-text --target openai-stream`
- `./api_fixtures_verify.sh --refresh --provider anthropic --target argo`
- `./api_fixtures_capture.sh -case openai-tool-followup -target openai`
- `./api_fixtures_capture_all.sh`
- `bash ./api_fixtures_compare.sh -case anthropic-messages-basic-text -target openai`
- `bash ./api_fixtures_compare.sh -case anthropic-messages-basic-text -target argo-openai`
- `bash ./api_fixtures_compare_all.sh -target argo-anthropic`

**Requirements**:
- Provider API keys must be supplied through environment variables outside the repository.
- Argo fixture capture additionally requires `ARGO_API_KEY`.
  Legacy `argo` targets inject it into the request body's `"user"` field.
  `argo-openai*` and `argo-anthropic*` use standard OpenAI/Anthropic auth headers.
- The request JSON for each capture target must already exist under `testdata/api-fixtures/cases/<case>/expected/render/`.
  Stream targets reuse the normal rendered request JSON unless a target-specific override such as `openai-stream.capture.request.json` is checked in.

**Outputs**:
- Canonical JSON responses saved under `captures/<target>.response.json`
- Raw SSE streams saved under `captures/<target>.stream.txt`
- Capture metadata saved under `captures/<target>.meta.json`
- Non-mutating compare commands print `match ...` lines on success and a structural diff on failure

**Notes**:
- These scripts are thin wrappers around `go run ./cmd/apifixtures ...`.
- Prefer extending the fixture corpus for repeatable request/response coverage instead of adding ad hoc shell-based API probes.
- `api_fixtures_verify.sh` also runs targeted Go tests for fixture-driven request/response/stream handling.
- `api_fixtures_compare.sh` performs non-mutating live-vs-capture drift checks by default.
- For `argo-openai*` and `argo-anthropic*`, that default comparison baseline is
  the matching upstream capture (`openai*` or `anthropic*`) rather than a
  same-target checked-in capture.
- `api_fixtures_compare.sh --against <target>` compares one target against another target's checked-in capture.
- `api_fixtures_compare.sh --against <target> --live-against` performs a live-to-live parity check.
- Compare normalizes some known optional metadata differences such as null-vs-absent fields, OpenAI token-detail metadata, Anthropic usage metadata, and OpenAI SSE usage chunks.
- The generic fixture model set is `gpt-5.4-nano`, `claude-haiku-4-5`, and `gemini-3.1-flash-lite-preview`.
- The `openai-chat-audio-input` case stays on an audio-capable OpenAI model for live refresh compatibility.
- That audio case skips Anthropic and Argo render/capture coverage because those endpoints do not accept `input_audio` request blocks.
- Large binary request samples can use `"$fixture_file"` in fixture JSON so the loader base64-encodes a checked-in file at read time.
- For OpenAI `image_url` objects, `"$fixture_file"` plus `media_type` builds a `data:<mime>;base64,...` URL at read time.
- The image live-capture overrides use a checked-in local PNG instead of a remote URL so compare/refresh does not depend on third-party image hosting.
- Live capture prefers `expected/render/<provider>.capture.request.json` when present.
- Live capture checks target-specific files like `expected/render/openai-stream.capture.request.json` before provider-level overrides.
- OpenAI and Anthropic stream targets automatically force `"stream": true` into the capture request body; Google and Argo streaming are selected by endpoint path.
- Google capture requests send `GOOGLE_API_KEY` in the `x-goog-api-key` header.
- Google stream capture uses `:streamGenerateContent?alt=sse`.
- Legacy Argo capture requests overwrite the JSON `"user"` field from `ARGO_API_KEY`, so checked-in fixture JSON can keep a placeholder value.
- Argo-hosted compatibility targets reuse the upstream wire-format render files:
  `argo-openai*` falls back to `openai.request.json`, and `argo-anthropic*`
  falls back to `anthropic.request.json` unless a target-specific capture
  override is present.
- Before those Argo-hosted compatibility requests are sent live, the fixture
  tool rewrites `"model"` to `models.argo` from the case metadata.
- You can override that per endpoint with optional `models.argo-openai` or
  `models.argo-anthropic` entries in `case.json`.
- Request cases can limit checked-in provider renders with `render_targets` in `case.json`.
- The `anthropic-messages-prefill` case keeps `gpt-5.4-nano`; its OpenAI live refresh override uses `reasoning_effort: "none"`, omits `stop`, and uses a larger completion budget.

## Recommended Workflow

```bash
# Make scripts executable
chmod +x scripts/*.sh

# Inspect or refresh the shared fixture corpus
./scripts/api_fixtures_list.sh
./scripts/api_fixtures_verify.sh
./scripts/api_fixtures_verify.sh --provider anthropic
./scripts/api_fixtures_capture.sh -case anthropic-messages-basic-text -target anthropic
./scripts/api_fixtures_capture_all.sh -provider anthropic -target argo
./scripts/api_fixtures_capture_all.sh -target openai
bash ./scripts/api_fixtures_compare.sh -case anthropic-messages-basic-text -target openai
bash ./scripts/api_fixtures_compare.sh -case anthropic-messages-basic-text -target argo-openai
bash ./scripts/api_fixtures_compare_all.sh -target argo-anthropic
make verify-fixtures
```

## Contributing

When adding new API schema coverage:
1. Add or update a case under `testdata/api-fixtures/`.
2. Capture representative replies with the `api_fixtures_*` scripts.
3. Prefer fixture-driven tests over new inline JSON literals.
4. Name the case after the actual endpoint and scenario, for example `anthropic-messages-prefill`.
