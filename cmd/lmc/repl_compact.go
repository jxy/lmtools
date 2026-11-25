package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
	"lmtools/internal/session"
	"lmtools/internal/workingcontext"
	"slices"
	"strings"
)

// runCompact returns an error only if the REPL must stop, just as it would
// for a standalone handoff whose answer could not be saved.
func (l *replLoop) runCompact() error {
	// Ctrl-C applies to the whole command, including the transition from
	// a saved handoff to the checkpoint writer.
	ctx, end := l.commandContext()
	defer end()
	if l.from == nil || l.from.Head == "" {
		l.env.notifier.Infof("No completed exchange to compact.")
		return nil
	}
	if l.base.Context.Notebook != "" && l.compactHandoff != l.from.Ref() {
		finished, stop := l.handoffBeforeCompact(ctx)
		if !finished || stop != nil {
			return stop
		}
	}
	if err := l.compact(ctx); err != nil {
		l.compactError(err)
	}
	return nil
}

func (l *replLoop) compactError(err error) {
	l.env.notifier.Errorf("/compact: %v", err)
	l.failedTurns++
}

func (l *replLoop) compactView() (*workingcontext.Engine, session.View, error) {
	opts := l.base
	opts.Developer, opts.Images = "", nil
	view, err := l.from.ViewWithChange(opts, l.env.cfg.Change)
	if err != nil {
		return nil, view, err
	}
	return newContextWindow(l.env, opts), view, nil
}

func (l *replLoop) handoffBeforeCompact(ctx context.Context) (bool, error) {
	engine, view, err := l.compactView()
	if err != nil {
		l.compactError(err)
		return false, nil
	}
	// Check before sending a maintenance turn: it would otherwise create
	// a new completed exchange on every /compact, even immediately after a cut.
	boundary, err := engine.CompactBoundary(ctx, view.Messages, view.Refs, view.Transcript)
	if err != nil {
		l.compactError(err)
		return false, nil
	}
	if boundary == 0 {
		l.env.notifier.Infof("No new completed exchange to compact.")
		return false, nil
	}
	action := l.handoffAction("")
	if !action.send {
		return false, nil
	}
	unfinished := slices.ContainsFunc(view.Refs[boundary:], func(ref contextstate.Ref) bool {
		return ref.Pin == nil && (ref.Role == core.RoleUser || ref.Role == core.RoleAssistant)
	})
	before, saved, awaiting := l.from, l.saved, l.awaiting
	l.env.notifier.Infof("Updating the project notebook before compacting...")
	finished, stop := l.submitTurn(ctx, session.Turn{Prompt: action.text})
	handoff := l.from
	// A handoff is a real saved turn. If the operator was midway through
	// another exchange, keep that cursor so the checkpoint and the next
	// answer follow their unfinished work, not the maintenance reply.
	if unfinished {
		l.from, l.saved, l.awaiting = before, saved, awaiting
		if handoff.Head != before.Head {
			l.env.notifier.Infof("Notebook handoff at %s. Continuing the unfinished branch at %s.", handoff.Ref(), before.Ref())
		}
	}
	if !finished || stop != nil {
		if stop == nil {
			l.env.notifier.Infof("Notebook handoff stopped before compaction.")
		}
		return false, stop
	}
	if err := notebookHandoffError(handoff); err != nil {
		l.compactError(err)
		if !unfinished {
			l.awaitAgain()
		}
		return false, nil
	}
	l.compactHandoff = l.from.Ref()
	return true, nil
}

// Check the last put for each note, so a successful retry resolves a failed
// write and a write to another note does not hide it. A final report can
// confirm that the notes are already current without any writes.
func notebookHandoffError(handoff *session.Session) error {
	h, err := handoff.History()
	if err != nil {
		return err
	}
	written := make(map[string]bool)
	last := len(h.Messages) - 1
	for i := last; i > 0; i-- {
		batch := h.Contents[i].Tools
		if batch == nil || len(batch.Results) == 0 {
			if h.Messages[i].Role == core.RoleUser && h.Messages[i].Pin == nil {
				break
			}
			continue
		}
		calls := h.Contents[i-1].Tools
		if calls == nil || len(calls.Calls) != len(batch.Results) {
			continue
		}
		// Calls in one batch can run concurrently. Only a later round
		// can retry a put after seeing its error.
		var successes []string
		for j, result := range batch.Results {
			call := calls.Calls[j]
			if call.MCPServer != "notebook" || call.MCPTool != "put" || call.ID != result.ID {
				continue
			}
			var note struct{ ID string }
			_ = json.Unmarshal(call.Args, &note)
			if written[note.ID] {
				continue
			}
			if result.Error != "" {
				return fmt.Errorf("the notebook handoff could not write note %q. Resolve the tool error and retry /compact", note.ID)
			}
			successes = append(successes, note.ID)
		}
		for _, id := range successes {
			written[id] = true
		}
	}
	// A provider can finish with empty content, leaving the handoff prompt
	// or its tool results awaiting an answer in the session.
	if last >= 0 && h.Messages[last].Role == core.RoleAssistant {
		for _, block := range h.Contents[last].Blocks {
			if text, ok := block.(core.TextBlock); ok && strings.TrimSpace(text.Text) != "" {
				return nil
			}
		}
	}
	return errors.New("the notebook handoff returned no final answer")
}

func (l *replLoop) compact(ctx context.Context) error {
	engine, view, err := l.compactView()
	if err != nil {
		return err
	}
	result, err := engine.Compact(ctx, view.Messages, view.Refs, view.Transcript)
	if err != nil {
		return err
	}
	if result.Checkpoint == nil {
		if result.Before == 0 {
			l.env.notifier.Infof("No new completed exchange to compact.")
		} else {
			l.env.notifier.Infof("Kept the current context: the summary would use %d estimated input tokens (currently %d).", result.After, result.Before)
		}
		return nil
	}

	settings := view.Settings
	settings.ManualCheckpoint = result.Checkpoint
	_, err = l.from.Append(ctx, session.Entry{Role: core.RoleSystem, Settings: &settings})
	if err != nil {
		return err
	}
	// This record also committed any startup settings. In particular an
	// initial -context-policy full must not clear this cut on the next turn.
	l.env.cfg.Change = session.Change{}
	l.env.cfg.Settings = settings
	l.env.cfg.Context.ManualCheckpoint = result.Checkpoint
	l.base.Context.ManualCheckpoint = result.Checkpoint
	l.saved = true
	l.showSession(l.from)
	l.env.notifier.Infof("Context compacted from %d to %d estimated input tokens. Saved at %s. Continue with your next prompt.", result.Before, result.After, l.from.Ref())
	return nil
}
