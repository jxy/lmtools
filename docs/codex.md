# Using Codex with apiproxy

Codex sends requests to the proxy's `/v1/responses` endpoint. Define the
connection in your user configuration and select it with a named profile.
The example uses Argo. Substitute model names appropriate for your backend.
[Starting apiproxy](../README.md#apiproxy)

## Configure the client

Add this provider to `~/.codex/config.toml`. The base URL includes `/v1`.
The proxy supplies its own upstream credentials.

```toml
[model_providers.apiproxy]
name = "apiproxy"
base_url = "http://127.0.0.1:8082/v1"
```

Create `~/.codex/apiproxy.config.toml` alongside that file. These two
settings go at the top level of the profile.

```toml
model = "gpt-5.5"
model_provider = "apiproxy"
```

## Connect to the backend

Start the proxy in one terminal. This rule translates the model name in the
profile to the model name at Argo. If your backend accepts the client name,
omit `-model-map`.

```sh
apiproxy -provider argo -argo-user "$USER" -model-map '^gpt-5\.5$=gpt55'
```

In a second terminal, start an interactive conversation or run one prompt.

```sh
codex --profile apiproxy
codex exec --profile apiproxy "Summarize this repository."
```

For this mapping the proxy forwards requests to Argo's Responses API because
the mapped name starts with `gpt`. To use a model that serves only Chat
Completions on an OpenAI backend, start the proxy with `-provider openai`
and `-chat-completions`. The converted route handles response state locally.
[Responses fields and tool support](api-support-matrix.md#v1responses)

If the client still reaches its default provider, check that the files are
in the user configuration directory and that the name before `.config.toml`
matches `--profile`. `curl http://127.0.0.1:8082/` checks the listener.
A request for `/v1/responses` in the proxy log confirms the client reached it.
