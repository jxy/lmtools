package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"lmtools/internal/limitio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
)

var errEditorTerminal = errors.New("restore editor terminal")

const maxEditorPreviewBytes = 2048

func editorCommand(configured, path string) *exec.Cmd {
	var cmd *exec.Cmd
	if strings.TrimSpace(configured) == "" {
		cmd = exec.Command("ed", path)
	} else {
		// EDITOR is the operator's shell command. The file name stays a
		// positional argument even when the command contains quotes/arguments.
		cmd = exec.Command("/bin/sh", "-c", "exec "+configured+` "$1"`, "lmc-editor", path)
	}
	return cmd
}

func selectEditorSeed(history *replHistory, draft, argument string, maxBytes int) (string, error) {
	if argument == "" {
		if len(draft) > maxBytes {
			return "", fmt.Errorf("draft is longer than %d bytes", maxBytes)
		}
		return draft, nil
	}
	numeric := true
	for _, c := range argument {
		if c < '0' || c > '9' {
			numeric = false
			break
		}
	}
	if numeric {
		id, err := strconv.Atoi(argument)
		if err != nil {
			return "", fmt.Errorf("history prompt number is too large; use /history")
		}
		if id > 0 {
			text, ok := history.get(id)
			if !ok {
				return "", fmt.Errorf("prompt %d is unavailable; use /history for retained prompt numbers", id)
			}
			if len(text) > maxBytes {
				return "", fmt.Errorf("selected prompt is longer than %d bytes", maxBytes)
			}
			return text, nil
		}
		return "", errors.New("history prompt numbers must be positive; use [0] to search for 0")
	}
	expression, err := regexp.Compile(argument)
	if err != nil {
		return "", fmt.Errorf("invalid history regular expression: %w", err)
	}
	var matches []string
	size := 0
	for _, entry := range history.entries {
		if expression.MatchString(entry.text) {
			if len(matches) > 0 {
				size += 2
			}
			size += len(entry.text)
			if size > maxBytes {
				return "", fmt.Errorf("matching prompts together are longer than %d bytes", maxBytes)
			}
			matches = append(matches, entry.text)
		}
	}
	if len(matches) == 0 {
		return "", errors.New("no retained history prompts match the regular expression")
	}
	return strings.Join(matches, "\n\n"), nil
}

func readEditorDraft(path string, maxBytes int) (string, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("editor draft is not a regular file")
	}
	data, err := limitio.ReadLimited(file, int64(maxBytes))
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("editor draft must be UTF-8 text without NUL bytes")
	}
	return string(data), nil
}

func (l *replLoop) editDraft(argument string) {
	if !l.terminal || l.input.term == nil {
		l.env.notifier.Errorf("/ed requires terminal input.")
		return
	}
	seed, err := selectEditorSeed(&l.history, l.draft, argument, maxTurnBytes)
	if err != nil {
		l.env.notifier.Errorf("Select editor draft; kept the existing draft: %v", err)
		return
	}
	dir, err := os.MkdirTemp("", "lmc-draft-*")
	if err != nil {
		l.env.notifier.Errorf("Create editor draft: %v", err)
		return
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			l.env.notifier.Warnf("Remove editor draft: %v", err)
		}
	}()
	path := filepath.Join(dir, "draft.txt")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		l.env.notifier.Errorf("Create editor draft: %v", err)
		return
	}
	_, err = file.WriteString(seed)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		l.env.notifier.Errorf("Write editor draft: %v", err)
		return
	}
	ctx, end := l.signals.begin()
	configured := os.Getenv("EDITOR")
	cmd := editorCommand(configured, path)
	err = l.input.runEditor(ctx, cmd, func(discarded bool) {
		if discarded {
			l.env.notifier.Infof("Discarded input queued before the editor opened.")
		}
		l.env.notifier.Infof("Opening editor; exit the editor to review the draft.")
	})
	end()
	if err == nil && errors.Is(context.Cause(ctx), errInterrupted) {
		// An interrupt after handback belongs to draft review, where the
		// next prompt discards the draft instead of losing the interrupt.
		l.signals.interrupt()
	}
	l.env.notifier.Promptf("\n")
	if err != nil {
		if errors.Is(err, errEditorTerminal) {
			l.signals.endProcess(err)
			return
		}
		if l.signals.ended() != nil {
			return
		}
		if errors.Is(context.Cause(ctx), errInterrupted) {
			l.env.notifier.Infof("Editor cancelled; kept the existing draft.")
		} else {
			l.env.notifier.Errorf("Editor failed; kept the existing draft: %v", err)
		}
		return
	}
	draft, err := readEditorDraft(path, maxTurnBytes)
	if err != nil {
		l.env.notifier.Errorf("Read editor draft; kept the existing draft: %v", err)
		return
	}
	l.draft = draft
	preview := draft
	if len(preview) > maxEditorPreviewBytes {
		end := maxEditorPreviewBytes
		for !utf8.RuneStart(preview[end]) {
			end--
		}
		preview = preview[:end]
	}
	l.env.notifier.Promptf("Draft (%d bytes): %q\n", len(draft), preview)
	if preview != draft {
		l.env.notifier.Infof("Draft preview truncated to %d bytes; /ed reopens the complete text.", len(preview))
	}
	if _, err := l.input.discardTerminalInput(); err != nil {
		l.signals.endProcess(fmt.Errorf("clear input after draft review: %w", err))
		return
	}
	if strings.TrimSpace(draft) == "" && len(l.images) == 0 {
		l.env.notifier.Infof("Draft is empty; type text or use /ed to revise.")
	} else {
		l.env.notifier.Infof("Enter sends the draft; type to append, /ed to revise, or Ctrl-C to discard.")
	}
}
