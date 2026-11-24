package workingcontext

import (
	"context"
	"errors"
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
	"strings"
)

// Transcript is where a request's history is kept, as the engine sees it.
// A boundary is the number of messages of the history a checkpoint covers,
// so boundary end follows the history's first end messages.
//
// The caller binds a transcript when it sets up a turn, from where the
// request's history came: a request built from a saved session uses that
// session's transcript, the first request of a resumed run included, and
// a request held only in memory uses MemoryTranscript.
type Transcript interface {
	// Stored reports whether the history's first end messages are saved,
	// so a checkpoint at that boundary can be published and found again.
	Stored(end int) bool
	// Key identifies the history through boundary end.
	Key(end int) string
	// Checkpoint loads the checkpoint saved under tag at boundary end.
	Checkpoint(ctx context.Context, end int, tag string) (contextstate.Checkpoint, bool, error)
	// Publish saves cp under tag at boundary end unless a checkpoint is
	// saved there already, and returns the one that is.
	Publish(ctx context.Context, end int, tag string, cp contextstate.Checkpoint) (contextstate.Checkpoint, error)
	// Current reports an error when the message the request continues no
	// longer exists, so the history tools serve nothing stale.
	Current(ctx context.Context) error
	// Used records the receipt of the window a request is sent with, which
	// the reply saved next carries.
	Used(contextstate.Receipt)
}

// MemoryTranscript is the transcript of a history held only in memory, as
// in a run without a session. Nothing in it is stored.
func MemoryTranscript(refs []contextstate.Ref) Transcript {
	return memoryTranscript{keys: contextstate.PrefixKeys(refs)}
}

type memoryTranscript struct {
	keys []string
}

func (memoryTranscript) Stored(int) bool      { return false }
func (m memoryTranscript) Key(end int) string { return m.keys[end-1] }
func (memoryTranscript) Checkpoint(context.Context, int, string) (contextstate.Checkpoint, bool, error) {
	return contextstate.Checkpoint{}, false, nil
}

func (memoryTranscript) Publish(_ context.Context, _ int, tag string, cp contextstate.Checkpoint) (contextstate.Checkpoint, error) {
	cp.Tag = tag
	return cp, nil
}
func (memoryTranscript) Current(context.Context) error { return nil }
func (memoryTranscript) Used(contextstate.Receipt)     {}

// transcriptKeys returns the key of each prefix of the first n messages.
func transcriptKeys(t Transcript, n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = t.Key(i + 1)
	}
	return keys
}

// activeCheckpoint follows the newest reply's receipt among the first end
// messages to the checkpoint its window used. Another branch can save a
// checkpoint on a shared ancestor without ever sending it here, so a saved
// checkpoint alone never selects a window.
func activeCheckpoint(ctx context.Context, refs []contextstate.Ref, t Transcript, end int) (contextstate.Checkpoint, error) {
	for i := end - 1; i >= 0; i-- {
		if refs[i].Role != core.RoleAssistant {
			continue
		}
		w := refs[i].Receipt
		if w == nil || w.Count == 0 {
			return contextstate.Checkpoint{}, nil
		}
		if w.Count < 0 || w.Count > i || t.Key(w.Count) != w.Prefix {
			// Replacing the system prompt or selecting a different branch
			// made a new prefix.
			return contextstate.Checkpoint{}, nil
		}
		if len(w.Tag) > 64 || w.Tag == "" || strings.Trim(w.Tag, "0123456789abcdef") != "" {
			return contextstate.Checkpoint{}, errors.New("invalid saved window identity")
		}
		cp, ok, err := t.Checkpoint(ctx, w.Count, w.Tag)
		if err != nil {
			return contextstate.Checkpoint{}, err
		}
		if !ok || cp.Count != w.Count || cp.Prefix != w.Prefix || cp.PinDigest != w.PinDigest || contextstate.PinDigest(cp.Pins) != cp.PinDigest {
			return contextstate.Checkpoint{}, errors.New("saved handoff for the active window is missing or inconsistent")
		}
		return cp, nil
	}
	return contextstate.Checkpoint{}, nil
}
