package session

import (
	"context"
	"errors"
	"fmt"
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
	lmerrors "lmtools/internal/errors"
	"os"
	"path/filepath"
	"strings"
)

// View is a request's history as a request carries it: the messages it
// sends, a ref describing each stored one for the context engine, and the
// transcript the engine keeps its checkpoints in.
type View struct {
	Messages   []core.TypedMessage
	Refs       []contextstate.Ref
	Transcript *Transcript
	// Settings are the conversation's settings in force for the request.
	Settings Settings
}

// systemSource names the system message in refs. The prompt comes from the
// settings and the run, so it is no stored message.
const systemSource = "system"

// view builds the view of history h under settings for a request with
// opts. The system message is the settings' prompt; the system records in
// the history are not sent. live is the session the reply goes to, or nil
// when the request's writes have not happened yet.
func (s *Store) view(h History, identity string, settings Settings, opts core.RequestOptions, live *Session) View {
	v := View{Settings: settings}
	t := &Transcript{store: s, session: h.Session, live: live}
	if text := SystemPrompt(settings, opts); text != "" {
		v.Messages = append(v.Messages, core.NewTextMessage(string(core.RoleSystem), text))
		v.Refs = append(v.Refs, contextstate.Ref{Source: systemSource, Role: core.RoleSystem})
		t.ids = append(t.ids, "")
		t.lead = 1
	}
	typed := h.Typed()
	for i, m := range h.Messages {
		if m.Role == core.RoleSystem {
			continue
		}
		v.Messages = append(v.Messages, typed[i])
		v.Refs = append(v.Refs, contextstate.Ref{
			Source: identity + "/" + m.ID, At: m.Time, Role: m.Role,
			Receipt: t.fromStored(m.Receipt), Pin: m.Pin, Incomplete: m.Incomplete,
		})
		t.ids = append(t.ids, m.ID)
	}
	if n := len(h.Messages); n > 0 {
		t.head = h.Messages[n-1].ID
	}
	t.refs = v.Refs
	v.Transcript = t
	return v
}

// awaitsAnswer reports whether a request at the end of h, sent without a new
// prompt, has something for the model to answer: the newest message that is
// not a system record, a pin event or a developer message is a question or
// tool results.
func awaitsAnswer(h History) bool {
	for i := len(h.Messages) - 1; i >= 0; i-- {
		m := h.Messages[i]
		switch {
		case m.Role == core.RoleSystem || m.Role == core.RoleDeveloper || m.Pin != nil:
			continue
		case m.Role == core.RoleUser:
			return true
		default:
			return false
		}
	}
	return false
}

// Transcript is the context engine's view of a request's history in the
// store. A checkpoint at a boundary is saved beside the message the boundary
// follows, in .context/<message>/<tag>.json, and is keyed by that message,
// whose history never changes.
//
// The engine counts a boundary over the messages a request sends, the
// system prompt among them, while checkpoints and receipts are stored with
// the count of stored messages they cover. A request that sends no system
// prompt therefore finds the checkpoints of one that sends it.
type Transcript struct {
	store   *Store
	session string
	refs    []contextstate.Ref
	// ids holds the message ID of each ref, empty for the system prompt.
	ids []string
	// lead is the number of refs before the stored messages: one when the
	// request sends a system prompt.
	lead int
	// head is the message the request continues.
	head string
	// live is the session the reply goes to. Until there is one, the
	// transcript keeps the receipt for the session the request's commit
	// makes.
	live    *Session
	receipt *contextstate.Receipt
}

// fromStored returns a stored receipt with its count in the request's terms.
func (t *Transcript) fromStored(r *contextstate.Receipt) *contextstate.Receipt {
	if r == nil || r.Count == 0 {
		return r
	}
	shifted := *r
	shifted.Count += t.lead
	return &shifted
}

// Stored reports whether the history's first end messages are stored.
func (t *Transcript) Stored(end int) bool {
	return end > 0 && end <= len(t.ids) && t.ids[end-1] != ""
}

// Key identifies the history through boundary end by the message it ends
// with.
func (t *Transcript) Key(end int) string {
	return t.refs[end-1].Source
}

func (t *Transcript) checkpointPath(end int, tag string) (string, error) {
	if !t.Stored(end) {
		return "", fmt.Errorf("no stored message ends boundary %d", end)
	}
	if tag == "" || len(tag) > 64 || strings.Trim(tag, "0123456789abcdef") != "" {
		return "", fmt.Errorf("invalid checkpoint tag %q", tag)
	}
	return filepath.Join(t.store.sessionDir(t.session), contextDirectory, t.ids[end-1], tag+".json"), nil
}

// Checkpoint loads the checkpoint saved under tag at boundary end.
func (t *Transcript) Checkpoint(_ context.Context, end int, tag string) (contextstate.Checkpoint, bool, error) {
	var cp contextstate.Checkpoint
	path, err := t.checkpointPath(end, tag)
	if err != nil {
		return cp, false, err
	}
	ok, err := readJSON(path, &cp)
	if err != nil || !ok {
		return cp, false, err
	}
	if cp.Count <= 0 || cp.Prefix == "" || strings.TrimSpace(cp.Handoff) == "" {
		return cp, false, errors.New("invalid saved context checkpoint")
	}
	cp.Tag = tag
	cp.Count += t.lead
	return cp, true, nil
}

// Publish saves cp under tag at boundary end, holding the session's lock,
// unless a checkpoint for the same history is saved there already: the first
// publication wins, and Publish returns it. The message the boundary follows
// must still exist.
func (t *Transcript) Publish(ctx context.Context, end int, tag string, cp contextstate.Checkpoint) (contextstate.Checkpoint, error) {
	path, err := t.checkpointPath(end, tag)
	if err != nil {
		return cp, err
	}
	cp.Tag = tag
	stored := cp
	stored.Count -= t.lead
	err = t.store.withSessionLock(ctx, t.session, func() error {
		if _, err := t.store.Message(Ref{Session: t.session, Message: t.ids[end-1]}); err != nil {
			return err
		}
		var prev contextstate.Checkpoint
		ok, err := readJSON(path, &prev)
		if err != nil {
			return err
		}
		if ok && prev.Prefix == stored.Prefix && prev.Count == stored.Count {
			stored = prev
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return lmerrors.WrapError("create checkpoint directory", err)
		}
		return writeJSON(path, stored)
	})
	stored.Tag = tag
	stored.Count += t.lead
	return stored, err
}

// Current reports an error when the message the request continues no longer
// exists.
func (t *Transcript) Current(context.Context) error {
	if t.head == "" {
		return nil
	}
	_, err := t.store.Message(Ref{Session: t.session, Message: t.head})
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("message %s/%s was deleted, so its history is no longer available", t.session, t.head)
	}
	return err
}

// Used records the receipt of the window a request is sent with, which the
// reply saved next carries.
func (t *Transcript) Used(receipt contextstate.Receipt) {
	if receipt.Count > 0 {
		receipt.Count -= t.lead
	}
	if t.live != nil {
		t.live.receipt = &receipt
		return
	}
	t.receipt = &receipt
}
