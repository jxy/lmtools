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
// model to answer the end of the history.
type Turn struct {
	Prompt    string
	Images    []core.ImageBlock
	Developer string
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

	store     *Store
	start     Ref
	identity  string
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
// start point, and is refused when the history ends with an answer.
func (s *Store) Prepare(ctx context.Context, start Ref, change Change, turn Turn, opts core.RequestOptions, mode PendingToolMode) (*Plan, error) {
	if turn.Developer != "" && !turn.hasPrompt() {
		return nil, errors.New("-developer needs a prompt to accompany")
	}
	p := &Plan{store: s}
	var h History
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
		p.start, p.identity = located, info.Identity
	} else if start.Message != "" {
		return nil, fmt.Errorf("message %s names no session", start.Message)
	}
	if err := validateDeveloperHistory(opts, h); err != nil {
		return nil, err
	}

	settings, changed := SettingsAt(h.Messages).Apply(change)
	p.Changed = changed
	if start.Session == "" || len(changed) > 0 {
		recorded := settings
		p.entries = append(p.entries, Entry{Role: core.RoleSystem, Settings: &recorded})
	}
	p.View = s.view(h, p.identity, settings, opts, nil)

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
			Role:    core.RoleDeveloper,
			Content: Content{Blocks: []core.Block{core.TextBlock{Text: turn.Developer}}},
		})
	}
	if turn.hasPrompt() {
		blocks := core.UserMessageBlocks(turn.Prompt, turn.Images)
		p.Messages = append(p.Messages, core.TypedMessage{Role: string(core.RoleUser), Blocks: blocks})
		p.entries = append(p.entries, Entry{Role: core.RoleUser, Content: Content{Blocks: blocks}})
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
// batch that stopped part way says where and how to retry.
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
	}
	now := time.Now().UTC()
	for i := range p.entries {
		p.entries[i].Time = now
	}
	committed, err := sess.Append(ctx, p.entries...)
	p.Transcript.live = sess
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
	return fmt.Sprintf("%v; saved through %s: %s", e.Err, e.Saved, e.Next)
}

func (e *SaveError) Unwrap() error {
	return e.Err
}
