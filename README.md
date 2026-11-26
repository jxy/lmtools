# lmtools

| Program | Use it to |
| --- | --- |
| [lmc](#lmc) | Chat in a terminal, run tools, generate embeddings, and return to a saved conversation. |
| [apiproxy](#apiproxy) | Connect an Anthropic Messages, OpenAI Chat Completions, or OpenAI Responses client to the provider you choose. |
| [alcfauth](#alcf-inference-endpoints) | Log in to the ALCF Inference Endpoints and keep a key file current for either client. |
| [notebook](#notebook) | Share a directory of Markdown project notes with `lmc` or another MCP client. |

**Contents** · [Install](#install) · [Chat](#chat) · [Sessions](#sessions) ·
[REPL commands](#conversation-loop) · [Tool use](#tool-use) · [lmc flags](#lmc-flags) ·
[Proxy endpoints](#supported-endpoints) · [Data locations](#data-locations) · [Troubleshooting](#troubleshooting)

> LLMs (GPT, Claude, Gemini, and others) "vibe-coded" almost all code in this
> repository. Use it at your own risk and review it before you deploy to production.

## Install

Download the archive for Linux/amd64, FreeBSD/amd64, or macOS/arm64. Verify it
against the release's `SHA256SUMS`, extract it, and put the four executables
on your `PATH`. Each `lmtools-<tag>-<goos>-<goarch>.tar.gz` includes
`LICENSE`, this README, the public guides in `docs/`, the examples in
`examples/lmc/`, and both notebook handoff prompts in `internal/prompts/`.
[GitHub releases](https://github.com/jxy/lmtools/releases)

To build from a checkout, use Go 1.26.11 or later and BSD or GNU make.

```sh
make build
export PATH="$PWD/bin/$(go env GOOS)-$(go env GOARCH):$PATH"
```

`make build GOOS=linux GOARCH=amd64` builds for Linux in `bin/linux-amd64/`.
Each executable accepts `-h` for usage and `-version` for its build version.

## lmc

### Chat

Pipe a question to `lmc`, or start `lmc -repl` for a conversation. These
examples use Argo, the default provider. Supply your Argo username or key.

```sh
echo "Explain what git rebase does." | lmc -argo-user "$USER"
lmc -argo-user "$USER" -repl
```

For OpenAI, Anthropic, or Google, put the API key in a file readable by you
and select the provider. `-list-models` prints the provider's model IDs and
any capabilities it reports. Pass an ID to `-model` to choose one.

```sh
lmc -provider openai -api-key-file ~/.openai-key -list-models
echo "Explain what git rebase does." | lmc -provider openai -api-key-file ~/.openai-key
lmc -provider anthropic -api-key-file ~/.anthropic-key -repl
lmc -provider google -api-key-file ~/.google-key -repl
```

For a local server that speaks OpenAI Chat Completions, supply its base URL
and the model it serves. A custom OpenAI URL can be used without a key file.

```sh
echo "Hello" | lmc -provider openai -provider-url http://localhost:8080/v1 \
  -model local-model -chat-completions
```

In a single run, `lmc` reads stdin to EOF. At a terminal, type the prompt
and press Ctrl-D at the start of a line. You can also redirect a file into
stdin. `lmc` takes flags only. Use `-tool=false` to give a boolean flag an
explicit value.

Answers stream to stdout. Redirect stdout to keep the answer in a file, or
use `-no-stream` to wait for the complete answer. `-effort` asks the model
for a reasoning effort. A non-`none` effort requests a readable summary on
Responses and enables summarized adaptive thinking on Anthropic routes.
What you see depends on what the model returns. `-hide-thinking` controls
only the display of summaries on stderr.

```sh
echo "Check the reasoning in this argument." | lmc -argo-user "$USER" -effort high
echo "Write a short commit message." | lmc -argo-user "$USER" -hide-thinking > message.txt
```

When a response reports a nonzero token count, `lmc` prints its usage on
stderr, including for tool follow-ups. Parentheses show counts the provider
includes in the input or output figure. `/usage off` hides these lines in
the REPL.

```text
Tokens: input 100 (cached 40) | output 19 (reasoning 12) | total 119
```

To inspect the request, add `-print-curl`. It prints a `curl` command,
including any credentials, without sending the request or updating the session.

```sh
echo "Hello" | lmc -argo-user "$USER" -print-curl
```

### Images, JSON, and embeddings

Attach a PNG, JPEG, GIF, or WebP file with `-image`. Repeat the flag to send
several images in order. An attachment can be the whole prompt. Each file
can be up to 20 MiB, subject to the model's own limits. Embed mode and
legacy Argo reject image input. The session keeps the image so you can
refer to it on a later turn.

```sh
echo "Which labels overlap?" | lmc -argo-user "$USER" -image chart.png
lmc -argo-user "$USER" -image screenshot.png </dev/null
```

For structured output, choose `-json` for a JSON object or `-json-schema`
for a schema accepted by the model. The schema file must contain a JSON
object. The provider enforces the schema. `lmc` checks the syntax of the
returned JSON, so scripts should check the exit status even if streamed
bytes have already arrived.

```sh
echo 'Return a JSON object with a "title" for this report.' | lmc -argo-user "$USER" -json
```

Use `-embed` to write a vector as a JSON array. Embedding runs read one input
from stdin and do not create a session. With OpenAI, select an embedding
model explicitly.

```sh
echo "release notes" | lmc -argo-user "$USER" -embed
echo "release notes" | lmc -provider openai -api-key-file ~/.openai-key \
  -embed -model text-embedding-3-small
```

### Sessions

A chat saves its messages and prints a reference such as `Saved at 0001/0006.`
The first number names the session and the second names a message. Use your
own references in the commands below. Listing, showing, and deleting sessions
work offline without provider credentials.

```sh
lmc -show-sessions
lmc -show 0001
lmc -show 0001/0004
echo "Continue with an example." | lmc -argo-user "$USER" -resume 0001
```

A session is a tree. A bare session number selects its newest message, which
can belong to any branch. A message reference selects the conversation
through that message. Continuing from a message that already has a reply
keeps both alternatives.

| What you want | Where to resume | What to send on stdin |
| --- | --- | --- |
| Ask a follow-up | The answer you want to continue | Your next question |
| Get another answer to a question | The saved question | Empty input (`</dev/null`) |
| Revise a question | Its parent | The revised question |
| Finish a turn after a failed tool follow-up | The saved tool results | Empty input |

```sh
lmc -argo-user "$USER" -resume 0001/0004 </dev/null
echo "What changes on FreeBSD?" | lmc -argo-user "$USER" -resume 0001/0003
```

`lmc -delete 0001/0004` deletes that message and its descendants on every
branch below it. `lmc -delete 0001` deletes the whole session. Use
`-no-session` for a single run that should leave no session transcript.

#### Settings and run options

The system prompt, notebook, and context settings follow the branch. Changing
one on resume applies it from that point forward. `-system TEXT` replaces the
built-in prompt, `-system ''` sets an empty prompt, and `-builtin-system`
restores the default. If the run advertises tools, `lmc` appends their
instructions to whichever prompt you chose.

Provider, model, effort, tool selection, and approval rules apply to the
current run. Supply them again on resume. A `-developer TEXT` instruction
is saved immediately before the next new question or image. OpenAI can
replay that role on both chat APIs, and native Argo `gpt*` can replay it
on Responses. To use instructions across other providers, set a system prompt.

```sh
lmc -argo-user "$USER" -repl -resume 0001 -system "Use SI units."
lmc -argo-user "$USER" -repl -resume 0001 -builtin-system
```

### Conversation loop

`lmc -repl` reads successive turns into one session. Enter sends a turn.
End a line with `\` to continue on the next line or `\\` to end with a
literal backslash. In terminals with bracketed paste, pasted text stays in
one draft until you press Enter. Start typed input with `//` to send a
literal leading slash.

```sh
lmc -argo-user "$USER" -repl -resume 0001
lmc -argo-user "$USER" -repl < questions.txt > answers.txt
```

Ctrl-C cancels the current draft or turn. Ctrl-D on an empty prompt or
`/exit` ends the REPL and reports where to resume. Up and Down recall
input. Ctrl-R searches it. A turn can contain up to 10 MiB of text. With
piped input, each line starts a turn unless continued. The loop processes
later turns after an error and exits nonzero if any turn failed.

To make room for the rest of this conversation, type `/compact`. With a
notebook attached, it runs `/handoff` first. Review any notebook writes
at the usual approval prompts. Without a notebook it goes straight to the
summary. If the summary reduces the estimated input size, `lmc` saves it
and reports the reduction. Type your next prompt at `you>`. Resuming the
branch uses the shorter context, and `/show` still displays the original
messages.

Use `/handoff` on its own to prepare **project notes for a fresh session**.
It runs as an ordinary turn and asks the model to update the notebook using `put`.
You can keep typing in this conversation or start a new REPL with the same
`-notebook` directory and ask it to read the notes. Pin commands also
require a notebook.

| Command | What it does |
| --- | --- |
| `/help [input\|history\|tools]` | List commands or open help for a topic. |
| `/info` | Show the model, endpoint, session, tools, context, and display settings. |
| `/compact` | Update the attached notebook first, then shorten this branch's context. Works without a notebook. |
| `/handoff [versions]` | Update project notes for the next operator or record how to reconstruct supported versions. |
| `/pin TEXT` | Keep a user instruction across context windows on this branch. |
| `/pin --from REF [START END]` | Pin saved user text, optionally selecting a range of UTF-8 bytes. |
| `/pin --replace ID TEXT` | Revise a standing instruction. |
| `/pins` | List active pins and their sources. |
| `/unpin ID` | Withdraw a standing instruction. |
| `/model [NAME]` | Show the model or choose one for subsequent turns. |
| `/effort [LEVEL\|default]` | Show, set, or reset the reasoning effort. |
| `/history [N\|clear]` | List recent inputs, read entry N, or clear saved input history. |
| `/show [last [N]\|all\|system]` | Read the conversation, the last turns with tool output, the full conversation with its system prompt, or just the system prompt. |
| `/show output [N]` | Read shortened tool output N in full, or the latest. Also works at an approval prompt. |
| `/ed [N\|REGEX]` | Edit the draft, a history entry, or matching entries in `$VISUAL`, `$EDITOR`, or `vi`. |
| `/image PATH` | Attach an image to the next turn. Enter the path literally, including spaces. |
| `/images` | List pending images by number. |
| `/remove-image N` | Remove one pending attachment. |
| `/clear-images` | Remove all pending attachments. |
| `/developer TEXT` | Set a developer instruction for the next new turn. |
| `/clear-developer` | Clear the pending developer instruction. |
| `/thinking on\|off` | Show or hide returned thinking. |
| `/usage on\|off` | Show or hide reported token counts. |
| `/tool-output full\|short` | Display all tool output or just the first and last lines of long results. |
| `/bell on\|off` | Ring the terminal when a question waits or a turn ends. |
| `/exit` | Report unsent input, show the session reference, and exit. |

`/ed` requires a terminal. When the editor closes, review the draft and
press Enter to submit it. History recalls text. Attach an image again when
you want to send it as part of a new prompt. On a terminal, `/show` uses
`$PAGER` or `less` when the text exceeds the screen.

### Tool use

Start `lmc` in a terminal so you can review calls. `-tool` gives the model
`universal_command` to run a program with an argument list and `view_image`
to send a local image to the model. Commands run directly. Shell syntax
such as a pipe has meaning only if the call invokes a shell. `/info` shows
the tools available to the current run.

```sh
lmc -argo-user "$USER" -repl -tool
```

Before execution, `lmc` shows the calls for the round, including arguments,
working directories, input, and redirections. At a question, `y` approves
one call and `a` approves this and the remaining calls that would ask in
this round. `n` denies. Other text supplies a denial reason that the model
can read. Enter takes the capitalized answer shown in the prompt. At an
ordinary prompt that approves one call (`Y`) or the rest of the round (`A`).
An uncertain rerun defaults to denial (`N`).

#### Approval rules

A rule file contains one JSON value per line. Blank lines and lines starting
with `#` are ignored. `lmc` makes the decision in this order for commands,
image reads, and MCP tools.

1. Deny a blacklist match.
2. Allow a whitelist match.
3. When a whitelist was supplied and a prompt cannot be answered, deny every remaining call. An empty whitelist counts.
4. Allow a remaining call if `-tool-auto-approve` is set.
5. Ask if stdin is a terminal and prompting is enabled. Otherwise deny.

An array such as `["git","status"]` matches that exact argument prefix,
including `git status --short`. In a whitelist it allows calls with no
added environment, literal stdin, or file redirection. To allow `sort` to
read and write named files, specify them in an object rule. This example
lets a script run those two operations.

```sh
cat > checks.rules <<'RULES'
["git","status"]
{"command":["sort"],"stdin_file":"names.txt","stdout_file":"sorted.txt"}
RULES
echo "Show git status and sort names.txt into sorted.txt." | lmc \
  -argo-user "$USER" -tool -tool-whitelist checks.rules -tool-non-interactive
```

A pipe supplies the model's input and leaves no terminal on stdin for
approval. Give scripts rules for the calls they need. `-tool-non-interactive`
requires a whitelist or `-tool-auto-approve`. When a script's call misses
the whitelist, `lmc` suggests a matching rule. Review the argument prefix
and file access before adding it. [Rule syntax and examples](docs/tool-approval.md)

#### MCP servers

`-mcp-config FILE` connects tools from Model Context Protocol (MCP) servers
and enables the local tools. The file contains an `mcpServers` object with a
`command` for a stdio server or a `url` for a Streamable HTTP server. In a
REPL, the servers stay connected across turns.

Use `-tool-include` to select a built-in tool, a whole server, or a
`server/tool` pair. `-tool-exclude` removes names from the selection.
For example, this starts the sample filesystem server through `npx`,
limits the run to that server, and lets you review its calls at the terminal.
The sample gives the server your home directory as its root.

```sh
lmc -argo-user "$USER" -repl -mcp-config examples/lmc/mcp-config.json \
  -tool-include filesystem
```

Each call uses the same approval order. To preapprove one MCP operation,
name the server and its own tool, as in `{"mcp":"notebook","tool":"read"}`.
[Sample configuration](examples/lmc/mcp-config.json) · [MCP configuration fields](docs/tool-approval.md#mcp-configuration)

#### Interrupted tool rounds

If a session ends with pending tools, resume at the reported message with
the necessary tools enabled. Supply the MCP configuration again for a pending
server call. `lmc` reuses recorded results. Calls that never started and
image reads without a recorded result go through the usual approvals.
A command or MCP call that may already have run needs explicit approval
to run again. Without a terminal the model receives an unknown outcome.
[Recovering a tool round](docs/tool-approval.md#recovering-a-tool-round)

### lmc flags

#### Provider options

`lmc` and `apiproxy` accept these connection options.

| Flag | Effect |
| --- | --- |
| `-provider NAME` | Choose `argo` (default), `openai`, `anthropic`, or `google`. |
| `-api-key-file FILE` | Read the provider's credential from a file. |
| `-argo-user USER` | Supply an Argo user or key. |
| `-provider-url URL` | Override the selected provider's API URL. |
| `-models-url URL` | Set a separate URL for model discovery. |
| `-anthropic-bearer` | Also send the Anthropic key as a bearer token. |
| `-argo-dev` | Use the Argo development host. |
| `-argo-test` | Use the Argo test host. |
| `-argo-legacy` | Use Argo's legacy chat and streamchat endpoints. |
| `-chat-completions` | Use Chat Completions for OpenAI or native Argo `gpt*` where Responses is the default. In `apiproxy` this applies to Responses requests. |

#### Input and output

| Flag | Effect |
| --- | --- |
| `-model NAME` | Choose the model. Chat defaults depend on the provider. |
| `-list-models` | Print the provider's models and any capabilities supplied by its catalog. |
| `-repl` | Read successive turns into one session. |
| `-embed` | Write an embedding for the input. |
| `-no-stream` | Wait for a complete response before printing the answer. |
| `-print-curl` | Print the equivalent request, including credentials, for inspection. |
| `-system TEXT` | Set the session's system prompt, including an empty string. |
| `-builtin-system` | Restore the built-in prompt. |
| `-developer TEXT` | Save an instruction with this run's first new user turn. |
| `-effort LEVEL` | Request `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`, where supported. |
| `-hide-thinking` | Hide returned summaries on stderr. |
| `-reasoning-mode MODE` | Set Responses `reasoning.mode` to `standard` or `pro` on a model that supports it. |
| `-reasoning-context SCOPE` | Set Responses `reasoning.context` to `auto`, `current_turn`, or `all_turns`. |
| `-max-tokens N` | Limit output tokens. With `0`, Claude requests use 128000 for Opus or 64000 otherwise. Other routes use provider defaults. |
| `-json` | Request a JSON object. |
| `-json-schema FILE` | Supply a schema for JSON output. Use one JSON output mode per request. |
| `-image FILE` | Attach an image. Repeat for multiple files. |
| `-image-detail LEVEL` | Set OpenAI image detail to `auto`, `low`, or `high`. Applies to later REPL attachments too. |

#### Session and context options

| Flag | Effect |
| --- | --- |
| `-show-sessions` | List local sessions and their message trees. |
| `-show S[/M]` | Print the branch through the newest message or the specified message. |
| `-resume S[/M]` | Continue after the newest message or the specified message. |
| `-delete S[/M]` | Delete a session or a message and its descendants. |
| `-no-session` | Chat without saving a session transcript. |
| `-sessions-dir DIR` | Set the session directory. |
| `-notebook DIR` | Attach a notebook. An empty string detaches it. |
| `-notebook-view VIEW` | Offer a `catalog` at each window boundary (default) or use `tools` alone. |
| `-context-policy POLICY` | Replay the `full` branch or use a prose `handoff` when a window fills. |
| `-context-format prose` | Select the prose checkpoint format. |
| `-context-window N` | Cap the context window in tokens. `0` clears the cap. |
| `-context-trigger N` | Start compaction at this estimated input size. `0` restores the default of 70% of the window. |
| `-context-early-cuts=BOOL` | Allow a handoff at a model's phase boundary. Defaults on with a notebook in handoff mode. |

#### Tools, requests, and logging

| Flag | Effect |
| --- | --- |
| `-tool` | Enable local commands and image reads. |
| `-mcp-config FILE` | Load an MCP server configuration. Repeat to combine files. |
| `-tool-include NAMES` | Select tools to advertise. Accepts comma separated names and repeated flags. |
| `-tool-exclude NAMES` | Withhold tools from that selection. |
| `-tool-whitelist FILE` | Load rules that approve matching calls. |
| `-tool-blacklist FILE` | Load rules that deny matching calls. |
| `-tool-auto-approve` | Approve calls still undecided by the rule files. |
| `-tool-non-interactive` | Apply the approval policy without prompting. |
| `-tool-timeout DURATION` | Set the default time for one command or MCP call (`1m`). |
| `-max-tool-rounds N` | Allow `64` rounds by default. At the limit a terminal user can grant another block. |
| `-max-tool-parallel N` | Run at most this many tools concurrently (default `8`). |
| `-tool-max-output-bytes N` | Capture up to N bytes per command. `0` uses the 1 MiB default. The maximum is 100 MiB. |
| `-timeout DURATION` | Set the HTTP request timeout (default `10m`). |
| `-retries N` | Retry HTTP 408, 425, 429, or 5xx and transient connection or early stream failures (default `10` retries). |
| `-log-dir DIR` | Set the log directory. |
| `-log-level LEVEL` | Choose `DEBUG`, `INFO` (default), `WARN`, or `ERROR`. |
| `-version` | Print the build version and exit. |

## notebook

Give a project a directory of Markdown notes so another conversation can
pick up decisions, evidence, and unfinished work. Install `notebook` beside
`lmc` or on `PATH`, then attach the directory when you start or resume a chat.

```sh
lmc -argo-user "$USER" -repl -notebook ./project-notes
```

The model can `list`, `search`, `read`, and `put` notes. Attaching a notebook
enables those tools and branch history reads. Add `-tool` if the model also
needs to run local commands. When starting a new session on the same project,
pass the same directory to share its notes. When resuming, the branch already
remembers its notebook.

At a milestone, type `/handoff` to ask the model to update the notes for the
next person. `/handoff versions` asks it to record how to reconstruct earlier
project versions. Both are normal turns with tool approvals. For a standing
instruction that must survive a long conversation, use `/pin TEXT`.

Other MCP clients can launch `notebook -root DIRECTORY` and communicate over
stdio. For example, put this entry in the client's MCP configuration and
substitute an absolute directory on your machine.

```json
{
  "mcpServers": {
    "notes": {"command": "notebook", "args": ["-root", "/absolute/path/to/project-notes"]}
  }
}
```

A session normally replays its branch in full. With a notebook and a known
context window, `lmc` asks the model to summarize earlier exchanges as the
window fills. `-context-policy full` selects full replay. For a chat without
a notebook, `-context-policy handoff -context-window N` enables summaries.
`/info` shows the effective settings and `-show` still displays the original
messages. [Note format, context windows, and handoffs](docs/context-and-notebooks.md)

## ALCF Inference Endpoints

`alcfauth` signs in through Globus and writes an access token to a file
that `lmc` and `apiproxy` can read. Run the following on the machine that
will use the key, open the printed URL, and paste the authorization code
back into the terminal.

```sh
alcfauth login
alcfauth status
```

Login opens a browser when available. Over SSH, you can open the URL on a
different machine. Use `alcfauth login -no-browser` to print the URL
explicitly. Both the cache and the key file are private files (mode `0600`).

List Minerva's models first. The catalog can report input types, effort
levels, context windows, and supported APIs. Choose a model advertising
Chat Completions for this example.

```sh
cluster=https://inference-api.alcf.anl.gov/resource_server/minerva
key="$HOME/.lmc/alcf/inference.key"

lmc -provider openai -api-key-file "$key" -models-url "$cluster/models" -list-models
printf 'Model ID from the list: '
read -r model
echo "Explain PKCE in two sentences." | lmc -provider openai \
  -api-key-file "$key" -provider-url "$cluster/api/v1" -model "$model" -chat-completions
```

For a model that supports Responses, omit `-chat-completions`. To use
Anthropic Messages, select a model advertising that API and pass
`-provider anthropic -anthropic-bearer` with the same key and API URL.

| Command | When to use it |
| --- | --- |
| `alcfauth login` | Start or renew a Globus login. `-client-id ID` selects a different native app. |
| `alcfauth token` | Refresh when less than `-min-valid` (default `1h`) remains, rewrite the key file, and print the token. Use `-quiet` to suppress it. |
| `alcfauth token -watch` | Keep the key file current for a long REPL or proxy session. Run this in a separate terminal. |
| `alcfauth status -check` | Check expiry and ask the gateway whether it accepts the token. |
| `alcfauth logout` | Revoke the tokens and remove the cache entry and key file. |

All four commands take `-cache FILE` and `-key-file FILE`. If you move the
key, pass that path to `-api-key-file`. A running `lmc` reads it for each
request and `apiproxy` reloads it when it changes. The watch reports refreshes
on stderr. If it exits requesting a new login, run `alcfauth login` and
restart the watch. [Background services and proxy setup](docs/alcf-inference.md) ·
[ALCF service guide](https://docs.alcf.anl.gov/services/inference-endpoints/)

## apiproxy

Start the proxy with the provider you want your clients to reach. It listens
on `127.0.0.1:8082` by default and authenticates upstream with the credentials
you supply to the process. Anyone able to reach the listener can send
requests using those credentials.

```sh
apiproxy -provider argo -argo-user "$USER"
```

In another terminal, check the listener and send a request. These requests
use Argo's `gpt5` model. For another backend, choose a name from `/v1/models`
or configure a model map.

```sh
curl -sS http://127.0.0.1:8082/
curl -sS http://127.0.0.1:8082/v1/models

curl -sS http://127.0.0.1:8082/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt5","input":"Explain the purpose of a checksum."}'
```

To use OpenAI, Anthropic, or Google upstream, choose one of these commands.

```sh
apiproxy -provider openai -api-key-file ~/.openai-key
apiproxy -provider anthropic -api-key-file ~/.anthropic-key
apiproxy -provider google -api-key-file ~/.google-key
```

### Provider routing

`-model-map REGEX=MODEL_NAME` lets clients use a name of their choosing.
Rules run in command line order. The first match supplies the upstream
model. An unmatched name goes to the configured provider as given.

```sh
apiproxy -provider argo -argo-user "$USER" -model-map '^review$=gpt5'
```

For native Argo the model *after mapping* selects the route.

| Model at Argo | Upstream API |
| --- | --- |
| Starts with `claude` | Anthropic Messages |
| Starts with `gpt` | Responses for a Responses client or `lmc` chat. Chat Completions for Messages and Chat Completions clients, or with `-chat-completions`. |
| Other models | Chat Completions |

`-argo-legacy` selects Argo's legacy endpoints for all chat models.

### apiproxy flags

Connection flags, including `-chat-completions`, are shared with `lmc`.
[Provider options](#provider-options)

| Flag | Effect |
| --- | --- |
| `-host ADDRESS` | Bind to this address (default `127.0.0.1`). |
| `-port N` | Listen on this port (default `8082`). |
| `-model-map REGEX=MODEL_NAME` | Add an ordered model mapping. Repeat for more rules. |
| `-sessions-dir DIR` | Store converted Responses and conversations here. Give each proxy process its own directory, separate from `lmc`. |
| `-max-request-body-size N` | Limit incoming bodies in MB (default `512`, each MB is 1,048,576 bytes). |
| `-strip-encrypted-reasoning` | Drop encrypted reasoning and compaction input items when recovering a direct Responses session. |
| `-log-level LEVEL` | Choose `DEBUG`, `INFO` (default), `WARN`, or `ERROR`. |
| `-log-format FORMAT` | Write `text` (default) or `json` logs to stderr. |
| `-version` | Print the build version and exit. |

### Supported endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/` | Health. Returns `{"status":"ok","name":"lmtools-proxy"}`. |
| `GET` | `/api/hello` | Liveness. Returns `{"message":"hello"}`. |
| `GET` | `/v1/models` | List the backend's models. |
| `POST` | `/v1/messages` | Anthropic Messages, including streaming. |
| `POST` | `/v1/messages/count_tokens` | Count or estimate input tokens. |
| `POST` | `/v1/chat/completions` | OpenAI Chat Completions, including streaming. |
| `POST` | `/v1/responses` | Create a response. |
| `POST` | `/v1/responses/input_tokens` | Count input for a Responses request. |
| `POST` | `/v1/responses/compact` | Compact Responses input. |
| `GET`, `DELETE` | `/v1/responses/{id}` | Fetch or delete a response. |
| `POST` | `/v1/responses/{id}/cancel` | Cancel a response. |
| `GET` | `/v1/responses/{id}/input_items` | List a response's input items. |
| `POST` | `/v1/conversations` | Create a conversation. |
| `GET`, `POST`, `DELETE` | `/v1/conversations/{id}` | Fetch, update, or delete a conversation. |
| `GET`, `POST` | `/v1/conversations/{id}/items` | List or add items. |
| `GET`, `DELETE` | `/v1/conversations/{id}/items/{item_id}` | Fetch or delete an item. |

For example, the same listener accepts both of these requests.

```sh
curl -sS http://127.0.0.1:8082/v1/messages \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt5","max_tokens":256,"messages":[{"role":"user","content":"Hello"}]}'

curl -sS http://127.0.0.1:8082/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt5","messages":[{"role":"user","content":"Hello"}],"stream":true}'
```

Messages streams send an SSE `ping` after 15 seconds without a downstream
event once the upstream request has returned HTTP 200. The proxy has no
absolute outbound provider timeout. The HTTP listener applies 15-minute
read and write timeouts.

For `/v1/messages/count_tokens`, Anthropic and native Argo `claude*` use
Anthropic token counting, and Google uses Gemini counting. Other routes
estimate locally. A missing native count endpoint (HTTP 404) also yields
an estimate.

### Using with Claude Code and Codex

Point Claude Code at the base URL without `/v1`. For Codex, define a
Responses provider with `base_url = "http://127.0.0.1:8082/v1"` and select
it in a user profile. [Codex profile and model mapping](docs/codex.md)

```sh
ANTHROPIC_BASE_URL=http://127.0.0.1:8082 claude
```

### Responses API compatibility

With OpenAI, or with a native Argo `gpt*` model, the proxy forwards Responses
requests and streams to the provider's Responses API. Provider fields and
tools pass through. The provider determines which state operations it supports.

With Anthropic, Google, other Argo models, or `-chat-completions`, the proxy
converts requests and builds Responses output from the backend's text,
reasoning, tool calls, and usage. It stores converted responses locally by
default. `previous_response_id` can continue a saved response or branch
from an earlier one. `"store":false` disables storage for a foreground request.

Conversion supports function and custom tools. It drops unsupported hosted
tools with a warning. Prompt templates, `max_tool_calls`, and `top_logprobs`
produce request errors on converted routes. Counts can be estimates,
and compacting on a converted route asks the backend for a summary.
[Responses fields, tools, and state](docs/api-support-matrix.md#v1responses)

## Data locations

| What | Default location |
| --- | --- |
| `lmc` sessions | `~/.lmc/sessions/` |
| Tool call records, including captured output and images | `~/.lmc/sessions/.journal/` (retained after session deletion) |
| REPL input history | `~/.lmc/sessions/.repl-history` (up to 100 entries, each saved line up to 64 KiB) |
| `lmc` logs | `~/.lmc/logs/` |
| `apiproxy` Responses and conversation state | `~/.apiproxy/sessions/` |
| `alcfauth` token cache and bearer key | `~/.lmc/alcf/tokens.json` and `~/.lmc/alcf/inference.key` |
| Notebook notes | `<directory given to -notebook or -root>/<id>.md` |
| Binaries from `make build` | `bin/<goos>-<goarch>/` contains `lmc`, `apiproxy`, `alcfauth`, and `notebook`. |

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| An ALCF request gets HTTP 401 or 403 | Run `alcfauth status -check`. Refresh the key with `alcfauth token -quiet`, or run `alcfauth login` when the gateway requires a new login. |
| A tool call in a script is denied | Read the denial reason. Check the blacklist first, then compare the call's arguments, environment, stdin, and redirections with your whitelist. |
| A resumed turn failed after tools ran | Use the session and message reference printed with the failure. Empty input after saved results asks for the model's answer. |
| A model or endpoint rejects the request | Check `-list-models` and the API your model advertises. A server offering only Chat Completions needs `-chat-completions` when used as an OpenAI provider. |
| The proxy returns `413 request_too_large` | Reduce the body or raise `-max-request-body-size` to the value in the diagnostic. Allow memory for concurrent buffered uploads. |
| The provider rejects an oversized request | Shrink the input, resize the images, or start a new session. The provider enforces its own limit. |
| A stream arrives all at once | Check for buffering or compression between the client, proxy, and provider. |
| A stream ends early | Inspect the terminal error event and proxy logs. A client disconnect or write failure can interrupt delivery. On an incomplete turn `lmc` retains partial text, discards incomplete tool calls, and reports failure. |
| A direct Responses backend cannot decrypt resumed input | Retry with `-strip-encrypted-reasoning` if you can discard the affected opaque context. The proxy removes whole encrypted reasoning and compaction items. |

`-log-level DEBUG` enables wire diagnostics. They contain full requests,
responses, stream data, and credential headers. Treat these logs and printed
`curl` commands as credentials when sharing a reproducer.
