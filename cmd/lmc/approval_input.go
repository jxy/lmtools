package main

import (
	"context"
	"errors"
	"fmt"
	"lmtools/internal/ui/tools"
	"strings"
)

// composeAnswer uses the normal turn reader and editor. The completed text is
// classified only after submission, so an edited y/yes is an approval too.
func (a *cliApprover) composeAnswer(ctx context.Context, initial inputLine) (inputLine, error) {
	owner, _ := a.answers.(*inputOwner)
	terminal := owner != nil && owner.terminal && owner.term != nil
	first := &initial
	read := func(ctx context.Context, bound int) (inputLine, error) {
		if first != nil {
			line := *first
			first = nil
			return line, nil
		}
		return a.answers.readLineUnder(ctx, bound)
	}
	show := func(prompt string) {
		if first == nil { // The initial approval question was already shown.
			if owner != nil {
				owner.setPrompt(tools.DetailIndent + prompt)
			}
			a.notifier.Promptf("%s%s", tools.DetailIndent, prompt)
		}
	}
	var draft string
	for {
		text, how, err := readTextTurn(ctx, read, terminal, show, func() {}, true)
		if err != nil {
			return inputLine{}, err
		}
		if err := ctx.Err(); err != nil {
			return inputLine{}, err
		}
		if how == readTooLong {
			return inputLine{tooLong: true}, nil
		}
		if how == readEnd || how == readEndInsideTurn {
			if draft != "" || how == readEndInsideTurn {
				a.notifier.Infof("Unsubmitted approval draft discarded.")
			}
			return inputLine{eof: true}, nil
		}

		var argument string
		edit := how == readEdit
		if edit {
			if len(draft)+len(text) > maxTurnBytes {
				a.notifier.Errorf("Draft plus input is longer than %d bytes; kept the existing draft.", maxTurnBytes)
				continue
			}
			draft += text
		} else if strings.HasPrefix(text, "//") {
			text = text[1:]
		} else {
			command, arg := splitREPLCommand(text)
			edit, argument = command == "/ed", arg
		}
		if !edit {
			if len(draft)+len(text) > maxTurnBytes {
				a.notifier.Errorf("Draft plus input is longer than %d bytes; kept the existing draft.", maxTurnBytes)
				continue
			}
			return inputLine{text: draft + text}, nil
		}

		edited, err := a.editAnswer(ctx, owner, draft, argument)
		if err != nil {
			if errors.Is(err, errEditorTerminal) {
				if a.stop != nil {
					a.stop(err)
				}
				return inputLine{}, err
			}
			if ctx.Err() != nil {
				return inputLine{}, ctx.Err()
			}
			a.notifier.Errorf("Editor failed; kept the existing draft: %v", err)
		} else {
			draft = edited
		}
		a.notifier.Infof("Enter submits the answer; type to append or use /ed to revise. y/yes approves; other text denies.")
	}
}

func (a *cliApprover) editAnswer(ctx context.Context, owner *inputOwner, draft, argument string) (string, error) {
	if owner == nil || !owner.terminal || owner.term == nil {
		return "", fmt.Errorf("/ed requires terminal input")
	}
	history := a.history
	if history == nil {
		history = &replHistory{}
	}
	seed, err := selectEditorSeed(history, draft, argument, maxTurnBytes)
	if err != nil {
		return "", err
	}
	return editInputDraft(ctx, owner, a.notifier, seed)
}
