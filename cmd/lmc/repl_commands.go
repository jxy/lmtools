package main

import (
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"strconv"
	"strings"
	"unicode"
)

const (
	maxREPLHistoryEntries = 100
	maxREPLHistoryBytes   = constants.MaxCLIInputSize
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
	case "/info", "/show", "/images", "/clear-images", "/clear-developer", "/exit":
		if argument != "" {
			l.env.notifier.Errorf("%s takes no arguments. Use /help for commands.", command)
			return replAction{}
		}
	}
	switch command {
	case "/help":
		l.showHelp(argument)
	case "/info":
		l.showInfo()
	case "/show":
		l.showConversation()
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
			l.env.notifier.Errorf("%v. Image paths are literal: omit shell quotes and use a full path instead of ~.", err)
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
