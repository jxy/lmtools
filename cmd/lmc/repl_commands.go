package main

import (
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"strings"
	"unicode"
)

const (
	maxREPLHistoryEntries = 100
	maxREPLHistoryBytes   = constants.MaxCLIInputSize

	replHelp = `REPL commands:
  /help             Show this help.
  /history          List prompt text from this invocation (up to 100 entries).
  /ed [N|REGEX]     Edit the draft, history prompt N, or all prompts matching REGEX.
  /image PATH       Attach an image to the next turn; spaces in PATH are accepted.
  /clear-images     Remove pending images.
  /developer TEXT   Set a developer instruction for the next turn.
  /clear-developer  Remove the pending developer instruction.
  /exit             Exit the conversation.
Commands except /ed also work in piped -repl input. Start with // to send a literal /.
Image paths are literal, without quotes or shell expansion. Press Enter with pending
images to send an image-only turn. History recalls text; it does not recall images.
After /ed, Enter sends the draft, typed text appends, and /ed edits it again.
Ctrl-C at the prompt discards a draft. Editor changes are kept only after success.
/ed uses $EDITOR as a shell command with the file passed separately; default: ed.
Digits select positive history IDs; other arguments are Go regexps.
Matching prompts appear in order with blank lines. Use [4]2 to search for 42.
Use bare /ed during continuation to edit unfinished text, or //ed for a literal line.
Draft previews are bounded; /ed reopens complete text. Ctrl-Z resumes the editor.
End a line with \ to continue; use \\ for a literal trailing \.
Terminal editing: Backspace erases, Ctrl-W erases a word, Ctrl-U clears the line.
Ctrl-C cancels the current turn; Ctrl-D or /exit exits.
`
)

type replPrompt struct {
	id   int
	text string
}

// replHistory bounds both the prompt count and the retained text. IDs remain
// stable as earlier entries expire, so /ed never selects a different prompt.
type replHistory struct {
	entries []replPrompt
	nextID  int
	bytes   int
}

func (h *replHistory) add(text string) {
	if text == "" {
		return
	}
	h.nextID++
	h.entries = append(h.entries, replPrompt{id: h.nextID, text: text})
	h.bytes += len(text)
	for len(h.entries) > maxREPLHistoryEntries || h.bytes > maxREPLHistoryBytes {
		h.bytes -= len(h.entries[0].text)
		h.entries[0] = replPrompt{}
		h.entries = h.entries[1:]
	}
}

func (h *replHistory) get(id int) (string, bool) {
	for _, entry := range h.entries {
		if entry.id == id {
			return entry.text, true
		}
	}
	return "", false
}

type replAction struct {
	text string
	send bool
	exit bool
}

// interpretInput handles commands after the existing input owner has read a
// complete turn. It never opens a second reader or interprets shell syntax.
func (l *replLoop) interpretInput(text string) replAction {
	if strings.HasPrefix(text, "//") {
		return replAction{text: text[1:], send: true}
	}
	if !strings.HasPrefix(text, "/") {
		return replAction{text: text, send: true}
	}
	command, argument := splitREPLCommand(text)
	switch command {
	case "/help", "/history", "/clear-images", "/clear-developer", "/exit":
		if argument != "" {
			l.env.notifier.Errorf("%s takes no arguments. Use /help for commands.", command)
			return replAction{}
		}
	}
	switch command {
	case "/help":
		l.env.notifier.Promptf("%s", replHelp)
	case "/history":
		if len(l.history.entries) == 0 {
			l.env.notifier.Infof("No prompt text in this invocation's history yet.")
		}
		for _, entry := range l.history.entries {
			l.env.notifier.Promptf("%d: %q\n", entry.id, replPromptPreview(entry.text))
		}
	case "/ed":
		l.editDraft(argument)
	case "/again":
		l.env.notifier.Errorf("Use /ed N to edit a history prompt, then Enter to send it.")
	case "/image":
		if argument == "" || strings.ContainsAny(argument, "\r\n") {
			l.env.notifier.Errorf("Use /image PATH on one line; spaces in the path are accepted.")
			return replAction{}
		}
		if l.base.ArgoLegacy {
			l.env.notifier.Errorf("/image is not supported with -argo-legacy.")
			return replAction{}
		}
		image, err := core.LoadImageFile(argument, constants.MaxCLIImageBytes)
		if err != nil {
			l.env.notifier.Errorf("%v", err)
			return replAction{}
		}
		image.Detail = strings.ToLower(strings.TrimSpace(l.env.cfg.ImageDetail))
		l.images = append(l.images, image)
		l.env.notifier.Infof("Attached %s; %d image(s) pending for the next turn.", core.DescribeImageBlock(image), len(l.images))
	case "/clear-images":
		l.images = nil
		l.env.notifier.Infof("Cleared pending images.")
	case "/developer":
		if argument == "" {
			l.env.notifier.Errorf("Use /developer TEXT to set an instruction for the next turn.")
			return replAction{}
		}
		opts := l.base
		opts.Developer = argument
		if err := core.ValidateDeveloperInstructions(opts); err != nil {
			l.env.notifier.Errorf("%v", err)
			return replAction{}
		}
		l.base.Developer = argument
		l.env.notifier.Infof("Developer instruction queued for the next turn.")
	case "/clear-developer":
		l.base.Developer = ""
		l.env.notifier.Infof("Cleared pending developer instruction.")
	case "/exit":
		return replAction{exit: true}
	default:
		l.env.notifier.Errorf("Unknown REPL command %q. Use /help, or start with // to send a literal /.", command)
	}
	return replAction{}
}

func splitREPLCommand(text string) (command, argument string) {
	if i := strings.IndexFunc(text, unicode.IsSpace); i >= 0 {
		return text[:i], strings.TrimSpace(text[i:])
	}
	return text, ""
}

// replPromptPreview keeps terminal labels and history listings readable while
// /ed selects the original prompt, including all continuation lines.
func replPromptPreview(text string) string {
	runes := 0
	for i := range text {
		if runes == 120 {
			return text[:i] + "..."
		}
		runes++
	}
	return text
}
