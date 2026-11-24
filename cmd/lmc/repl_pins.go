package main

import (
	"context"
	"errors"
	"lmtools/internal/core"
	session "lmtools/internal/oldsession"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Consume the command separator and keep the user's literal remainder.
func pinField(line string) (string, string) {
	at := strings.IndexFunc(line, unicode.IsSpace)
	if at < 0 {
		return line, ""
	}
	_, n := utf8.DecodeRuneInString(line[at:])
	return line[:at], line[at+n:]
}

func (l *replLoop) pinCommand(ctx context.Context, command, argument string) error {
	opts := l.base
	if opts.Context.Notebook == "" || l.env.cfg.NoSession {
		return errors.New("standing pins require a saved session with -notebook DIR")
	}
	// A local command doesn't consume the next turn's developer text or images.
	opts.Developer, opts.Images = "", nil
	var plan *session.RequestPlan
	var err error
	if l.from != nil {
		pinned := *l.from
		plan, err = session.PrepareRequestAt(ctx, opts, l.env.notifier, l.env.toolUI, &pinned, "", false, session.PendingToolPreview)
	} else {
		plan, err = session.PrepareRequest(ctx, opts, l.env.notifier, l.env.toolUI, "", false, session.PendingToolPreview)
	}
	if err != nil {
		return err
	}
	pins, err := session.ActivePins(plan.Messages, plan.Refs)
	if err != nil {
		return err
	}
	if command == "/pins" {
		if argument != "" {
			return errors.New("/pins takes no arguments")
		}
		if len(pins) == 0 {
			l.env.notifier.Infof("No active user instructions.")
		} else {
			l.env.notifier.Infof("%s", session.RenderPins(pins))
		}
		return nil
	}
	if plan.HasPendingTools {
		return errors.New("finish the pending tool exchange before changing standing instructions")
	}
	if n := len(plan.Refs); n > 0 && plan.Messages[n-1].Role == "assistant" {
		for _, b := range plan.Messages[n-1].Blocks {
			if _, ok := b.(core.ToolUseBlock); ok {
				return errors.New("finish the pending tool exchange before changing standing instructions")
			}
		}
	}
	change := session.PinChange{Action: "add"}
	text := argument
	first, rest := pinField(argument)
	switch {
	case command == "/unpin":
		change.Action, change.ID, text = "remove", argument, ""
	case first == "--replace":
		change.Action = "replace"
		change.ID, text = pinField(strings.TrimLeftFunc(rest, unicode.IsSpace))
	case first == "--":
		text = rest
	case first == "--from":
		parts := strings.Fields(rest)
		if len(parts) != 1 && len(parts) != 3 {
			return errors.New("use /pin --from REF [START END]")
		}
		change.Source = parts[0]
		var span []int
		for _, word := range parts[1:] {
			n, err := strconv.Atoi(word)
			if err != nil {
				return errors.New("pin offsets must be integers")
			}
			span = append(span, n)
		}
		text, change.Start, err = session.UserPinText(plan.Messages, plan.Refs, change.Source, span)
		if err != nil {
			return err
		}
	}
	if change.Action == "add" {
		for _, p := range pins {
			if p.Text == text && (change.Source == "" || p.Source == change.Source && p.Start == change.Start) {
				l.env.notifier.Infof("Pin %s already active.", p.ID)
				return nil
			}
		}
	}
	entry, err := session.PinEntry(change, text, pins)
	if err != nil {
		return err
	}
	sess, err := plan.Commit(ctx)
	if sess != nil {
		l.from = sess
	}
	if err != nil {
		return err
	}
	if err = session.BindContext(ctx, sess, opts.Context); err != nil {
		return err
	}
	if _, err = session.AppendMessageWithBlocks(ctx, sess, entry.Message, nil, nil, entry.Blocks); err != nil {
		return err
	}
	l.showSession(sess)
	l.env.notifier.Infof("Pin %s: %s.", entry.Message.Pin.ID, change.Action)
	return nil
}
