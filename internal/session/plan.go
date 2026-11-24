package session

import (
	"context"
	"errors"
	"fmt"
	"lmtools/internal/core"
	"time"
)

// Turn is what a run adds to a conversation: a prompt with any images, and
// a developer instruction sent with it. A turn without a prompt asks the
// model to answer the end of the history. A turn with a pin event adds that
// event alone and sends nothing.
type Turn struct {
	Prompt    string
	Images    []core.ImageBlock
	Developer string
	Pin       *Entry
}

func (t Turn) hasPrompt() bool {
	return t.Prompt != "" || len(t.Images) > 0
}

// Plan is a prepared request: the view it sends and the writes its commit
// makes once the provider has answered. Nothing is written before Commit.
type Plan struct {
	View
	// Changed names the settings the run changes. The commit records them
	// before its first message.
	Changed []string
	// Pending is set when the start point holds tool calls whose results
	// the request previews with placeholders.
	Pending bool
	// OldSessions, after a commit that created the sessions directory's
	// first session, counts the sessions there in the old format.
	OldSessions int
	// SettingsRecord, after a commit, is the ID of the system record it
	// saved, empty when it saved none.
	SettingsRecord string

	store    *Store
	start    Ref
	identity string
	// history is the start point's history, which the session the commit
	// returns keeps. A plan made from a session's kept history holds that
	// history and the length of the session's part of it instead, so the
	// session the commit returns extends it as the session would.
	history   History
	kept      *keptHistory
	keptLen   int
	entries   []Entry
	committed bool
}

// Start returns where the plan continues: a message, or with no session
// named, a new session.
func (p *Plan) Start() Ref {
	return p.start
}

// Prepare builds the request a run sends after start, writing nothing. A
// start with no session makes a new session, whose first message is a
// system record with the run's settings. Otherwise the request carries the
// history of the start message under the settings in force there, with the
// run's change applied; a change is recorded at the commit. The run's
// developer instruction and prompt follow the history.
//
// A run whose start message is a developer message with the same text as
// its instruction reuses that message. A run without a prompt answers the
// start point, and is refused when the history ends with an answer. The
// messages the commit saves are dated when the plan is made.
func (s *Store) Prepare(ctx context.Context, start Ref, change Change, turn Turn, opts core.RequestOptions, mode PendingToolMode) (*Plan, error) {
	var h History
	var identity string
	if start.Session != "" {
		located, err := s.Locate(start)
		if err != nil {
			return nil, err
		}
		info, err := s.Info(located.Session)
		if err != nil {
			return nil, err
		}
		if h, err = s.History(located); err != nil {
			return nil, err
		}
		start, identity = located, info.Identity
	} else if start.Message != "" {
		return nil, fmt.Errorf("message %s names no session", start.Message)
	}
	return s.prepare(start, identity, h, change, turn, opts, mode)
}

// prepare builds the plan of a run after start, whose history is h, in the
// session identity names.
func (s *Store) prepare(start Ref, identity string, h History, change Change, turn Turn, opts core.RequestOptions, mode PendingToolMode) (*Plan, error) {
	if turn.Developer != "" && !turn.hasPrompt() {
		return nil, errors.New("-developer needs a prompt to accompany")
	}
	if turn.Pin != nil && (turn.hasPrompt() || turn.Developer != "") {
		return nil, errors.New("a pin event is added on its own")
	}
	if err := validateDeveloperHistory(opts, h); err != nil {
		return nil, err
	}
	p := &Plan{store: s, start: start, identity: identity, history: h}
	now := time.Now().UTC()

	settings, changed := SettingsAt(h.Messages).Apply(change)
	p.Changed = changed
	// Each conversation begins with a record of its settings, also one
	// whose first commit failed and left its session empty.
	if len(h.Messages) == 0 || len(changed) > 0 {
		recorded := settings
		p.entries = append(p.entries, Entry{Role: core.RoleSystem, Time: now, Settings: &recorded})
	}
	p.View = s.view(h, p.identity, settings, opts, nil)
	if turn.Pin != nil {
		pin := *turn.Pin
		if pin.Time.IsZero() {
			pin.Time = now
		}
		p.entries = append(p.entries, pin)
		return p, nil
	}

	if calls, _ := pendingCalls(h); len(calls) > 0 && mode != PendingToolSkip {
		if !opts.ToolEnabled {
			return nil, fmt.Errorf("pending tool calls require -tool to continue")
		}
		if mode == PendingToolExecute {
			return nil, fmt.Errorf("%d pending tool call(s) must be resolved before the request is prepared", len(calls))
		}
		results := placeholderToolResults(calls)
		names := make(map[string]string, len(calls))
		for _, call := range calls {
			names[call.ID] = call.Name
		}
		p.Messages = append(p.Messages, core.TypedMessage{
			Role:   string(core.RoleUser),
			Blocks: core.ToolResultsMessageBlocks(results, core.BuildTruncationNotes(results, calls), names),
		})
		p.Pending = true
	}

	if turn.Developer != "" && !reusesDeveloper(h, turn.Developer) {
		p.Messages = append(p.Messages, core.NewTextMessage(string(core.RoleDeveloper), turn.Developer))
		p.entries = append(p.entries, Entry{
			Role: core.RoleDeveloper, Time: now,
			Content: Content{Blocks: []core.Block{core.TextBlock{Text: turn.Developer}}},
		})
	}
	if turn.hasPrompt() {
		blocks := core.UserMessageBlocks(turn.Prompt, turn.Images)
		p.Messages = append(p.Messages, core.TypedMessage{Role: string(core.RoleUser), Blocks: blocks})
		p.entries = append(p.entries, Entry{Role: core.RoleUser, Time: now, Content: Content{Blocks: blocks}})
	} else if !p.Pending && !awaitsAnswer(h) {
		return nil, errors.New("nothing to answer: the conversation ends with an answer, so this run needs a prompt")
	}
	return p, nil
}

// reusesDeveloper reports whether h ends with a developer message saying
// text, which a retry of a run that saved it reuses.
func reusesDeveloper(h History, text string) bool {
	n := len(h.Messages)
	if n == 0 || h.Messages[n-1].Role != core.RoleDeveloper {
		return false
	}
	blocks := h.Contents[n-1].Blocks
	if len(blocks) != 1 {
		return false
	}
	b, ok := blocks[0].(core.TextBlock)
	return ok && b.Text == text
}

// Commit makes the plan's writes: for a new session, the session itself;
// then a system record when the settings changed, the developer instruction
// and the prompt, as one batch after the start point. It returns the session
// the run continues in, at the last message saved, also beside an error; a
// batch that stopped part way says where and how to retry. The session
// keeps the history the plan was made from.
func (p *Plan) Commit(ctx context.Context) (*Session, error) {
	if p.committed {
		return nil, errors.New("request plan already committed")
	}
	p.committed = true
	sess := &Session{Store: p.store, Number: p.start.Session, Identity: p.identity, Head: p.start.Message, receipt: p.Transcript.receipt}
	if sess.Number == "" {
		info, old, err := p.store.Create(ctx)
		if err != nil {
			return nil, err
		}
		sess.Number, sess.Identity, p.OldSessions = info.Number, info.Identity, old
		p.history = History{Session: info.Number}
	}
	if p.kept != nil {
		sess.kept, sess.keptLen = p.kept, p.keptLen
	} else {
		sess.keep(p.history)
	}
	committed, err := sess.Append(ctx, p.entries...)
	p.Transcript.live = sess
	if len(committed) > 0 && p.entries[0].Settings != nil {
		p.SettingsRecord = committed[0].ID
	}
	if err != nil {
		if len(committed) == 0 {
			return sess, err
		}
		return sess, &SaveError{Saved: sess.Ref(), Next: "resume there with the same prompt to retry", Err: err}
	}
	return sess, nil
}

// SaveError reports a write that stopped part way: what it saved last, and
// how a run continues from there.
type SaveError struct {
	Saved Ref
	Next  string
	Err   error
}

func (e *SaveError) Error() string {
	return fmt.Sprintf("%v; saved through %s; %s", e.Err, e.Saved, e.Next)
}

func (e *SaveError) Unwrap() error {
	return e.Err
}
