package main

import (
	"fmt"
	"strings"
)

// replCommands is the /help table, in the order a newcomer needs them.
var replCommands = []struct{ usage, effect string }{
	{"/help [input|history|tools]", "Show these commands, or help on a topic."},
	{"/info", "Show the model, endpoint, session, tools, and settings."},
	{"/model [NAME]", "Show the model, or change it for the next turns."},
	{"/effort [LEVEL|default]", "Show the reasoning effort, or change it for the next turns."},
	{"/history [N|clear]", "List recent prompts, show prompt N in full, or clear the history."},
	{"/show [last [N]|all|system]", "Show the saved conversation, its last turns, or its system prompt."},
	{"/show output [N]", "Show a shortened tool output in full, or the latest; works at questions."},
	{"/ed [N|REGEX]", "Edit a draft or recalled prompts with $VISUAL or $EDITOR."},
	{"/image PATH", "Attach an image to the next turn."},
	{"/images", "List pending images."},
	{"/remove-image N", "Remove one pending image."},
	{"/clear-images", "Remove all pending images."},
	{"/developer TEXT", "Add an instruction with the next turn; it stays in history."},
	{"/clear-developer", "Remove only the pending instruction."},
	{"/thinking on|off", "Show or hide returned thinking; does not enable reasoning."},
	{"/usage on|off", "Show or hide reported token counts."},
	{"/tool-output full|short", "Show long tool output in full, or its first and last lines."},
	{"/bell on|off", "Ring the terminal when a question waits and when a turn ends."},
	{"/exit", "Exit, reporting anything unsent and the session to resume."},
}

// replHelpText lays the commands out in two columns as wide as the longest
// usage, so no description starts out of line.
func replHelpText() string {
	width := 0
	for _, command := range replCommands {
		width = max(width, len(command.usage))
	}
	var b strings.Builder
	b.WriteString("REPL commands:\n")
	for _, command := range replCommands {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, command.usage, command.effect)
	}
	b.WriteString("Ctrl-C cancels; Ctrl-D on an empty line exits. Start with // to send a literal leading /.\n")
	return b.String()
}

const replInputHelp = `Input:
Enter sends a turn. An empty line sends nothing unless a draft or images are pending.
A long line wraps across rows as you type, and the whole turn stays in the scrollback.
End a line with \ to continue; use \\ for a literal trailing \.
Bracketed paste stays one editable draft; Enter sends it. Unbracketed pasted lines
and piped input remain one turn per line. Text queued during a response appears
at the next prompt; approval questions discard queued input.
Left/Right move; Home/End or Ctrl-A/Ctrl-E move to line ends. Backspace/Delete erase.
Ctrl-W erases a word; Ctrl-U clears input. Up/Down recall prompts and commands,
including those of earlier REPLs; Ctrl-R searches them.
During search, type or paste a query; Ctrl-R finds older matches. Escape restores
the draft and cursor. Enter accepts the match for editing; another Enter sends it.
Ctrl-C discards unfinished input; during a response it cancels the turn, keeping
an answer that had begun.
Ctrl-D deletes the character under the cursor; on an empty line it exits, and on
the empty line of an unfinished turn it exits without sending it.
/exit also reports unsent state and the session to resume.
Ctrl-\ exits after cancelling active work and restoring the terminal.
/ed opens $VISUAL, else $EDITOR, else vi. Exit the editor to review its preview;
Enter sends, typed text appends verbatim, /ed revises, and Ctrl-C discards the draft.
A bare /ed during continuation edits unfinished text; //ed sends a literal line.
Paths in /image are literal: no quotes, ~ expansion, or shell escaping.
Image-only turns are sent by Enter. /images lists them; /remove-image N removes one.
Answers are shown as the model wrote them, markdown included. To ask for plain
text, use /developer, for example: /developer Answer in plain text without markdown.
`

const replHistoryHelp = `History:
Up, Down, and Ctrl-R recall prompts and commands, including those of earlier
REPLs: up to 100 entries are kept in .repl-history in the sessions directory.
/history clear empties it.
/history lists the prompts; these numbers are separate from saved session and
message references. /history N shows the complete prompt; /ed N opens it as a
new draft. /ed REGEX selects every matching prompt, in order with blank lines
between them. Digits select a prompt number; use [4]2 to search for the text 42.
History recall includes text, not earlier image attachments.
/show displays the saved conversation, including tool results and attachments;
/show last shows the last turn and /show last N the last N, with tool output in full.
/show all includes the system prompt, and /show system shows it alone.
/show output N prints shortened output N in full, and /show output the latest;
the REPL keeps 64 MiB of shortened output.
On a terminal, /show opens in $PAGER, else less, when taller than the screen.
/info shows the session, current message, and logs. Sessions are saved as turns
commit; failed requests can leave images and instructions pending for the next turn.
Developer instructions are inserted once and replayed with saved history.
/clear-developer affects the pending instruction only.
`

const replToolsHelp = `Tools:
A round's calls are all shown first, each with its exact command arguments or tool
name and arguments, and the environment, input, working directory, and output
destinations when specified. A program found outside the system directories is
named. Arguments spanning lines, such as scripts, are shown line by line.
Then each call that needs approval is asked about by number and name.
y or yes approves. a or all approves this call and the rest of the round.
Enter answers with the capital letter: [y/n/A] approves this call and the rest of
the round, and [Y/n], at the last or only question, approves the call.
n or no declines. Other text declines with your reason.
/ed edits a reason; continuation lines work as at the normal prompt.
At a question, /show output [N] and /help run, other commands get a note, and
the question is asked again. A reason starting with / begins with //.
Ctrl-C cancels the turn. Ctrl-D on an empty answer declines.
Input typed before an approval question cannot answer it.
A command whose program or working directory is missing shows Cannot start now:
under its review; it is still asked about and run, since an earlier call may create it.
An unknown earlier outcome is shown before asking whether to run that call again;
that question is [y/N], and Enter declines it.
Results appear in order as calls finish, quoted under each call; long output shows
its first and last lines, and the note under it names the /show output N that
prints it whole, at the prompt or at the next question (/tool-output full shows
all).
A line longer than the terminal wraps, and its continuation rows start with "  : ".
Results distinguish not run, completed, failed, timed out, cancelled, and unknown.
Token counts belong to each model response; tool follow-ups have separate counts.
/info lists the enabled tools and execution settings.
`

func (l *replLoop) showHelp(topic string) {
	text := replHelpText()
	switch topic {
	case "":
	case "input":
		text = replInputHelp
	case "history":
		text = replHistoryHelp
	case "tools":
		text = replToolsHelp
	default:
		l.env.notifier.Errorf("Unknown help topic %q. Use /help input, /help history, or /help tools.", topic)
		return
	}
	l.env.notifier.Promptf("%s", text)
}
