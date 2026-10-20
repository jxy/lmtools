package main

const replHelp = `REPL commands:
  /info                  Show connection, session, tools, and pending input.
  /help [input|history|tools]  Show help for a topic.
  /history [N]           List this invocation's prompts, or show prompt N in full.
  /show                  Show the saved conversation.
  /ed [N|REGEX]          Edit a draft or recalled prompts with $EDITOR.
  /image PATH            Attach an image to the next turn.
  /images                List pending images.
  /remove-image N        Remove one pending image.
  /clear-images          Remove all pending images.
  /developer TEXT        Add an instruction with the next turn; it stays in history.
  /clear-developer       Remove only the pending instruction.
  /thinking on|off       Show or hide returned thinking; does not enable reasoning.
  /usage on|off          Show or hide reported token counts.
  /exit                  Exit; report anything left unsent.
Ctrl-C cancels; Ctrl-D exits. Start with // to send a literal leading /.
`

const replInputHelp = `Input:
Enter sends a turn. An empty line sends nothing unless a draft or images are pending.
End a line with \ to continue; use \\ for a literal trailing \.
Bracketed paste stays one editable draft; Enter sends it. Unbracketed pasted lines
and piped input remain one turn per line. Text queued during a response appears
at the next prompt; approval questions discard queued input.
Left/Right move; Home/End or Ctrl-A/Ctrl-E move to line ends. Backspace/Delete erase.
Ctrl-W erases a word; Ctrl-U clears input. Up/Down recall prompts; Ctrl-R searches.
During search, type or paste a query; Ctrl-R finds older matches. Escape restores
the draft and cursor. Enter accepts the match for editing; another Enter sends it.
Ctrl-C discards unfinished input; during a response it cancels the turn.
Ctrl-D exits without sending unfinished input. /exit also reports unsent state.
Ctrl-\ exits after cancelling active work and restoring the terminal.
/ed opens $EDITOR (default: ed). Exit the editor to review its multiline preview;
Enter sends, typed text appends verbatim, /ed revises, and Ctrl-C discards the draft.
A bare /ed during continuation edits unfinished text; //ed sends a literal line.
Paths in /image are literal: no quotes, ~ expansion, or shell escaping.
Image-only turns are sent by Enter. /images lists them; /remove-image N removes one.
`

const replHistoryHelp = `History:
/history lists prompt text from this invocation, retaining up to 100 prompts and
10 MiB. These prompt numbers are separate from saved session/message references.
/history N shows the complete prompt; /ed N opens it as a new draft.
/ed REGEX selects every matching prompt, in order with blank lines between them.
Digits select a prompt number; use [4]2 to search for the text 42.
History recall includes text, not earlier image attachments.
/show displays the saved conversation, including tool results and attachments.
/info shows the session, current message, and logs. Sessions are saved as turns
commit; failed requests can leave images and instructions pending for the next turn.
Developer instructions are inserted once and replayed with saved history.
/clear-developer affects the pending instruction only.
`

const replToolsHelp = `Tools:
Each review shows the exact command arguments or tool name and arguments, including
environment, input, working directory, and output destinations when specified.
y or yes approves. Enter, n, or no declines. Other text declines with your reason.
/ed edits a reason; continuation lines work as at the normal prompt.
Ctrl-C cancels the turn. Ctrl-D discards an unsubmitted approval in the REPL editor.
Input typed before an approval question cannot answer it.
An unknown earlier outcome is shown before asking whether to run that call again.
Results distinguish not run, completed, failed, timed out, cancelled, and unknown.
Output boundaries preserve partial output and identify execution-time truncation.
Token counts belong to each model response; tool follow-ups have separate counts.
/info lists the enabled tools and execution settings.
`

func (l *replLoop) showHelp(topic string) {
	text := replHelp
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
