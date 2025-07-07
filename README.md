# argo CLI

`argo` is a simple command-line client for the Argo API to generate embeddings and chat responses.

## Installation

Build the binary (requires Go 1.21+):

```bash
# produces an `argo` binary in the repository root
go build ./cmd/argo
```

## Usage examples

### Embedding (default dev environment)
```bash
echo "Hello world" | argo --env dev -e
```

### Chat (non-streaming)
```bash
echo "What is Argo?" | argo --env dev
```

### Streaming chat
```bash
echo "Tell me a story" | argo --env prod --stream
```

### Prompt-chat mode
```bash
echo "Fix my code" | argo --env dev --prompt-chat
```

### Custom API URL
```bash
echo "Test" | argo --env https://custom.example/api -e
```

## Flags

- `--env`: environment (`prod`, `dev`, or custom base URL)
- `-e`: embed mode
- `--stream`: streaming chat mode
- `--prompt-chat`: use prompt instead of messages
- `--log-level`: logging level (`info`|`debug`)
- `--timeout`: HTTP timeout duration
- `-u`: user identifier
- `-s`: system prompt for chat
- `-m`: model name (overrides default)
- `--logDir`: directory to save JSON logs

Logs are written to the specified `--logDir` (default `$HOME/tmp/log/argo`).