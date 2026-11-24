package main

import (
	"context"
	"errors"
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
	"lmtools/internal/session"
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
	if opts.Context.Notebook == "" {
		return errors.New("standing pins require a saved session with -notebook DIR")
	}
	// A local command doesn't consume the next turn's developer text or images.
	opts.Developer, opts.Images = "", nil
	start := l.current()
	// at is the session the pin follows, a copy of the loop's, which keeps
	// the history it reads for the plan as well.
	var at *session.Session
	if l.from != nil {
		copied := *l.from
		at = &copied
	} else if start.Session != "" {
		var err error
		if at, err = l.env.store.Session(start); err != nil {
			return err
		}
	}
	var view session.View
	if at != nil {
		var err error
		if view, err = at.View(opts); err != nil {
			return err
		}
	}
	pins, err := contextstate.ActivePins(view.Messages, view.Refs)
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
			l.env.notifier.Infof("%s", contextstate.RenderPins(pins))
		}
		return nil
	}
	if n := len(view.Messages); n > 0 && view.Messages[n-1].Role == "assistant" {
		for _, b := range view.Messages[n-1].Blocks {
			if _, ok := b.(core.ToolUseBlock); ok {
				return errors.New("finish the pending tool exchange before changing standing instructions")
			}
		}
	}
	change := contextstate.PinChange{Action: "add"}
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
		text, change.Start, err = contextstate.UserPinText(view.Messages, view.Refs, change.Source, span)
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
	turn := session.Turn{Pin: &entry}
	var plan *session.Plan
	if at != nil {
		plan, err = at.Prepare(ctx, l.env.cfg.Change, turn, opts, session.PendingToolSkip)
	} else {
		plan, err = l.env.store.Prepare(ctx, start, l.env.cfg.Change, turn, opts, session.PendingToolSkip)
	}
	if err != nil {
		return err
	}
	sess, err := plan.Commit(ctx)
	announceOldSessions(l.env, plan)
	if sess != nil && sess.Head != start.Message {
		l.from, l.saved = sess, true
	}
	if err != nil {
		return err
	}
	announceCommit(l.env, plan, sess)
	l.showSession(sess)
	l.env.notifier.Infof("Pin %s: %s.", entry.Pin.ID, change.Action)
	return nil
}
