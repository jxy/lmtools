# lmtools

`lmtools` builds three binaries:

- `lmc`: a command-line client for chat, embeddings, tool calls, and local session history.
- `apiproxy`: an HTTP proxy that serves Anthropic-compatible, OpenAI Chat
  Completions-compatible, and OpenAI Responses-compatible endpoints over
  Anthropic, OpenAI, Google, or Argo backends.
- `alcfauth`: a Globus login for the ALCF Inference Endpoints that keeps an
  access token in a key file for `lmc -api-key-file`.

**Disclaimer**: LLMs (GPT, Claude, Gemini, and others) "vibe-coded" almost all
code in this repository. Use it at your own risk and review it before you deploy
to production.

## Install

Requires Go 1.21.13 or later.

```bash
make build
```

`make build` writes `lmc`, `apiproxy`, and `alcfauth` for the host into
`./bin/<goos>-<goarch>/`, such as `./bin/linux-amd64/`. Set `GOOS` and `GOARCH`
to cross-compile; each platform gets its own directory. The Makefile works with
both BSD make and GNU make.

Prebuilt binaries for linux/amd64, freebsd/amd64, and macOS/arm64 are attached
to each [GitHub release](https://github.com/jxy/lmtools/releases) as
`lmtools-<tag>-<goos>-<goarch>.tar.gz`, with a `SHA256SUMS` file beside them.
Each archive holds `lmc`, `apiproxy`, `alcfauth`, `LICENSE`, and this README.

The examples below run the binaries by name. Add the directory that
holds them to your `PATH`, or copy them into a directory already on it.

`lmc -version`, `apiproxy -version`, and `alcfauth -version` print the version the go command
stamped at build time: the release tag for a release archive, or a
pseudo-version naming the commit for a build from an untagged checkout.

## lmc

For chat and embeddings, `lmc` reads the input from stdin and writes the result
to stdout. Typed at a terminal, the input ends with Ctrl-D at the start of a
line, and Ctrl-C ends the run. `lmc` takes no positional arguments: a word
left after the flags, such as a prompt or the `false` of `-tool false`, is
an error, since every flag after it would go unread. Give a boolean flag its
value with `=`, as in `-tool=false`.

### Chat

```bash
# Chat with the default Argo provider. The answer streams to stdout, and
# provider-returned thinking summaries print on stderr.
echo "Tell me a story" | lmc -argo-user "$USER"

# Wait for the whole answer, and hide the thinking.
echo "Tell me a story" | lmc -argo-user "$USER" -no-stream -hide-thinking

# Ask for reasoning summaries. Argo's gpt* models, such as the default gpt5,
# return them through the Responses API, which lmc uses for them, when the
# request sets an effort.
echo "Solve this carefully" | lmc -argo-user "$USER" -effort high

# Ask about an image. Repeat -image to attach several; the prompt may be empty.
echo "What is wrong in this screenshot?" | lmc -argo-user "$USER" \
  -image screenshot.png

# Use OpenAI.
echo "Explain quantum computing" | lmc \
  -provider openai \
  -api-key-file "$HOME/.openai-key" \
  -model gpt-5

# Use OpenAI's Chat Completions API instead of the Responses API.
echo "Return three bullet points" | lmc \
  -provider openai \
  -api-key-file "$HOME/.openai-key" \
  -chat-completions

# Use Argo's chat completions route for a gpt* model.
echo "Return three bullet points" | lmc \
  -argo-user "$USER" \
  -model gpt5 \
  -chat-completions

# Print the equivalent curl command instead of sending the request.
echo "Explain quantum computing" | lmc \
  -provider openai \
  -api-key-file "$HOME/.openai-key" \
  -print-curl

# Generate embeddings. Embed mode turns off session tracking.
echo "Hello world" | lmc -argo-user "$USER" -e
```

When a response reports token counts, `lmc` prints them to stderr as soon as
the response is handled — in tool mode that is before any approval prompt or
command execution, and again after each follow-up round:

```text
Tokens: input 100 (cached 40) | output 19 (reasoning 12) | total 119
```

Cache and reasoning counts appear when the provider reports a non-zero value;
total counts appear whenever reported. A response whose counts are all zero,
as a provider reports for a prompt it refused, gets no line. Parentheses identify counts explicitly
reported within input or output token details; separate counts keep their reported
scope. Counts are not added together to invent a total. Responses without counts
print nothing. REPL tool follow-ups are labeled `Tokens (response N)`, and
`/usage off` hides the counts. Stdout stays the model output alone.

### Sessions

`lmc` stores each exchange in a session tree under `~/.lmc/sessions`. A path
addresses either a session (`0001`) or one message inside it (`0001/0002`).

```bash
# List saved sessions.
lmc -show-sessions

# Show a session or a single message.
lmc -show 0001
lmc -show 0001/0002

# Continue a session.
echo "Continue" | lmc -argo-user "$USER" -resume 0001

# Branch from a message.
echo "Try a different answer" | lmc -argo-user "$USER" -branch 0001/0002

# Delete a session, branch, or message together with its descendants.
lmc -delete 0001

# Answer once without writing session files.
echo "One-off question" | lmc -argo-user "$USER" -no-session
```

`-show-sessions`, `-show`, and `-delete` touch only local files, so they need no
provider credentials.

A resumed session keeps the system prompt it started with, and a branch keeps
the prompt of the session it branched from. A different `-s` forks the
conversation into a new session under that prompt. So does `-tool` on a
session that uses one of the built-in prompts when this run advertises a
different tool set, because the built-in tool prompt follows the tools.

An image attached with `-image` is saved inline in the session, so resuming or
branching the session sends it to the provider again. `-show` lists each one
as `[image: name (media type, size)]` after the message text and never prints
the image bytes. An image the model loaded with `view_image` is saved the same
way, inside its tool result, and listed under that result.

A call the model made to an MCP server is recorded with the server and tool
name, which `-show` prints under the call. Resuming a session with such a
call pending needs the `-mcp-config` that defines the server.

#### Interrupted tool rounds

When a turn is cancelled while tools run, `lmc` still saves the results of
the calls that finished and marks the ones that were interrupted or never
started, so the session is left with no round half done. If the process is
killed or its terminal closes, the session can instead end with tool calls
still pending. Resuming it with `-tool` resolves them before anything is
sent:

- A call that finished has its outcome on record, and `lmc` sends that
  outcome instead of running the call again.
- A call that never started runs, with the usual approval.
- A call that may already have run is not run again automatically. That is
  a call that started without recording an outcome, or one saved by an
  older `lmc`, which recorded nothing. Interactive `lmc` shows the call and
  asks `Run universal_command again? It may already have run. [y/N or reason; /ed to edit]`.
  Neither `-tool-auto-approve` nor a whitelist answers that question.
  Without a terminal, or with `-tool-non-interactive`, the model is told
  the outcome is unknown.

If the request that follows fails, run the same `-resume` again. It sends
the saved results and runs nothing, and it accepts empty input for that. If
`lmc` cannot write one of the records described next, it still saves the
results, then stops with an error naming the call: another copy of the
session would find that call's outcome unknown.

These records live under `~/.lmc/sessions/.journal`, one per tool call. A
branch or a copy of a session finds the same record, so a call runs once
even when two runs resume the same session: the second waits for the first
and then uses its outcome. The records hold command output and images and
can grow large, and `-delete` leaves them in place. Older `lmc` builds
ignore them, so an older build should not resume a session whose pending
tool calls a newer build handles.

If another run adds messages to a session while a turn is running, the turn
keeps its messages apart from them. It continues in a new session copied
from the conversation it had seen, and `lmc` prints a note naming the new
session.

### Conversation Loop

`lmc -repl` holds a conversation in one process. It reads one turn per line
from stdin until stdin ends, and each turn continues from where the previous
one left the session, so MCP servers start once and stay up across turns.

```bash
lmc -argo-user "$USER" -repl
lmc -argo-user "$USER" -repl -resume 0001 -tool
lmc -argo-user "$USER" -repl < questions.txt > answers.txt
```

- A line ending in `\` continues the turn on the next line; `\\` at the end
  of a line puts one backslash there and ends the turn. An empty line at the
  prompt sends nothing unless a draft or images are pending. A turn larger
  than 10 MiB (10,485,760 UTF-8 bytes, including newlines) is discarded with a note.
  In terminals supporting bracketed paste,
  pasted text is one editable multiline draft, and Enter sends it. Text you
  paste or recall is sent exactly as it is, and the keys you type work as
  usual. A `\` you type at the end of a line continues the turn, after a
  paste or a recall as well, while a backslash that came with the text stays
  in it: to continue after a paste that ends in `\`, type one more. A paste
  is never a command, `/ed`, or a `//` escape, even when it starts with `/`;
  a `/` typed in front of it still makes a command. A recalled command runs
  again as it ran. Pasted CRLF and CR line endings become LF before the
  size check, so each newline counts as one byte.
  Unbracketed pasted lines and piped input remain one
  turn per line.
- On a terminal the prompt is `you> `, and `...> ` on a continuation line,
  with an empty line above each fresh prompt. It goes to stderr, or to the
  terminal when stderr is redirected, with the notes, thinking summaries, tool
  transcript, approval questions, and fatal errors. A line longer than the
  terminal wraps across rows as it is typed, and when a turn is sent the
  whole of it is printed, so the scrollback holds what was sent. With
  terminal editing enabled, lines typed while a turn runs stay hidden until
  the next prompt; approval questions discard them. Startup shows the model,
  the reasoning effort, the display settings, and the tools with the directory
  their commands run in.
  `Assistant:` labels answers. Thinking sits between `--- thinking ---` and
  `--- end thinking ---`, and on a terminal each of its lines starts with `| `
  and wraps at the terminal width, so it cannot be mistaken for the answer.
  A status line says what a turn is waiting for, with the seconds so far:
  `Waiting for response...`, `Thinking...` while the model thinks without
  showing text, `Writing tool call NAME...` while it writes a call's
  arguments, and `Running NAME...` while commands run. It appears only on an
  empty line and clears when output starts. Retries show their delay,
  attempt count, and reason, such as `after HTTP 503` or the error a stream
  reported; a reason too long for the line keeps its first sentence, then
  its start and end with `...` between. The usage line of each response gives its time, and in a turn
  with tool calls every response is numbered. The session ID appears once
  established. `/info` shows the endpoint, API, session, tools, and settings.
  All UI text is plain: no colors or styling. Cursor controls are used only
  on an interactive terminal; `TERM=dumb` keeps basic line input and prints
  waiting and retry notices as ordinary lines.
- Ctrl-D on an empty prompt ends the loop; with text on the line it deletes
  the character under the cursor, as readline does. Ctrl-D on the empty line
  of an unfinished turn, after a continuation, ends the loop without sending
  it, with a note. On exit the loop names the session and the `-resume` that
  continues it. Ctrl-C during a turn cancels the turn and returns to the
  prompt, noting what the turn saved: an answer that had begun is kept, the
  prompt with it, so the next turn can refer to it; a turn cancelled before
  any answer text saves nothing. Ctrl-C at an idle prompt says how to exit,
  and a second one in a row exits. Any line typed in between,
  even an empty one, starts the count again. Before the first prompt, while
  `lmc` starts and connects MCP servers, Ctrl-C ends the run with status
  130. SIGTERM or a hangup cancels the turn and exits once it has saved what
  it must.
- With piped input, failed, cancelled, or incomplete turns are reported and
  later turns still run. At EOF or `/exit`, the process exits nonzero if any
  turn failed. Interactive sessions can recover from a failed turn and exit
  normally.
- With stdout a terminal, it carries the answers alone, and the prompt starts
  below an answer after an empty line. With stdout redirected, each turn's
  answer ends with a newline and an empty line separates each answer from
  the next, the answers of a turn's tool rounds included. This is formatting
  for a reader, not a record format, since an answer can hold empty lines
  itself; the session holds both sides, and `-show` prints them.
- The first turn that saves anything settles where the conversation goes:
  a new session, or the one `-resume` or `-branch` names. A turn that fails
  before saving anything leaves that choice for the next turn. `-branch` on
  an assistant message regenerates it before the first prompt, and a resumed
  session with pending tool calls resolves them first. Images from `-image`
  go with the first turn whose prompt is saved, so unlike a single run,
  `-repl` accepts them with a regeneration, which goes out without them.
- If another run changes the session, the loop continues in a fork, as a
  single run does, and names it. If an answer that was shown could not be
  saved, the loop stops rather than continue from a session without it.
- `-repl` streams as a single run does, so `-no-stream` keeps
  streaming off. It cannot be used with `-e`, `-print-curl`,
  `-show-sessions`, `-show`, `-delete`, `-list-models`, or `-no-session`.
  With a terminal on stdin it needs to open that terminal again by its
  name, and stops at startup, naming the terminal and the reason, when it
  cannot.

Left/Right move the cursor; Home/End or Ctrl-A/Ctrl-E move to line ends.
Backspace/Delete erase, Ctrl-W erases a word, Ctrl-U clears input, and Ctrl-K
erases from the cursor to the end of the draft. Up/Down recall prompts and REPL
commands, including those of earlier REPLs, and restore an unsent draft when
returning past the newest entry. The last 100 entries are kept in
`.repl-history` in the sessions directory, readable by you alone; an entry
whose saved line would pass 64 KiB is kept for the current REPL only. Ctrl-R
searches history: type or paste a query, and press Ctrl-R again to find an
older match.
Enter accepts a match for editing, then another Enter sends it; Escape restores
the draft and cursor. Oversized queries leave the previous query unchanged.
Recall accepts prompts through the same 10 MiB limit as their original submission;
the protective slash added to recalled literal `/` input does not count.
If a draft exceeds the input limit, Ctrl-U clears it so you can start again;
Enter discards the oversized submission, including its continuation lines.
A multiline draft shows its current line while editing. Resizing the terminal
repaints the input, including history search and approval answers, without
changing or submitting it. Ctrl-C discards unfinished input without sending it.
This also discards unread input already buffered by the editor, so a cancelled
draft cannot reappear as a new prompt.
Ctrl-\ exits after cancelling active work and restoring the terminal.

REPL commands work with terminal and piped input:

| Command | Effect |
| --- | --- |
| `/help [input\|history\|tools]` | Show commands or help for a topic. |
| `/info` | Show the connection, display settings, tools, session, logs, and pending input. |
| `/model [NAME]` | Show the model, or change it for the turns that follow. |
| `/effort [LEVEL\|default]` | Show the reasoning effort, or change it for the turns that follow. |
| `/history [N\|clear]` | List retained prompts, show prompt N in full, or empty the history, saved history included. |
| `/show` | Show the saved conversation through the current message, the system prompt as a one-line summary. |
| `/show last [N]` | Show the last turn, or the last N, with tool output in full. |
| `/show all`, `/show system` | Show the conversation with its system prompt in full, or the system prompt alone. |
| `/show output [N]` | Show a shortened tool output in full: number N from the note under it, or the latest; also works at an approval question. |
| `/ed` | Edit the current draft with `$VISUAL`, else `$EDITOR`, else `vi`. |
| `/ed N` | Edit retained prompt N as a new draft. |
| `/ed REGEX` | Edit all matching retained prompts together, in history order. |
| `/image PATH` | Queue an image for the next turn; the path is literal and may contain spaces. |
| `/images` | List pending attachments with numbers. |
| `/remove-image N` | Remove one pending attachment. |
| `/clear-images` | Remove all pending attachments. |
| `/developer TEXT` | Set a developer instruction for the next turn, replacing any pending instruction. |
| `/clear-developer` | Remove the pending developer instruction. |
| `/thinking on\|off` | Show or hide returned thinking; does not enable reasoning. `/show` includes saved thinking while it is on. |
| `/usage on\|off` | Show or hide reported token counts. |
| `/tool-output full\|short` | Show long tool output in full, or its first and last lines (the default on a terminal). |
| `/bell on\|off` | Ring the terminal when an approval question waits and when a turn ends. |
| `/exit` | End the conversation, report unsent drafts, images, or instructions, and name the session to resume. |

`/ed` requires a terminal. It waits for the editor, shows a draft preview after
a successful exit, and returns to input. Enter sends the draft; ordinary text
extends it before sending, and a trailing backslash continues input as usual.
Use `/ed` again to revise it. Failed edits keep the previous draft. Configure
GUI editors to wait until editing is complete. Input queued before or during
editing is discarded so accepting the edited draft requires a fresh Enter.

An argument containing only digits selects a number from `/history`. Other arguments
are Go regular expressions matched against the full retained prompt text.
Matches are joined with blank lines into one draft. Use a pattern such as
`[4]2` to search for numeric text rather than select history entry 42. Invalid
expressions and no matches leave the pending draft intact.
History selection recalls prompt text only. Text entered after editing is
appended verbatim to the draft; include spacing or a continuation newline as
needed. A bare `/ed` during continuation opens the unfinished text; `//ed`
in that position inserts a literal `/ed` line.

Start a prompt with `//` to send a literal leading `/`. Image paths in `/image`
use no quotes, shell expansion, or escaping. Press Enter after attaching an
image to send it without text. History recalls prompt text; it does not attach
the earlier images again. New attachments remain pending until their user
turn is saved, including across a failed request. Pending state appears above
the prompt. Conversation inspection and all UI messages use diagnostics;
redirected stdout keeps the answers.
Developer instructions can be added during the conversation. They are saved
with the next text or image turn and replayed on resume; `/clear-developer`
affects only an instruction that has not yet been sent. Use continuation lines
for a developer instruction spanning multiple lines.

Saved conversations and live tool turns use the same tool/result labels and
readable durations, with each line of output quoted under its result. A round
of tool results is labelled `[tool results]`. Live output longer than about
twenty lines shows its first and last ten, and a line over 2000 bytes is cut
with a count of the bytes left out. The note under a shortened output names
the command that prints it whole, as in
`[281 lines not shown; /show output 3 prints them]`; the REPL keeps the whole
text of every shortened output, up to 64 MiB in all, the oldest dropped
first. `/show output 3` works at the prompt and at an approval question, so
an output can be read before the next call is approved. `/show` and
`/show last` never shorten saved tool output, and any truncation done during
execution is identified separately. On a terminal, `/show` and
`/show output` open in `$PAGER`, else `less`, when the text is taller than
the terminal; quitting the pager returns to the prompt or the question.

Answers are shown as the model wrote them, markdown included, since stdout
keeps the answer bytes exactly. To ask for plain text, add an instruction:
`/developer Answer in plain text without markdown.`

### Tool Use

`-tool` enables the built-in `universal_command` tool. It executes each requested
argument vector directly with `execvpe`-style semantics, without a shell. A
whitelist match is already an approval: the matching command runs without a
prompt, and `-tool-auto-approve` is not needed.

A round's calls are all shown before the first question, and each question
names the call it is about, for example `[2/3] Allow grep?`. At a tool
approval prompt, `y` or `yes` approves; `n` or `no` denies.
`a` or `all` approves this call and every later call in the round that would
ask; the question offers it when there are such calls, and at any other
question it approves the one call. Enter alone gives the answer in capitals:
`[y/n/A = this and the rest, ...]` while later calls would ask, and
`[Y/n ...]` at the last or only one, so Enter approves the call and, where
offered, the rest of the round. Asking to run again a call that may already
have run is the exception, `[y/N ...]`: there Enter denies. Input typed
before a question appears is discarded, so it cannot answer the question,
and input that ends without an answer denies. In the REPL
editor, Enter submits the answer; Ctrl-D on an empty answer denies. A question
also takes `/show output [N]`, `/help`, which explains the answers, and `/ed`,
and asks again afterwards; any other line that starts with `/` gets a note
and the question again, and the call stays undecided. A reason that starts
with `/` begins with `//`. Any other
reply denies the call and supplies a reason, for
example `This is a read-only review; do not change files.` The model receives
an explicit user denial with the text labeled as a reason provided by the
user, and the same result is saved in the session; the transcript shows it as
`Denied by you: ...`. This works for command, image, MCP, and uncertain-rerun
approvals.

In a round that asks anything, each call that policy will not run says so
under its review, `Will not run: REASON`, and a command whose program is not
on PATH, or whose working directory does not exist, says
`Cannot start now: REASON`. That command is still asked about and run, since
an earlier call of the round may create what it needs; if it still cannot
start, its result says it was not run. A bare program name that
resolves outside the system directories (`/bin`, `/usr/bin`, `/usr/local/bin`,
and their `sbin`) is named under the command, as `Program: PATH`. An argument
that spans lines, such as a script passed to an interpreter, and a multiline
`stdin` are shown line by line under the command, each line starting with
`| `; the vector names the argument `<arg N>`. Control characters in them,
carriage returns and tabs included, are shown as `\uXXXX`, so a script
indented with tabs never looks like one indented with spaces, and the label
says when the value ends with a newline, as in
`Stdin (2 lines, ending with a newline):`.

Approval input uses the normal 10 MiB turn limit and continuation rules: end
a line with `\` to continue, or `\\` for a literal trailing backslash. Use
`/ed` to edit the current answer with `$VISUAL`, `$EDITOR`, or `vi`. In a
REPL, `/ed N` and `/ed REGEX` can also select retained prompt history. After
the editor exits, review the draft and press Enter to submit, type to append,
or use `/ed` again. Editor exit and queued input do not submit the answer.
The final text is interpreted the same way whether typed or edited:
`y`/`yes` approves, `a`/`all` approves this call and the rest of the round,
an empty answer is what Enter gives, `n`/`no` denies, and other text becomes
the user-provided denial reason.
Use `//ed` when the literal text `/ed` should be part of the reason.
An oversized approval answer is discarded through its last continuation line
and denies the call without a reason.

`-tool` also enables `view_image`, which lets the model look at an image file:
a plot a command just wrote, or a screenshot named in the prompt. The model
calls it with a `path` and an optional `detail` hint. `lmc` reads the file,
checks that it is PNG, JPEG, GIF, or WebP, and returns it inside the tool
result, so the model sees the picture on its next turn. On Anthropic, OpenAI
Responses, and Gemini 3 the image travels in the result itself; on OpenAI Chat
Completions, whose tool messages carry text only, it follows in a user message
right after the round's tool messages. A symlink is refused, and so is a file
over 5 MB on the Anthropic wire or 20 MiB elsewhere; the refusal reaches the
model as an error result, which it can answer by resizing the file with a
command. `view_image` is not offered with `-argo-legacy`.

Sending a file to the provider is disclosure, so a `view_image` call is
approved the way a command is. A whitelist rule naming the tool and the file
or its directory admits it without a prompt, a blacklist rule naming them
refuses it, and otherwise interactive `lmc` shows the path and asks `Send
shot.png (image/png, 48213 bytes) to the model? [Y/n or reason; /ed to edit]`, `-tool-auto-approve`
approves it, and when no prompt can be answered it is denied; a denial prints
the rule that would have allowed it. The image is saved once, inside the tool
result in the session, and `-show` lists it under the result without printing
the bytes.

Commands can take standard input from a literal `stdin` string or a streamed
`stdin_file`, and can redirect output to files with `stdout_file` and
`stderr_file` instead of returning those streams in the tool result. Relative
paths resolve against the call's `workdir`. Every redirection must name a
regular file directly: symlinks and FIFOs are rejected, so the path shown at
the approval prompt is the file that is written. Streams without a file are
captured into the tool result, bounded per command by `-tool-max-output-bytes`
(1 MiB by default); output past the bound is cut off and the result is marked
truncated.

Existing output files are emptied only after every redirection opens. If setup
creates an output but no process starts, `lmc` removes it and reports any
cleanup failure; pre-existing files are never removed.

A closed set of pseudo-devices is accepted alongside regular files:
`/dev/null`, `/dev/zero`, `/dev/full`, `/dev/random`, and `/dev/urandom`.
Writing to `/dev/null` is the way to throw a stream away rather than pay for it
in the tool result. Any other device is refused, including `/dev/tty`, and the
file found at one of those names must really be a character device. They are
not truncated, they are not an approval shortcut, and unlike a regular file
they may be shared: several commands in one round may write to `/dev/null`, and
one command may read and write it at once.

File redirection is a separate approval boundary. A legacy array whitelist rule
such as `["sort"]` approves `sort` only when `stdin_file`, `stdout_file`, and
`stderr_file` are all absent. To approve a redirected call, use an object rule
that names its exact file settings:

```json
{"command":["sort"],"stdin_file":"unsorted.txt","stdout_file":"sorted.txt"}
```

Literal `stdin` is authority too, so an array rule such as `["python3"]` also
requires `stdin` to be absent. Feeding a program to an interpreter through
`stdin` is arbitrary code execution, and a grant written before that channel
existed cannot be read as consent to it. An object rule opts in with
`"stdin": true`, which permits any literal stdin for that prefix:

```json
{"command":["python3"],"stdin":true}
```

`environ` is authority as well, and it reaches further than the others: it
chooses the dynamic linker for every program the command starts (`LD_PRELOAD`,
`DYLD_INSERT_LIBRARIES`), the module path an interpreter imports from, and the
proxy a network tool trusts. An array rule therefore requires `environ` to be
absent or empty, and an object rule grants one exact environment:

```json
{"command":["make"],"environ":{"CC":"clang"}}
```

An object rule accepts exactly these fields:

- `command`: required, an array of strings, matched as an argv prefix.
- `environ`: optional, an object of string values, naming the environment
  variables the call may add.
- `workdir`: optional, a string.
- `stdin`: optional, a boolean naming the literal-stdin channel rather than the
  bytes sent through it.
- `stdin_file`, `stdout_file`, `stderr_file`: optional, each a string.

Every object rule needs `command` plus at least one of `environ`, `stdin`,
`stdin_file`, `stdout_file`, or `stderr_file`. A rule naming only `command` and
`workdir` is rejected when the file loads, so `workdir` can only be pinned
alongside one of those five. Empty file paths and unrecognized field names are
rejected at load time as well, which is why a call's `timeout` cannot be written
into a rule; it does not take part in matching, because it bounds how long an
already-granted command runs rather than what that command can reach.

A rule may name the `view_image` tool instead of a command:

```json
{"tool":"view_image"}
{"tool":"view_image","path":"plots"}
```

The first grants every image the model asks to see; the second grants the
file `plots` names or any file inside that directory. Both paths are resolved
against the working directory and cleaned before they are compared, so
`plots/../secret.png` is judged as `secret.png` and does not match. A `tool`
rule accepts `path` and nothing else, and it has no array form, since an array
is an argv prefix: `["view_image"]` grants a command called `view_image`. The
call's `detail` takes no part in matching. In a blacklist the same forms deny:
`{"tool":"view_image"}` refuses every image, and a `path` rule refuses the
file or directory it names. A denied image prints the rule naming the file
itself; widen it to the directory by hand if that is what you want.

A rule may name an MCP server instead:

```json
{"mcp":"github"}
{"mcp":"github","tool":"list_issues"}
```

The first grants every tool of the server, the second one tool; `tool` is
the server's own tool name, without the `mcp__github__` prefix the model
sees. An `mcp` rule accepts `tool` and nothing else, and it has no array
form. The call's arguments take no part in matching. In a blacklist the same
forms deny. A denied call prints the rule naming the one tool.

In a whitelist, `workdir` and the three file fields must equal what the call
supplied, and an omitted field matches only an absent field: a rule naming
`stdout_file` does not grant a call that also sets `stderr_file`, and a rule
without `"stdin": true` does not grant a call carrying literal stdin. `environ`
must match as a whole map, same variables and same values, so a rule granting
`{"CC":"clang"}` does not grant a call that also sets `LD_PRELOAD`, and a rule
naming no `environ` grants only a call that adds no variables. Paths are
compared as written, without normalization, so a relative redirection needs the
same `workdir` in the rule and in the tool call.

Denials are broader than grants, in both forms. An array blacklist rule blocks
its argv prefix in every shape: any `workdir`, any environment, any literal
stdin, any redirection. An object blacklist rule blocks every call carrying the
fields it names and ignores the fields it does not, so
`{"command":["tee"],"stdout_file":"/etc/hosts"}` still denies that call when it
also sets `stderr_file`. `environ` follows the denial direction rather than the
whitelist's: every variable a denial names must be present in the call, and the
call may carry others, so
`{"command":["python3"],"environ":{"LD_PRELOAD":"/tmp/evil.so"}}` cannot be
evaded by setting an unrelated variable beside it.

A denied command prints the exact rule that would have allowed it, in whichever
form applies, ready to paste into a whitelist file.

Adding `environ` to rule matching narrowed the array form: a line such as
`["go","test"]` no longer covers a call that sets an environment variable. Those
calls now ask for approval, or are denied when no prompt can be answered, and
the denial prints the object rule that admits them.

Approval policy is evaluated in this order:

1. A blacklist match is denied.
2. A whitelist match runs without prompting.
3. If a whitelist is loaded and no prompt can be answered, a non-match is denied.
4. `-tool-auto-approve` approves any command still undecided.
5. A still-undecided command is denied when no prompt can be answered.
6. Otherwise, `lmc` asks for approval.

This ordering means a blacklist always wins. Steps 3 and 5 turn on whether a
prompt can actually be answered, which is `-tool-non-interactive` **or** a stdin
that is not a terminal — piping the prompt into `lmc` is the usual case. So
combining a whitelist with `-tool-auto-approve` in a script keeps the whitelist
restrictive: commands not in it are denied, because there is no one to ask.

Step 3 turns on the flag, not on how many rules the file produced. A whitelist
that is empty, entirely comments, or truncated to nothing is still a whitelist:
passing `-tool-whitelist` is the statement that unlisted commands need review,
so with no way to ask, every command is denied rather than falling through to
`-tool-auto-approve`. Omit `-tool-whitelist` if you want auto-approve to run
whatever the blacklist does not stop.

Approval needs a terminal on stdin to read the answer, and there is no
substitute for it. The command review and the question go to stderr, or
together to `/dev/tty` when stderr is redirected, so `lmc -tool 2>lmc.log`
still shows what it asks you to approve. Before each question `lmc` discards
what the terminal holds, so a line typed while the model was generating, or
keys typed without Enter, cannot answer it; when a complete line was
discarded, a note above the question says so. A tool approval answer takes
the 10 MiB turn limit; the tool-call round limit question takes at most 64
bytes, and a longer answer to it keeps the limit.

Whitelist and blacklist files contain one JSON command rule per nonempty,
non-comment line. Matching is exact per argument and treats `command` as a
prefix. For example, `["git","status"]` matches
`["git","status","--short"]`, while `["git"]` matches every command whose
executable argument is `git`. Longer entries grant narrower permissions and are
usually safer.

Assistant text stays on stdout. Tool review, approval prompts, execution
results, warnings, and errors go to stderr, so redirecting stdout captures only
the assistant response. When stdout and stderr go to different places, an
empty line on stdout separates the text of one response from the next, which
the tool transcript does on a shared terminal. Tool commands are displayed as
their exact JSON argv arrays, and each result quotes its output a line at a
time. Results appear in request order as calls finish. In the REPL on a
terminal a round looks like this:

```text
Tokens (response 1): input 4472 | output 403 (reasoning 152) | 4.4s

Tools: 2
[1/2] Command: ["git","status","--short"]

[2/2] Command: ["go","test","./internal/core"]

[1/2] Allow git? [y/n/A = this and the rest, or reason; /ed to edit]:
  > y

[2/2] Allow go? [Y/n or reason; /ed to edit]:
  > y

Running 2 tools in parallel...

Tool results:
[1/2] git: Completed in 24ms
  Output (stdout+stderr, 1 line):
  | ## main

[2/2] go: Completed in 814ms (no output)
```

Everything under a call or a result sits two columns in, the answer to a
question included. On a terminal the transcript wraps a line longer than the
terminal itself, and each row that continues it starts with `  : `, so the
row stays in its block and cannot be mistaken for a line of output. Tabs in
output reach every eighth column counted from the start of the output, which
keeps tables aligned under `| `.

`stdout+stderr` says the two streams were captured together; a call that
redirected one of them shows `stdout` or `stderr` alone. A command that exited
with an error status shows `Output`, and one stopped by its timeout or by
Ctrl-C shows `Partial output`.

```bash
# Run whitelisted commands without prompting.
printf '["ls"]\n["pwd"]\n' > whitelist.txt
echo "Show the working directory and files" | lmc \
  -argo-user "$USER" \
  -tool \
  -tool-whitelist whitelist.txt

# Grant one exact redirected shape. The plain ["sort"] rule would not grant it.
printf '%s\n' '{"command":["sort"],"stdin_file":"in.txt","stdout_file":"out.txt"}' \
  > redirected-whitelist.txt
echo "Sort in.txt into out.txt" | lmc \
  -argo-user "$USER" \
  -tool \
  -tool-whitelist redirected-whitelist.txt

# For scripts, run only whitelisted commands and deny every non-match.
echo "Run the allowed checks" | lmc \
  -argo-user "$USER" \
  -tool \
  -tool-non-interactive \
  -tool-whitelist whitelist.txt

# Run every command not blocked by the blacklist, without prompting.
printf '["rm"]\n["dd"]\n' > blacklist.txt
echo "Inspect this repository" | lmc \
  -argo-user "$USER" \
  -tool \
  -tool-auto-approve \
  -tool-blacklist blacklist.txt

# Give the model the tools of the MCP servers a file names, approving each
# call at the prompt.
echo "List the files in my home directory" | lmc \
  -argo-user "$USER" \
  -mcp-config examples/lmc/mcp-config.json
```

#### MCP servers

`-mcp-config file` connects the servers a Model Context Protocol (MCP)
configuration names and advertises their tools beside the built-in ones; it
implies `-tool`. The file is the `mcpServers` JSON that Claude Code and
Gemini CLI write, so a file made for them loads unchanged:

```json
{
  "mcpServers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": {"GITHUB_TOKEN": "${GITHUB_TOKEN}"}
    },
    "docs": {
      "url": "https://mcp.example.com/mcp",
      "headers": {"Authorization": "Bearer ${DOCS_TOKEN}"},
      "timeout": 120000
    }
  }
}
```

A server with `command` is started as a subprocess without a shell and spoken
to over stdio; a server with `url` is a Streamable HTTP endpoint. The other
fields are `type` (`stdio` or `http`, inferred when absent), `args`, `env`,
`cwd`, `headers`, `timeout` (per call, in milliseconds; the default is
`-tool-timeout`), `startupTimeout` (milliseconds, 30 seconds by default),
`includeTools` and `excludeTools` (the server's own tool names), and
`optional`. `${VAR}` and `${VAR:-default}` expand from the environment in
`command`, `args`, `env`, `cwd`, `url`, and `headers`; an unset variable
without a default is an error. Unknown fields are ignored with a warning,
`type: "sse"` is refused because the HTTP+SSE transport is deprecated, and
the flag may be repeated to merge files as long as no server is named twice.
Servers are connected when `lmc` starts and shut down when it exits. A server
that fails to start fails the run, unless it is `optional`, which skips it
with a warning.

Each stdio server runs in a process group of its own. Ctrl-C reaches `lmc`
and the commands it runs and leaves the servers running, and Ctrl-Z stops
`lmc` and its commands while the servers wait idle until `lmc` resumes.
Shutdown closes a server's stdin, where the protocol's shutdown begins, then
sends `SIGTERM` to its whole process group and `SIGKILL` two seconds later,
which also ends what a launcher such as `npx` or `uvx` left running, even a
process that ignores `SIGTERM`. A server that has exited stays a zombie
until the `SIGKILL`, so the ID of its group cannot pass to another group
that a signal would then reach. Since the group cannot be checked while the
zombie holds it, those two seconds pass even when nothing is left, so `lmc`
takes about two seconds longer to exit when it runs stdio servers; the
servers shut down together. Shutdown gives up after about six seconds and
reports anything it could not confirm. A process that moved to another
group with `setsid` is out of its reach, and if `lmc` is killed before it
can shut down, a server learns of it from the end of its stdin, so one that
ignores that is left running. A server that exits on its own is reported
once and stays a zombie until shutdown; its tools stay listed, and a call to
one returns an error to the model.

`lmc` speaks the `2026-07-28` revision of the protocol and the earlier
revisions with the `initialize` handshake, and detects which one a server
uses. Each tool is advertised as `mcp__<server>__<tool>`, with characters
outside letters, digits, `_`, and `-` replaced and a long name shortened to
64 characters with a stable suffix. A server's `instructions` are appended to
the tool system prompt under a heading naming the server. Authorization to an
HTTP server is whatever `headers` carries; `lmc` does not run an OAuth flow.

An MCP call is approved the way a command is. A whitelist rule naming the
server, or the server and tool, admits it without a prompt, a blacklist rule
refuses it, and otherwise interactive `lmc` shows the server, tool, and
arguments and asks `Call github/list_issues? [Y/n or reason; /ed to edit]`; `-tool-auto-approve`
approves it, and when no prompt can be answered it is denied with the rule
that would have allowed it. The result's text reaches the model as the tool
output. Images in the result are attached the way `view_image` attaches a
file, under the same size cap, and audio, resource links, and other content
the wire cannot carry become one line notes. A server that reports an error,
asks the user a question (which `lmc` does not answer), or fails to reply in
time produces an error result the model can read. Calls are recorded in the
session with the server and tool name, and resuming a session that has one
pending needs the same `-mcp-config`.

#### Choosing tools for a run

`-tool-include` and `-tool-exclude` narrow the tools a run advertises, out of
those `-tool` and `-mcp-config` turn on. A selector is `universal_command`,
`view_image`, an MCP server name for every tool of that server, or
`server/tool` for one of them; selectors are comma separated and the flags
may be repeated. Include narrows the run to what it names, exclude removes
from what remains, and a name in both is excluded. A server that include
leaves out or exclude names is never started. A tool named as `server/tool`
is settled when the server lists its tools, after the file's `includeTools`
and `excludeTools`, and a name the server does not offer is reported the
same way. A withheld tool is not advertised, and a call to it, such as one
pending in a resumed session, is an error result. The rule files stay the
permission layer: a withheld tool needs no rule.

A run that withholds `universal_command` gets a shorter tool system prompt
without the command rules, and a resumed session on the other prompt forks,
as it does when MCP servers change. A selector naming nothing the run has, a
selection that leaves no tool at all, and an MCP server named
`universal_command` or `view_image` are errors at startup.

```bash
# The filesystem server alone: no local commands, no image tool.
echo "List the files in my home directory" | lmc \
  -argo-user "$USER" \
  -mcp-config examples/lmc/mcp-config.json \
  -tool-include filesystem

# Local commands and the server, less one of its tools and the image tool.
echo "Inspect this repository" | lmc \
  -argo-user "$USER" \
  -mcp-config examples/lmc/mcp-config.json \
  -tool-exclude filesystem/directory_tree,view_image
```

### lmc Flags

Provider and credentials:

- `-provider string`: `argo` (default), `openai`, `google`, or `anthropic`.
- `-argo-user string`: Argo user or API key.
- `-api-key-file string`: API key file for OpenAI, Google, Anthropic, or Argo.
- `-provider-url string`: Custom provider URL.
- `-anthropic-bearer`: With `-provider anthropic`, also send the API key as
  `Authorization: Bearer`, which the ALCF inference gateway requires.
- `-models-url string`: URL of the provider's model list, in place of the one
  `-provider-url` implies. See [ALCF Inference Endpoints](#alcf-inference-endpoints).
- `-argo-dev`, `-argo-test`: Use the Argo dev or test host.
- `-argo-legacy`: Use the Argo legacy chat endpoints.

Mode and model:

- `-model string`: Model name. The default depends on the provider.
- `-e`: Generate embeddings instead of chat. Embed mode turns off sessions,
  does not stream or use the Responses API, and rejects `-tool`, `-resume`,
  `-branch`, and the output controls.
- `-list-models`: List the models the provider reports. When the provider
  describes its models, as Anthropic, Google, and the ALCF inference gateway
  do, a line under each gives what it reports of the context and output
  limits, inputs, thinking, effort levels, tools, structured output, and APIs.
- `-no-stream`: Wait for the whole answer instead of streaming it. A chat
  streams by default; `-show` and embed mode do not stream.
- `-repl`: Read one turn per line from stdin until it ends, continuing one
  session. See [Conversation Loop](#conversation-loop).
- `-chat-completions`: Send the chat through the provider's chat completions
  route instead of its Responses API. `lmc` uses the Responses API by default
  for `-provider openai`, and for Argo `gpt*` models without `-argo-legacy`,
  which are the models Argo serves at `/v1/responses`; other Argo models keep
  their usual routing. The other providers, `-argo-legacy`, and embed mode
  keep their own API, and the flag changes nothing there.
- `-print-curl`: Print the equivalent `curl` command and exit without sending
  the request. With `-resume` or `-branch`, `lmc` reads session history and
  writes nothing back. With `-resume -tool`, it substitutes placeholder results
  for pending tool calls.

Output controls:

- `-s string`: System prompt.
- `-developer string`: Add a developer instruction immediately before the
  invocation's first new user turn. Supports `-provider openai`, and Argo
  `gpt*` models on the Responses API, which `-chat-completions` turns off. The
  instruction is saved and replayed with
  its developer role. On resume, supplying the flag adds another instruction;
  omitting it keeps the saved instructions. In a REPL it is consumed once the
  first user turn is committed. It requires prompt text or an image and cannot
  accompany regeneration of an assistant answer. Other provider wires reject
  developer instructions, including ones in resumed history; use `-s` to set
  a system prompt on those providers.
- `-effort string`: Reasoning effort hint: `none`, `minimal`, `low`, `medium`,
  `high`, `xhigh`, or `max`. On Anthropic-wire requests, any supported
  non-`none` effort also enables `thinking.type="adaptive"` with
  `thinking.display="summarized"` so the returned thinking blocks contain the
  visible summary rather than only an encrypted signature. On Responses
  requests, any effort other than `none` also sends `reasoning.summary="auto"`
  so the reasoning items carry a readable summary beside the encrypted
  reasoning. A model that reasons only briefly may return no summary.
- `-hide-thinking`: Hide the provider-returned visible thinking summaries,
  which `lmc` prints to stderr by default. The display is presentation-only:
  hiding it does not turn reasoning off or alter the request. The assistant
  answer remains unchanged on stdout in both streaming and non-streaming
  modes. Signatures, encrypted content, and opaque redacted
  payloads are never printed; a returned Anthropic thinking block, or a run of
  Responses reasoning items, without visible text is shown as
  `[omitted by provider]` where the thinking happened, before the answer or
  tool call that follows it; a summary that arrives only with the finished
  response shows after the answer. Each block is delimited by `--- thinking ---` and
  `--- end thinking ---`, and consecutive Responses reasoning items share one
  block with each summary on its own line; an unterminated stream uses
  `--- end thinking (incomplete) ---`. When stderr is a terminal each line
  of thinking starts with `| ` and wraps at the terminal width, and control
  characters are shown as `\uXXXX`; redirected stderr keeps the text as
  returned. If the provider counts reasoning tokens and returns no visible
  thinking, lmc says so, once a run. Anthropic's `summarized` display is its readable
  thinking mode; adaptive thinking may skip thinking for a simple request,
  which needs no note.
  Compatible Chat Completions servers' `reasoning_content` or `reasoning`
  field (newer vLLM sends `reasoning`) and Responses reasoning text/summary
  events are supported and retained in session history.
  A server may return thinking by default without an `-effort` flag. OpenAI's
  Chat Completions API returns no reasoning text, and Argo's chat completions
  route adds none for `gpt*` models. `lmc` warns when the thinking display
  meets that route, which `-chat-completions` selects, and a REPL warns
  at `/thinking on`, or at `/model` while the display is on. The Responses
  API with an `-effort` returns their reasoning summaries.
- `-reasoning-mode string`: Responses API reasoning mode: `standard` or `pro`
  (`pro` applies to GPT-5.6 only). `lmc` emits it only on the Responses API
  (OpenAI, or Argo `gpt*`, without `-chat-completions`) and otherwise warns
  and ignores it.
- `-reasoning-context string`: Responses API reasoning context: `auto`,
  `current_turn`, or `all_turns`. Same Responses API restriction as
  `-reasoning-mode`.
- `-max-tokens int`: Maximum output tokens. `0` (the default) uses the provider
  default. Anthropic-wire requests — the `anthropic` provider and Argo `claude*`
  models — always send a limit: `128000` for Opus models, `64000` for other
  Claude models. OpenAI Chat Completions uses `max_completion_tokens`;
  Responses uses `max_output_tokens`. Reported incomplete output returns an
  error with a nonzero exit status; partial text and thinking are saved.
  Incomplete tool calls are discarded and cannot execute on resume.
- `-json`: Request a JSON object and validate the returned answer.
- `-json-schema path`: Request JSON output matching the supplied schema and
  check the returned JSON syntax. Does not combine with `-json`. The schema file
  must contain a JSON object; the provider validates and enforces the schema.
  Invalid returned JSON causes a nonzero exit status. Streamed answer bytes may
  already have reached stdout when the error is reported; check the exit status.
  In `-repl`, errors are reported per turn and the conversation loop continues;
  with piped input, any failed turn makes the final exit status nonzero.

Chat Completions places the schema under `response_format.json_schema`;
Responses places it under `text.format`.

Thinking chunks stay separate from the answer and are retained in session
history, and shown unless `-hide-thinking` is given. A malformed Chat Completions
stream stops the turn with an error and retains already received text and
thinking. Pending tool calls from that response cannot run.

Token usage is shown on stderr when reported, including Chat Completions
streaming usage. Separate reasoning-token counts are shown only when supplied
by the provider.

Images:

- `-image path`: Attach an image file to the prompt. Repeat the flag to attach
  several; they are sent in the order given, ahead of the prompt text. The
  type is sniffed from the content and must be PNG, JPEG, GIF, or WebP; each
  file may be at most 20 MiB, and the provider's own limits still apply.
  Truncated or malformed image containers are rejected locally; decoding
  compressed pixel data remains the provider's responsibility.
  The image is read when the flags are parsed, base64-encoded into the
  request, and saved inline in the session. Empty stdin is accepted when an
  image is attached. Not available in embed mode, with `-argo-legacy`, or
  with `-branch` from an assistant message, which regenerates that answer
  without a new user turn.
- `-image-detail string`: OpenAI detail hint for every attached image: `auto`,
  `low`, or `high`. Requires `-image` or `-repl`; in a REPL it also applies to
  later `/image` commands. Other providers ignore it.

To let the model open an image itself, see `view_image` under Tool Use.

Tools:

- `-tool`: Enable the built-in `universal_command` and `view_image` tools.
  Commands run directly with `execvpe`-style semantics, without a shell;
  `view_image` returns an image file to the model inside the tool result.
- `-mcp-config path`: Connect the MCP servers an `mcpServers` JSON file names
  and advertise their tools beside the built-in ones. Implies `-tool`;
  repeatable. See MCP servers under Tool Use.
- `-tool-include selectors`: Advertise only the tools named:
  `universal_command`, `view_image`, an MCP server, or `server/tool`. Comma
  separated; repeatable. A server left out is never started. Needs `-tool`
  or `-mcp-config`.
- `-tool-exclude selectors`: Withhold the tools named, in the same form.
  Applied after `-tool-include`. See Choosing tools for a run under Tool Use.
- `-tool-timeout duration`: Per-command timeout; default `1m`. A tool call may
  set its own `timeout` in seconds instead; that value is clamped to 24 hours.
  An MCP call uses its server's `timeout` when the configuration sets one.
- `-tool-whitelist path`: JSON rules that run without prompting. File
  redirection, literal `stdin`, and `environ` each require an object rule that
  names them, `{"tool":"view_image","path":"plots"}` admits the images under a
  directory, and `{"mcp":"github","tool":"list_issues"}` admits an MCP tool.
  When no prompt can be answered, non-matching calls are denied — including
  when the file produced no rules at all.
- `-tool-blacklist path`: JSON rules that are always denied. Array rules cover
  every shape of the prefix; object rules cover every call carrying the fields
  they name, a `tool` rule covers the images it names, and an `mcp` rule
  covers the server or tool it names.
- `-tool-auto-approve`: Run without prompting unless denied by the blacklist, or
  by a whitelist non-match when no prompt can be answered.
- `-tool-non-interactive`: Never prompt. Commands not approved by the policy are
  denied. Requires `-tool-whitelist` or `-tool-auto-approve`.
- `-max-tool-rounds int`: Tool-call rounds allowed per block; default `64`. If
  another tool call is pending at the limit, interactive `lmc` asks whether to
  grant another full block. This confirmation is independent of command
  approval; non-interactive use stops at the limit.
- `-max-tool-parallel int`: Maximum concurrent tool executions; default `8`.
- `-tool-max-output-bytes int`: Maximum captured, non-redirected output per
  command; default `1048576` bytes (1 MiB). Zero uses the default; a negative
  value, or one above the 100 MiB ceiling, is rejected at startup rather than
  quietly replaced. Output past the limit is cut off and the model is told the
  result was truncated.

Sessions:

- `-resume string`, `-branch string`, `-show-sessions`, `-show string`, `-delete string`
- `-no-session`: Send the request without writing session files.
- `-sessions-dir string`: Default `~/.lmc/sessions`.
- `-skip-flock-check`: Skip the session file lock check.

Requests and logging:

- `-timeout duration`
- `-retries int`: How many more times to send a request that failed, waiting
  longer before each attempt, from about a second up to a minute. Default 10,
  so a request that keeps failing is given up after a few minutes; Ctrl-C
  ends the wait. A request is sent again after HTTP
  408, 425, 429, or 5xx, after a connection error before the response, and
  when a stream reports one of those failures before any content, as Argo
  does with `Streaming response failed`, or ends before any content. Tool
  rounds wait as the first request does. A retried 5xx or stream failure is
  written to the log file, and so is the error a request finally fails with.
- `-log-dir string`: Default `~/.lmc/logs`.
- `-log-level string`: `DEBUG`, `INFO`, `WARN`, or `ERROR`.

Build information:

- `-version`: Print the build's version and exit. The line names the module
  version stamped at build time, the Go toolchain, and the commit.

## ALCF Inference Endpoints

The [ALCF Inference Endpoints](https://docs.alcf.anl.gov/services/inference-endpoints/)
serve OpenAI compatible APIs that take a Globus access token as the bearer
key. `alcfauth` logs in through Globus the way `alcf-tokens login inference`
does, without Python, and writes the access token to a key file that `lmc`
reads with `-api-key-file`.

```bash
# Log in: open the printed URL, log in with your institution, and paste the
# code Globus shows. ALCF asks for a new login every 30 days.
alcfauth login

# Chat with a model on Minerva.
echo "Explain PKCE in two sentences" | lmc \
  -provider openai \
  -provider-url https://inference-api.alcf.anl.gov/resource_server/minerva/api/v1 \
  -api-key-file ~/.lmc/alcf/inference.key \
  -model mistral-large-4-nvfp4

# List Minerva's models with their context windows, inputs, effort levels,
# and APIs.
lmc -provider openai -api-key-file ~/.lmc/alcf/inference.key \
  -models-url https://inference-api.alcf.anl.gov/resource_server/minerva/models \
  -list-models
```

`alcfauth login` opens the URL in a browser when it can. Over SSH, or with
`-no-browser`, it only prints the URL; open it in a browser on any machine and
paste the code back.

An access token lasts 48 hours. `alcfauth token` refreshes it when less than
an hour is left (`-min-valid` sets the margin), rewrites the key file, and
prints the token, so `curl -H "Authorization: Bearer $(alcfauth token)"`
works; `-q` skips the printing. `alcfauth token -watch` keeps running and
refreshes the key file whenever less than `-min-valid` is left, until it is
stopped. `lmc` reads the key file for every request, and `apiproxy` reads it
again whenever it changes, so with a watch running a long `lmc -repl` or
`apiproxy` never meets an expired token. Run the watch in its own terminal or
tmux window:

```bash
alcfauth token -watch
```

or as a systemd user service on Linux, in
`~/.config/systemd/user/alcfauth.service` with the path to your `alcfauth`,
started with `systemctl --user enable --now alcfauth`:

```ini
[Unit]
Description=Keep the ALCF inference key file valid

[Service]
ExecStart=%h/bin/alcfauth token -watch

[Install]
WantedBy=default.target
```

or as a launchd agent on macOS, in
`~/Library/LaunchAgents/gov.anl.alcf.alcfauth.plist` with your own paths,
started with `launchctl load ~/Library/LaunchAgents/gov.anl.alcf.alcfauth.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>gov.anl.alcf.alcfauth</string>
  <key>ProgramArguments</key>
  <array><string>/Users/you/bin/alcfauth</string><string>token</string><string>-watch</string></array>
  <key>RunAtLoad</key><true/>
  <key>StandardErrorPath</key><string>/Users/you/.lmc/alcf/watch.log</string>
</dict>
</plist>
```

The watch prints a line to stderr for each refresh. It exits with status 1
when Globus wants a new login, which ALCF requires every 30 days; run
`alcfauth login` and start the watch again.

Other clusters differ in the URL: replace `minerva/api/v1` with
`metis/api/v1`, `tara/api/v1`, or `sophia/vllm/v1`, and pick a model the
cluster serves. With `-provider openai`, `lmc` uses the Responses API;
`-chat-completions` switches to Chat Completions for a model whose
capabilities lack `responses`. `lmc` shows Minerva's reasoning on both APIs.
When a model's server refuses a streamed request, the gateway sends the
refusal as an answer that starts with `[ERROR]`; `lmc` and `apiproxy` report
it as an error.

The gateway also serves Anthropic Messages, which returns thinking blocks with
their signatures. It reads only `Authorization: Bearer`, so the anthropic
provider needs `-anthropic-bearer`:

```bash
echo "Explain PKCE in two sentences" | lmc \
  -provider anthropic -anthropic-bearer \
  -provider-url https://inference-api.alcf.anl.gov/resource_server/minerva/api/v1 \
  -api-key-file ~/.lmc/alcf/inference.key \
  -model inkling-bf16
```

The gateway has no `/models` under a cluster's API. `-models-url
https://inference-api.alcf.anl.gov/resource_server/minerva/models` points
`lmc -list-models` and apiproxy's `/v1/models` at the cluster's list, from
which `lmc` shows each model's context window, inputs, effort levels, and APIs.

What Minerva's models refused or ignored on 2026-10-30:

| Model | Limits |
| --- | --- |
| `beam-1.0-bf16` | Text only; refuses images. |
| `mistral-large-4-nvfp4` | Refuses `-developer`. |
| `muse-spark-bf16` | Does not enforce `-json-schema`: the answer can be prose or fenced JSON, which `lmc` reports as an error. On Messages, refuses a thinking `budget_tokens`. |

With `apiproxy` in front of a cluster:

- Anthropic clients such as Claude Code: `apiproxy -provider anthropic
  -anthropic-bearer -provider-url
  https://inference-api.alcf.anl.gov/resource_server/minerva/api/v1
  -api-key-file ~/.lmc/alcf/inference.key`. The gateway's Messages answers,
  thinking included, pass through; `/v1/messages/count_tokens` is estimated
  locally, since the gateway has no such route.
- Chat Completions and Responses clients: the same with `-provider openai` and
  without `-anthropic-bearer`. Chat requests pass through with their
  reasoning. `/v1/responses` is forwarded, and Minerva keeps no responses, so
  `previous_response_id` and `GET /v1/responses/{id}` fail. With
  `-chat-completions`, apiproxy converts `/v1/responses` to chat completions
  and keeps the responses itself, so both work, as does
  `/v1/responses/input_tokens`. The converted answers carry Minerva's
  reasoning as reasoning items, and `/v1/messages` on this setup gives it as
  thinking blocks.
- Add `-models-url` for `/v1/models`.

`-log-level DEBUG` logs every request in full, its `Authorization` header
included, so a DEBUG log of `lmc` or `apiproxy` holds a live access token.

| Command | Action |
| --- | --- |
| `alcfauth login` | Log in and write the token cache and the key file. `-no-browser` only prints the URL; `-client-id ID` logs in as another Globus native app. |
| `alcfauth token` | Refresh the access token when less than `-min-valid` (default `1h`) is left, rewrite the key file, and print the token unless `-q` is given. `-watch` keeps running and refreshes the key file before it runs out. |
| `alcfauth status` | Show how long the access token has left and when the login ran. `-check` asks the gateway whether it accepts the token, refreshing an expired one first. |
| `alcfauth logout` | Revoke the tokens at Globus and remove them from the cache and the key file. |

Every command takes `-cache file` (default `~/.lmc/alcf/tokens.json`) and
`-key-file file` (default `~/.lmc/alcf/inference.key`). Both files hold
credentials and are written with mode 0600. The cache uses the layout of the
`alcf-tokens.sh` script that ships with `alcf-tokens`, so `-cache` can point at
that script's `tokens-bash.json`. By default `alcfauth` logs in as the public
Globus app `alcf-tokens` uses, but it keeps its own cache: a login with
`alcf-tokens` or `inference_auth_token.py` does not log `alcfauth` in.

## apiproxy

`apiproxy` binds `127.0.0.1:8082` and translates request and response formats at
the API boundary. Pass `-host 0.0.0.0` only when you intend to reach it from
beyond localhost.

### Start The Proxy

```bash
# OpenAI backend.
echo "sk-..." > ~/.openai-key
chmod 600 ~/.openai-key
apiproxy -provider openai -api-key-file "$HOME/.openai-key"

# Anthropic backend.
echo "sk-ant-..." > ~/.anthropic-key
chmod 600 ~/.anthropic-key
apiproxy -provider anthropic -api-key-file "$HOME/.anthropic-key"

# Google backend.
echo "AIza..." > ~/.google-key
chmod 600 ~/.google-key
apiproxy -provider google -api-key-file "$HOME/.google-key"

# Argo backend.
apiproxy -provider argo -argo-user "$USER"

# Listen on another host and port.
apiproxy -host 0.0.0.0 -port 8080 \
  -provider openai \
  -api-key-file "$HOME/.openai-key"
```

### apiproxy Flags

- `-provider string`: `argo` (default), `anthropic`, `openai`, or `google`.
- `-api-key-file string`: API key file for the selected provider.
- `-argo-user string`: Argo user or API key when using Argo.
- `-provider-url string`: Custom provider URL.
- `-anthropic-bearer`: With `-provider anthropic`, also send the API key as
  `Authorization: Bearer`, which the ALCF inference gateway requires.
- `-models-url string`: URL of the provider's model list for `/v1/models`, in
  place of the one `-provider-url` implies.
- `-model-map REGEX=MODEL_NAME`: Map matching request models to a backend model.
  Repeat it to add rules; the first match wins.
- `-argo-dev`, `-argo-test`, `-argo-legacy`
- `-chat-completions`: Convert `/v1/responses` requests to the provider's
  chat completions instead of forwarding them to its own Responses API. By
  default the proxy forwards them with `-provider openai`, and with
  `-provider argo` for `gpt*` backend models unless `-argo-legacy` is given.
  The other providers convert them either way, and the flag changes nothing
  there.
- `-host string`: Bind host. Default `127.0.0.1`.
- `-port int`: Bind port. Default `8082`.
- `-sessions-dir string`: Local Responses API state directory. Default `~/.apiproxy/sessions`.
- `-max-request-body-size int`: Request body limit in MB. Default `512`, matching
  OpenAI's documented payload limit so the proxy never rejects a request the
  provider would have accepted. Anthropic caps Messages requests at 32MB and
  Argo enforces its own limit; those rejections pass through untouched. Requests
  over the proxy's own limit get `413 request_too_large`; when `Content-Length`
  is known, the message includes the observed size and exact whole-MB setting.
- `-strip-encrypted-reasoning`: Recovery mode for direct Responses backends.
  Before forwarding a request, drop every reasoning and compaction input item
  that carries `encrypted_content`. The item goes whole: what would be left of a
  reasoning item is an `rs_...` id, and that id is a handle on the same state
  the backend could not resolve. This discards opaque model context, so use it
  only when a backend can no longer decrypt a resumed session. It is off by
  default and logs each request it changes.
- `-log-level string`: `DEBUG`, `INFO`, `WARN`, or `ERROR`.
- `-log-format string`: `text` or `json`.
- `-version`: Print the build's version and exit.

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
- `GET /api/hello` returns `{"message":"hello"}`, mirroring the Anthropic API's
  liveness endpoint so clients that probe `<base-url>/api/hello` for reachability
  get the response they expect. Neither endpoint requires credentials.

Streaming `/v1/messages` responses emit Anthropic-format SSE. Once the upstream
streaming request returns HTTP 200, `apiproxy` sends an `event: ping` keepalive
whenever it has written no downstream SSE event for 15 seconds.

From a Chat Completions backend, a `/v1/messages` answer carries the backend's
reasoning (`reasoning_content` or `reasoning`) as a thinking block ahead of the
text. Its signature starts with `lmtools-chat-reasoning:`: sent back in a later
request, the block returns to the backend in the field it came from, while an
Anthropic backend would refuse that signature. On a stream, reasoning that
arrives after the text or a tool call began is dropped.

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
apiproxy -provider argo -argo-user "$USER" \
  -model-map '^gpt-5\.5$=gpt55'
```

Then run Codex with the profile:

```bash
codex --profile apiproxy
codex exec --profile apiproxy "summarize this repository"
```

The proxy now sends `gpt-5.5` requests to Argo as `gpt55`. Codex speaks the
Responses API (`wire_api` defaults to `responses` and takes no other value), so
its traffic arrives at `POST /v1/responses`, and the proxy forwards it to
Argo's Responses API for a `gpt*` backend model such as `gpt55` (see
[Direct Argo Backend](#direct-argo-backend)). Other models, and every model
under `-chat-completions`, take the converted path, with the
[limitations](#converted-responses-limitations) that implies.

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
apiproxy -provider argo -argo-user "$USER" \
  -model-map '^gpt-4o-mini$=gpt5mini' \
  -model-map '^gpt-4o$=gpt5' \
  -model-map '^claude-3-haiku.*=claude-3-haiku-20240307' \
  -model-map '^claude-.*=claude-opus-4-1-20250805'
```

Both binaries route native Argo requests by the prefix of the model that reaches
Argo, which for `apiproxy` is the model after `-model-map`:

- Models starting with `claude` use Argo's Anthropic-compatible `/v1/messages` wire format.
- Models starting with `gpt` use Argo's Responses API at `/v1/responses` for an
  `lmc` chat and for a request that reaches `apiproxy` at `/v1/responses`.
  `-chat-completions` sends them to `/v1/chat/completions` instead.
- All other models, and `gpt` models in requests that reach `apiproxy` at
  `/v1/messages` or `/v1/chat/completions`, use Argo's OpenAI-compatible
  `/v1/chat/completions` wire format.
- `-argo-legacy` forces the older Argo chat and streamchat endpoints.

## Responses API Compatibility

`apiproxy` serves `/v1/responses` two ways: it passes the request through to a
backend that speaks the Responses API, or it converts the request to another
wire format.

### Direct OpenAI Backend

With `-provider openai`, the proxy maps the model and forwards `/v1/responses`
to OpenAI's own Responses API, unless `-chat-completions` converts the requests
to OpenAI's chat completions. When it forwards them, it:

- Preserves OpenAI Responses request bodies, including valid OpenAI fields the compatibility layer does not understand.
- With `-strip-encrypted-reasoning`, makes an explicit exception to raw
  preservation to recover otherwise unusable replayed state. Reasoning and
  compaction input items carrying `encrypted_content` are removed entirely;
  every other input item and every unrelated request field is retained,
  including reasoning items that carry no encrypted content.
- Returns non-stream and stream responses in OpenAI Responses format.
- Forwards Responses lifecycle and Conversations API calls upstream.
- Rewrites returned model names only where it holds enough request context to restore the client-visible alias.

### Direct Argo Backend

Argo serves an OpenAI-compatible Responses API at
`<argo-host>/argoapi/v1/responses` for its `gpt*` models, and the proxy uses it
by default. `-chat-completions` converts those requests to Argo's chat
completions instead:

```bash
apiproxy -provider argo -argo-user "$USER" -chat-completions
```

Without `-chat-completions`, the proxy:

- Forwards `/v1/responses` requests for `gpt*` backend models to Argo with the
  request body preserved byte-for-byte, apart from `-model-map` rewriting or
  opt-in `-strip-encrypted-reasoning` recovery, and returns Argo's response body
  unchanged apart from restoring the client-visible model alias. Streams pass
  through the same way.
- Still converts requests for every other Argo model, so one proxy serves both
  paths. `-argo-legacy` has no Responses route, so the proxy converts every
  request there.
- Resolves lifecycle routes (`GET`/`DELETE` `/v1/responses/{id}`, `cancel`,
  `input_items`) and Conversations routes against local state first, then falls
  back to Argo, so converted responses stay retrievable while passed-through IDs
  resolve upstream. `POST /v1/conversations` carries no ID to look up, so it goes
  straight to Argo.
- Forwards `/v1/responses/input_tokens` and `/v1/responses/compact` to Argo for
  `gpt*` models and converts them otherwise.

### Converted Backends

With `-provider anthropic`, `-provider google`, `-chat-completions`, or
`-provider argo` on a model that does not use OpenAI Responses upstream, the
proxy rewrites the request as a portable backend call and builds a
Responses-shaped result from the reply.

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
- Anthropic thinking blocks, and the reasoning a Chat Completions backend
  returns in `reasoning_content` or `reasoning`, become Responses `reasoning`
  output items with summary text, which a stream sends as
  `response.reasoning_summary_text.delta` events as it arrives. An item made
  from Chat reasoning carries a marker as its `encrypted_content`, so a client
  that sends the item back has the reasoning returned to the backend in the
  field it came in.
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
applies to every path except `-provider openai`, and `-provider argo` on a
`gpt*` model, without `-chat-completions`. Known gaps:

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
- Give each `apiproxy` process a `-sessions-dir` of its own, apart from `lmc`
  sessions. The local state finds a conversation's last message by session
  path and message ID, so another process that deletes a session there and
  writes another at the same path can make a later request continue the
  replacement's history.
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
- `lmc` tool call records: `~/.lmc/sessions/.journal`
- `lmc -repl` input history: `~/.lmc/sessions/.repl-history` (prompts and
  REPL commands, the last 100; `/history clear` empties it)
- `lmc` logs: `~/.lmc/logs`
- `apiproxy` Responses state: `~/.apiproxy/sessions`
- `alcfauth` token cache and key file: `~/.lmc/alcf/tokens.json` and
  `~/.lmc/alcf/inference.key`
- Built binaries: `./bin/<goos>-<goarch>/lmc`,
  `./bin/<goos>-<goarch>/apiproxy`, and `./bin/<goos>-<goarch>/alcfauth`

## Troubleshooting

- Both binaries ask providers for uncompressed event streams
  (`Accept-Encoding: identity`); apiproxy asks for every response it requests
  that way. A compressed stream reaches the client only when the server's
  compressor flushes, which some servers leave until the response ends; a
  proxy or gateway between you and the provider can still do the same, and
  then streamed text arrives all at once.
- Pass `-log-level DEBUG` to either binary to inspect request routing and conversion warnings.
  DEBUG wire logs include complete, unredacted request, response, and stream
  bytes, the responses of retried attempts among them; they can contain
  credentials and grow without bound on long streams.
- Confirm that the selected `-provider` has credentials or a `-provider-url`.
- When the ALCF gateway answers HTTP 401 or 403, run `alcfauth status -check`.
  An expired token needs `alcfauth token`; a login 30 days old or more needs
  `alcfauth login`, and `alcfauth token` warns from day 29. `apiproxy` reads
  `-api-key-file` again whenever the file changes, so `alcfauth token` or a
  running `alcfauth token -watch` renews its token without a restart.
  `-print-curl` puts the access token in the curl command it prints.
- For Argo, check whether native mode or `-argo-legacy` matches the model and endpoint you intend to use.
- When a converted Responses request loses an OpenAI-only field, switch to
  `-provider openai`, or to a `gpt*` model on `-provider argo`, without
  `-chat-completions`, for direct Responses API passthrough.
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
- A direct Responses backend may return HTTP 200 and then report failure as an
  SSE `event: error`; `apiproxy` forwards that provider event as the sole
  terminal event. If it says a resumed reasoning or `cmp_...` compaction item
  can no longer be decrypted, restart the proxy with
  `-strip-encrypted-reasoning` and retry the session. Recovery preserves the
  visible transcript but intentionally drops the affected reasoning and
  compaction items, losing their opaque model context.

Every path buffers the request body, and converted or stored paths create
additional representations of it. Peak memory depends on payload shape and
concurrency rather than a fixed multiple of body size.
`-max-request-body-size` defaults to 512MB to match OpenAI's own payload limit;
treat that as an admission ceiling, not a per-request memory estimate, and lower
it if the machine cannot safely absorb concurrent large requests.
