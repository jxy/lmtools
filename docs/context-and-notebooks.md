# Context and project notebooks

A notebook belongs to a directory and can be shared by sessions. A session
keeps the conversation, including the original messages behind a summary.
Use a notebook for decisions another session needs to find. Use a pin for an
instruction that should appear verbatim in each new context window on a branch.
[Starting a notebook](../README.md#notebook) · [Resuming a branch](../README.md#sessions)

## Notes on disk

`notebook -root DIR` serves Markdown files over MCP stdio. The four tools
work with a note ID, the filename without `.md`. IDs are 1 to 64 characters
and begin with a lowercase ASCII letter or digit. The remaining characters
can also include `_` and `-`.

| Tool | Inputs and result |
| --- | --- |
| `list` | Page through note IDs, titles, summaries, and revisions with `offset` and `limit`. |
| `search` | Supply a word query and page through matching headers. |
| `read` | Supply an ID. For later pages, pass the returned revision and byte offset. |
| `put` | Send a complete note with `id`, `title`, `summary`, `body`, `sources`, and `expected_revision`. Use `"new"` to create it or the revision you read to replace it. |

You can edit a note with a text editor. Save this example as `build-output.md`,
including the blank lines separating the title, summary, and sections.

```markdown
# Build output

Where to find the executables for a given platform.

## Note
Run `make build` to write the programs under `bin/<goos>-<goarch>/`.

## Sources
- Makefile build target
```

Keep the whole file within 32 KiB. The title is one line of up to 160 bytes
and the summary one line of up to 400 bytes. Include 1 to 64 sources, each
a single line of up to 512 bytes. A source can name a file, a decision,
or an exact block of the saved conversation.

A write creates a missing root directory with mode `0700`. New files use
`0666` subject to your umask. A revision keeps the note's mode and group
when possible. If the writer cannot retain the group, group and other
permissions are limited to the access both had before. Set directory and
file permissions deliberately if several accounts share a notebook.

If another writer revised the note first, read its revision and incorporate
its changes before retrying `put`. A malformed or oversized note appears
under `invalid_notes`. `read` returns the original in pages and supplies
the revision needed to repair it. Binary repair pages use `base64`.
`unreadable_notes` lists notes the server lacks permission to open.

## What the model sees

`-notebook DIR` exposes notebook and history tools. Each tool call needs
approval under the same rules as an external MCP call. A script that should
read but never revise a note can include just `notebook/list`,
`notebook/search`, and `notebook/read` and whitelist those operations.
[Selecting and approving tools](tool-approval.md#image-and-mcp-rules)

`-notebook-view catalog` supplies a bounded list of note IDs, titles,
summaries, and revisions at the start of a window. Edits appear in tool
results immediately and in the next window's catalog. Selecting only
some tools still supplies the catalog. Choose `-notebook-view tools` to
expose the notes only through tool calls, including on resume.

The first window can omit a catalog that has never been sent if there
is nothing to compact and the request would otherwise exceed its budget.
`lmc` reports that choice. Once sent, the catalog stays in the window.
Under full replay a request that can no longer fit with it reports the size.

`/handoff` asks for a notebook update a new operator can use, including
outstanding work and evidence. `/handoff versions` asks for a baseline and
the changes needed to reproduce supported versions. Each sends a normal
user turn. For batch use, feed the corresponding prompt to `lmc`.
[Handoff prompt](../internal/prompts/notebook_handoff.txt) · [Version history prompt](../internal/prompts/notebook_versions.txt)

## Context windows

| Setting | What goes into the request |
| --- | --- |
| Ordinary chat | Full history of the selected branch. |
| `-context-policy full` | Full branch, with the selected notebook view if attached. |
| `-notebook DIR` with the default policy | Handoffs when a window size is known. Otherwise `lmc` reports that it will use full replay. |
| `-context-policy handoff` | The run's model and effort produce a prose checkpoint from completed exchanges. Requires a known window size. |

A checkpoint summarizes earlier decisions, evidence, and open work. Later
requests carry it along with recent exchanges and standing instructions.
The original messages remain available through `-show`. A single
`-no-session` run keeps its history and checkpoints in memory. An explicitly
attached notebook still stores notes in its directory.

A run takes the newest capacity recorded for the same model and endpoint
on this branch, or looks in the provider's model catalog. `-context-window N`
caps the window at the smaller of that number and the known capacity.
When only one is known it supplies the window. For example, an 8192-token
cap and a 4096-token model give a 4096-token window. A nonzero cap must
be between 2048 and 1,000,000,000 tokens. Use `/info` to inspect the
effective policy, trigger, and input ceiling.

```sh
lmc -argo-user "$USER" -repl -context-policy handoff -context-window 32768
```

By default a handoff is due at about 70% of the window. The input ceiling
reserves one eighth for output, reasoning, and estimation error. Set
`-context-trigger N` below that ceiling to change the trigger. Notebook
handoff sessions also let the model request an earlier cut at a completed
phase. `-context-early-cuts=false` uses the size trigger alone.

The estimator accounts for instructions, tools, and retained reasoning.
It budgets 2048 tokens per image, including images returned by tools, and
32 tokens per second of recorded audio (2048 if the duration is unknown).
When the estimate would refuse a long text exchange, `lmc` can make sizing
requests for groups of complete exchanges, or measure large text fields in
fragments. Sizing requests never execute tools. Missing usage or a transient
failure leaves `lmc` using its estimate. HTTP 401 or 403 during sizing stops
the run so you can restore credentials and resume.

## History and pins

`history/search` and `history/read` retrieve visible text and attachment
metadata from the active branch, including earlier windows. Search returns
block references and indicates whether more matches exist. Read accepts a
reference and byte offset. A citation into another session can be resolved
by resuming that session. A shared notebook alone grants no access to it.

```sh
lmc -argo-user "$USER" -repl -resume 0001 -context-policy full -tool-include history
```

In a saved REPL session with a notebook, type `/pin TEXT` for a new
standing instruction. To mark existing user
text, use `/pin --from REF [START END]` with its original `message.block`
reference. START is inclusive and END exclusive, both in UTF-8 bytes on
character boundaries. `/pins` shows IDs and sources. `/pin --replace ID TEXT`
and `/unpin ID` revise or withdraw one. A new branch inherits pins from its
parent. Each new window carries the exact active text in the user role.
You can override it for one reply with a later request.

## When a handoff or tool result will not fit

A handoff during a tool turn keeps the active request and the completed
batch's observed results. If a result exceeds the input budget and history
reads are enabled, the new context includes its source reference, byte
length, and excerpts. The model can page through the saved text. If a later
run disables history reads, only the excerpts are available and `lmc` says
so. History has only the bytes originally captured. Increase
`-tool-max-output-bytes` before running the command if you need more.

If the writer fails, `lmc` keeps the preceding context while it fits. Once
it exceeds the ceiling, a run with access to archived history can continue
with references to the original records and reports the fallback. At a later
cut it tries to replace those references with a prose handoff, starting from
the last usable summary or the original records if that summary takes too
much space. A run without history reads needs a fitting handoff. Protected
input that still exceeds the ceiling produces an error with its estimate.

A retry can reuse a saved checkpoint for the same sources, instructions,
writer, window, and trigger. Changing the budget can make a later cut rebuild
from the original records. The transcript keeps the checkpoint used by a
saved reply, which remains usable while the next request fits.

`-print-curl` previews the request without calling the model. Configured
MCP servers still connect for tool discovery. If no reply recorded the model's
capacity, supply `-context-window N` or select `-context-policy full`. A
preview that requires a new model-written checkpoint or a live measurement
reports what is missing.

## Changing a saved session

A switch to a different policy resets the format and early cuts to their
defaults unless supplied again. A switch to full replay clears the previous
compaction trigger. `-context-trigger 0` restores the default trigger and
`-context-window 0` removes the cap. Use `-notebook ''` to detach the
directory and its catalog. Session settings apply forward from the chosen
message, so a sibling branch can use a different notebook or policy.

If `lmc` reports experimental context settings on resume, choose
`-context-policy handoff` for prose checkpoints or `-context-policy full`
to replay the branch. A session listed as an unreadable format supports
listing and deletion. Move its numbered directory out of the session store
to retain a copy.
