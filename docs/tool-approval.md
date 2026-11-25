# Tool approval and MCP configuration

Use `-tool-whitelist FILE` for calls you have reviewed and
`-tool-blacklist FILE` for calls to deny. When a call misses the whitelist
and prompting is unavailable, `lmc` prints a matching rule for review.
Each nonblank line in either file is one JSON value. Lines starting with
`#` are comments. [Approval order and a batch example](../README.md#approval-rules)

## Command rules

`universal_command` starts a program with the argument vector the model
supplies. Every command rule matches an argument prefix, one complete
argument at a time. For example, `["git","status"]` matches
`["git","status","--short"]`. Further arguments are unrestricted by that
prefix. Matching uses the executable spelling and arguments in the call.
A command blacklist alone cannot bound everything a program can do.

| Rule | In a whitelist | In a blacklist |
| --- | --- | --- |
| `["sort"]` | Allow that prefix with no literal stdin, added environment, or redirection. Any working directory matches. | Deny that prefix with any working directory, input, environment, or redirection. |
| `{"command":["sort"],"stdout_file":"sorted.txt"}` | Allow that prefix with exactly this output path, the default working directory, and no other input, redirection, or added environment. | Deny that prefix whenever it writes stdout to this path, including calls with other fields. |
| `{"command":["make"],"environ":{"CC":"clang"}}` | Allow that prefix with exactly this environment addition. | Deny that prefix when it adds this variable with this value, including alongside other variables. |

### Command object fields

| Field | Value and matching |
| --- | --- |
| `command` | Required nonempty array of strings. Both lists treat it as an argument prefix. |
| `workdir` | Working directory as supplied by the call. Can be pinned alongside an input, redirection, or environment field. |
| `environ` | Map of variable names to string values that the call adds or overrides. |
| `stdin` | Boolean permission for literal input. In a whitelist, `true` allows any literal input for this prefix, including a call with no literal input. Omitted or `false` requires absence. In a blacklist, `true` matches input present and `false` matches input absent. |
| `stdin_file` | Path to use as standard input. A command can use either a literal `stdin` string or a file. |
| `stdout_file` | Path to receive stdout. |
| `stderr_file` | Path to receive stderr. |

An object needs `command` plus at least one of `environ`, `stdin`,
`stdin_file`, `stdout_file`, or `stderr_file`. An object containing only
`command` and `workdir` fails to load. File paths must be nonempty. The
loader rejects unknown fields. A call can set its own `timeout` in seconds,
clamped to 24 hours. Timeout is an execution option outside the rule syntax.

For an object grant, the working directory and each redirection must match
exactly. An omitted path requires the call to omit it too. Paths are compared
as written and relative paths open under the call's working directory.
Environment additions must match the whole map, including values. An absent
or empty map grants zero additions. Literal input permission applies to
every string, including an empty string. Consider what the selected program
can do with arbitrary stdin before granting it.

For an object denial, every named condition must match. The call can carry
other redirections or environment variables. A denial with no `workdir`
condition matches in any working directory. An array denial covers every
call with that argument prefix.

For example, this rule allows `sort` to read `unsorted.txt` and write
`sorted.txt` with the given working directory.

```json
{"command":["sort"],"workdir":"/tmp/job","stdin_file":"unsorted.txt","stdout_file":"sorted.txt"}
```

The sample files include both command forms and rules for sending images
and invoking server tools. [Example whitelist](../examples/lmc/tool-whitelist.txt) · [Example blacklist](../examples/lmc/tool-blacklist.txt)

### Files and output

A redirection accepts a regular file or one of the permitted character
devices, `/dev/null`, `/dev/zero`, `/dev/full`, `/dev/random`, or
`/dev/urandom`. `lmc` refuses a symlink as the final path component, a FIFO,
or another device. Regular output files are truncated after the redirections
open. Even a command that fails can leave an existing output file empty.
If setup created a new output file and no process started, `lmc` removes
that file when it can.

Two calls in one round cannot write the same regular file, or read a regular
file another call writes. `lmc` denies each call involved in a collision.
Permitted devices can be shared. For example, several calls can redirect
output to `/dev/null`. A call can send stdout and stderr to the same file.

Streams without a redirection go into the tool result. The capture limit is
`-tool-max-output-bytes` per command, with a 1 MiB default and a 100 MiB
ceiling. Reaching the limit truncates the captured data and the model sees
a truncation note. A shortened terminal display can still have the complete
captured text. Use `/show output N` to read it, including at the next
approval question. The REPL keeps up to 64 MiB of shortened outputs for
this command. `/show last` reads tool output from the saved turn.

## Image and MCP rules

```json
{"tool":"view_image","path":"plots"}
```

This rule matches the image file or directory `plots`, including files
below the directory. A `view_image` rule accepts an optional `path`.
Omitting it matches every image. Paths are resolved against the working
directory and cleaned for this comparison. `detail` has no effect on the
match. An array names a command, so an image permission uses the object
form. The same rule in a blacklist denies the matching images.

Approving `view_image` sends the image bytes to the model and saves them
in the tool result. The tool accepts PNG, JPEG, GIF, and WebP regular files
with a maximum of 5 MB on Anthropic routes and 20 MiB elsewhere. A symlink
as the final component is refused. Legacy Argo does not offer the tool.

```json
{"mcp":"notebook","tool":"read"}
```

An MCP rule matches the server name and, if supplied, its own tool name.
A rule naming only `{"mcp":"notebook"}` matches all that server's tools,
including `put`. Use individual `list`, `search`, and `read` rules to grant
reads, and grant `put` separately for writes. A tool rule applies to all
arguments of the operation. The model sees a name such as
`mcp__notebook__read`, while the rule uses `notebook` and `read`. The
`history` tools accept the same kind of rule. [Notebook and history access](context-and-notebooks.md#history-and-pins)

## MCP configuration

`-mcp-config FILE` loads the `mcpServers` member of a JSON object. Repeat
the flag to combine files with distinct server names. For example, this
configuration starts a local notebook and describes a remote server.
Substitute the remote endpoint and set `DOCS_TOKEN` before use.

```json
{
  "mcpServers": {
    "notes": {
      "command": "notebook",
      "args": ["-root", "${HOME}/project-notes"],
      "includeTools": ["list", "search", "read"]
    },
    "docs": {
      "url": "https://mcp.example.com/mcp",
      "headers": {"Authorization": "Bearer ${DOCS_TOKEN}"},
      "timeout": 120000,
      "optional": true
    }
  }
}
```

| Field | Meaning |
| --- | --- |
| `command`, `args`, `env`, `cwd` | Start a stdio program directly, with these arguments, environment additions, and working directory. |
| `url`, `headers` | Contact a Streamable HTTP endpoint. `httpUrl` is also accepted. Supply any required authorization in the headers. |
| `type` | `stdio` or `http`, inferred from `command` or `url` when omitted. Specify one transport per server. The `sse` transport is unsupported. |
| `timeout` | Time limit per tool call in milliseconds. Defaults to `-tool-timeout`. |
| `startupTimeout` | Time limit for connecting and discovering tools, in milliseconds. Default `30000`. |
| `includeTools`, `excludeTools` | Lists of the server's own tool names. Exclusions apply after inclusions. |
| `optional` | Skip the server with a warning if it fails to start. A required server's startup failure ends the run. |

`${VAR}` and `${VAR:-default}` expand in `command`, `args`, `env`, `cwd`,
`url`, and `headers`. An unset variable needs a default. Unknown server
fields produce warnings. A server name shared by two files is an error.

Servers connect at startup. A selected stdio subprocess stays available
across REPL turns and Ctrl-C during a turn. On exit `lmc` closes its stdin
and terminates the server process group. HTTP servers use the configured
headers for authentication. Tool results deliver text and supported images
to the model. Timeouts, server errors, and unsupported requests for user
input produce an error result the model can read.

Apply `-tool-include` and `-tool-exclude` after choosing the configuration.
Both accept comma separated lists and repeated flags. Use a built-in tool
name, a server name for all its tools, or `server/tool` for one tool. A
whole server left out of the selection never starts. Per-tool selectors
are checked against what the server offers after its own filters. An
excluded tool takes precedence over an inclusion. Keep at least one tool
in the selection. [Interactive MCP example](../README.md#mcp-servers)

## Recovering a tool round

When a process stops with calls pending, resume with the same tools,
including the configuration for any MCP server it needs. `lmc` resolves
each pending call before it sends a follow-up to the provider.

| Recorded state | What happens on resume |
| --- | --- |
| Finished | The saved outcome is reused. |
| Never started | The ordinary approval policy applies before execution. |
| Command or MCP call may already have run | The terminal asks whether to run it again. Enter declines. Without an answer, the model receives an unknown outcome. |
| Image read without a recorded result | The ordinary image approval policy applies before reading the file again. |

A whitelist or `-tool-auto-approve` cannot authorize an uncertain rerun.
At an approval prompt, `/help` explains the answers, `/show output [N]`
displays a previous result, and `/ed` lets you edit a denial reason.
A reply starting with `//` sends a reason beginning with `/`. Input already
queued before the question cannot approve a call.

The invocation journal under the sessions directory holds one record per
call. Concurrent runs that resume the same call there can share its outcome.
`-delete` leaves the journal in place, including captured output and images.
If `lmc` reports that it could not write a journal record, check the saved
turn before deciding whether to repeat a command.

After resolving the batch, `lmc` saves the tool results before making the
next request. If that request fails, resume at the reported results with
empty stdin to ask for the answer. A pending notebook call that still needs
to run requires its original notebook directory. If a pending batch needs
other tools, enable them on resume.

A preview with `-resume REF -tool -print-curl` substitutes placeholder
results for pending calls. The calls are still pending when you resume.
