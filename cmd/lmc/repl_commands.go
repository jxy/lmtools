package main

import (
	"errors"
	"io/fs"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/prompts"
	"lmtools/internal/session"
	"strconv"
	"strings"
	"unicode"
)

const (
	maxREPLHistoryEntries = 100
	maxREPLHistoryBytes   = constants.MaxCLIInputSize
)

// replPrompt is one history entry: a prompt sent to the model, or a REPL
// command, which Up and Ctrl-R recall as typed and /history and /ed leave out.
type replPrompt struct {
	id      int
	text    string
	command bool
}

// replHistory bounds both the entry count and the retained text. IDs remain
// stable as earlier entries expire, so /ed never selects a different prompt.
// With a file, entries are saved as they are added and reloaded by the next
// REPL, so Up and Ctrl-R reach earlier sessions.
type replHistory struct {
	entries []replPrompt
	nextID  int
	bytes   int
	file    *historyFile
}

func (h *replHistory) add(text string) {
	h.addEntry(text, false)
}

// addCommand records a REPL command for recall.
func (h *replHistory) addCommand(text string) {
	h.addEntry(text, true)
}

func (h *replHistory) addEntry(text string, command bool) {
	if text == "" {
		return
	}
	h.remember(text, command)
	if h.file != nil {
		h.file.append(text, command)
	}
}

func (h *replHistory) remember(text string, command bool) {
	h.nextID++
	h.entries = append(h.entries, replPrompt{id: h.nextID, text: text, command: command})
	h.bytes += len(text)
	for len(h.entries) > maxREPLHistoryEntries || h.bytes > maxREPLHistoryBytes {
		h.bytes -= len(h.entries[0].text)
		h.entries[0] = replPrompt{}
		h.entries = h.entries[1:]
	}
}

// clear forgets every entry, and empties the file.
func (h *replHistory) clear() error {
	h.entries, h.bytes = nil, 0
	if h.file != nil {
		return h.file.clear()
	}
	return nil
}

func (h *replHistory) get(id int) (string, bool) {
	for _, entry := range h.entries {
		if entry.id == id && !entry.command {
			return entry.text, true
		}
	}
	return "", false
}

// prompts are the entries /history lists and /ed selects from.
func (h *replHistory) prompts() []replPrompt {
	prompts := make([]replPrompt, 0, len(h.entries))
	for _, entry := range h.entries {
		if !entry.command {
			prompts = append(prompts, entry)
		}
	}
	return prompts
}

// isREPLCommand reports text the loop interprets rather than sends.
func isREPLCommand(text string) bool {
	return strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "//")
}

type replAction struct {
	text    string
	send    bool
	exit    bool
	compact bool
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
	case "/info", "/images", "/clear-images", "/clear-developer", "/exit":
		if argument != "" {
			l.env.notifier.Errorf("%s takes no arguments. Use /help for commands.", command)
			return replAction{}
		}
	}
	switch command {
	case "/help":
		l.showHelp(argument)
	case "/pin", "/pins", "/unpin":
		ctx, end := l.commandContext()
		if command == "/pin" {
			_, argument = pinField(text)
		}
		err := l.pinCommand(ctx, command, argument)
		end()
		if err != nil {
			l.env.notifier.Errorf("%v", err)
			l.failedTurns++
		}
	case "/info":
		l.showInfo()
	case "/compact":
		if argument != "" {
			l.env.notifier.Errorf("/compact: use /compact without arguments")
			l.failedTurns++
			return replAction{}
		}
		return replAction{compact: true}
	case "/handoff":
		return l.handoffAction(argument)
	case "/show":
		l.showConversation(argument)
	case "/model":
		l.setModel(argument)
	case "/effort":
		l.setEffort(argument)
	case "/tool-output":
		l.setToolOutput(argument)
	case "/bell":
		l.setBell(argument)
	case "/thinking", "/usage":
		l.setDisplay(command, argument)
	case "/history":
		l.showHistory(argument)
	case "/images":
		l.showImages()
	case "/remove-image":
		n, err := strconv.Atoi(argument)
		if err != nil || n < 1 || n > len(l.images) {
			l.env.notifier.Errorf("Use /images for pending image numbers, then /remove-image N.")
			return replAction{}
		}
		l.images = append(l.images[:n-1], l.images[n:]...)
		l.env.notifier.Infof("Removed image %d; %d pending.", n, len(l.images))
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
			// The hint explains a path that was not found because of how it
			// was spelled, and nothing else.
			if errors.Is(err, fs.ErrNotExist) && (strings.ContainsAny(argument, `"'`) || strings.HasPrefix(argument, "~")) {
				l.env.notifier.Errorf("%v. Image paths are literal: omit shell quotes and use a full path instead of ~.", err)
			} else {
				l.env.notifier.Errorf("%v.", err)
			}
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
		l.env.notifier.Infof("Developer instruction queued for the next turn; it will remain in saved conversation history.")
	case "/clear-developer":
		l.base.Developer = ""
		l.env.notifier.Infof("Cleared pending developer instruction; saved instructions are unchanged.")
	case "/exit":
		return replAction{exit: true}
	default:
		l.env.notifier.Errorf("Unknown REPL command %q. Use /help, or start with // to send a literal /.", command)
	}
	return replAction{}
}

func (l *replLoop) handoffAction(argument string) replAction {
	for _, tool := range core.AdvertisedTools(l.base) {
		if tool.MCPServer == "notebook" && tool.MCPTool == "put" {
			switch argument {
			case "":
				return replAction{text: strings.TrimSpace(prompts.NotebookHandoff), send: true}
			case "versions":
				return replAction{text: strings.TrimSpace(prompts.NotebookVersions), send: true}
			default:
				l.env.notifier.Errorf("Use /handoff or /handoff versions.")
				l.failedTurns++
				return replAction{}
			}
		}
	}
	l.env.notifier.Errorf("/handoff requires the notebook put tool")
	l.failedTurns++
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

// setModel changes the model of the turns that follow; the session keeps the
// model of each answer it saved.
func (l *replLoop) setModel(model string) {
	if model == "" {
		l.env.notifier.Infof("Model: %s (%s API). /model NAME changes it for the next turns.", core.EffectiveChatModel(l.base), replAPIName(l.base))
		return
	}
	if strings.IndexFunc(model, unicode.IsSpace) >= 0 {
		l.env.notifier.Errorf("Use /model NAME; a model name has no spaces.")
		return
	}
	next := l.base
	next.Model = model
	if next.Context.Enabled() {
		var lineage []session.Message
		if at := l.current(); at.Message != "" {
			var err error
			if lineage, err = l.env.store.Lineage(at); err != nil {
				l.env.notifier.Errorf("%v", err)
				return
			}
		}
		ctx, end := l.commandContext()
		err := resolveWindow(ctx, l.env.cfg, &next, l.env.notifier, l.env.logDir, lineage)
		end()
		if err != nil {
			l.env.notifier.Errorf("%v", err)
			return
		}
	}
	l.base = next
	l.env.notifier.Infof("Model set to %s for the next turns (%s API).", model, replAPIName(l.base))
	l.warnHiddenThinking()
}

// setEffort takes what -effort takes, in any case, or default, which sends
// none.
func (l *replLoop) setEffort(effort string) {
	current := l.base.Effort
	if current == "" {
		current = "default"
	}
	levels := strings.Join(core.EffortLevels, ", ")
	switch level := core.NormalizeEffort(effort); {
	case effort == "":
		l.env.notifier.Infof("Reasoning effort: %s. /effort LEVEL changes it: %s, or default.", current, levels)
		return
	case strings.EqualFold(effort, "default"):
		effort = "default"
		l.base.Effort = ""
	case level != "":
		effort = level
		l.base.Effort = level
	default:
		l.env.notifier.Errorf("Unknown effort %q; use %s, or default.", effort, levels)
		return
	}
	l.env.notifier.Infof("Reasoning effort set to %s for the next turns.", effort)
}

// setToolOutput chooses between the first and last lines of long output and
// all of it, on the terminal; the model always receives everything.
func (l *replLoop) setToolOutput(value string) {
	ui, ok := l.env.toolUI.(*replToolUI)
	if !ok {
		l.env.notifier.Errorf("/tool-output applies to a terminal; tool output is shown in full here.")
		return
	}
	switch value {
	case "full":
		ui.Options.ShortOutput = false
		l.env.notifier.Infof("Tool output is shown in full.")
	case "short":
		ui.Options.ShortOutput = true
		l.env.notifier.Infof("Long tool output shows its first and last lines; /show last prints the rest.")
	case "":
		mode := "full"
		if ui.Options.ShortOutput {
			mode = "short"
		}
		l.env.notifier.Infof("Tool output: %s. Use /tool-output full or /tool-output short.", mode)
	default:
		l.env.notifier.Errorf("Use /tool-output full or /tool-output short.")
	}
}

// setBell turns on a terminal bell when a question waits for an answer and
// when a turn ends, for an operator who has looked away during a long turn.
func (l *replLoop) setBell(value string) {
	switch value {
	case "on", "off":
		l.bell = value == "on"
		l.env.notifier.Infof("Bell %s.", value)
	default:
		l.env.notifier.Errorf("Use /bell on or /bell off.")
	}
}
