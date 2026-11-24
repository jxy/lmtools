package session

import (
	"context"
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
	"time"
)

// Session is where a run writes in a stored conversation: Head is the
// message its next write follows, empty for the session root, and every
// write moves Head to the message it saved. Another run that writes after
// the same message makes a separate branch, so nothing a run writes depends
// on what other runs do.
type Session struct {
	Store    *Store
	Number   string
	Identity string
	Head     string
	// receipt is the window the next reply was produced with.
	receipt *contextstate.Receipt
}

// Open returns the session at ref, which names a message or, for a session
// without messages, the session.
func (s *Store) Session(ref Ref) (*Session, error) {
	located, err := s.Locate(ref)
	if err != nil {
		return nil, err
	}
	info, err := s.Info(located.Session)
	if err != nil {
		return nil, err
	}
	if located.Message != "" {
		if _, err := s.Message(located); err != nil {
			return nil, err
		}
	}
	return &Session{Store: s, Number: located.Session, Identity: info.Identity, Head: located.Message}, nil
}

// Ref names the session's head.
func (s *Session) Ref() Ref {
	return Ref{Session: s.Number, Message: s.Head}
}

// Append saves entries as a chain after the head and moves the head to the
// last one saved, also when a later one fails.
func (s *Session) Append(ctx context.Context, entries ...Entry) ([]Message, error) {
	committed, err := s.Store.Append(ctx, s.Ref(), entries...)
	if n := len(committed); n > 0 {
		s.Head = committed[n-1].ID
	}
	return committed, err
}

// History returns the lineage of the head with its contents.
func (s *Session) History() (History, error) {
	return s.Store.History(s.Ref())
}

// View returns the history at the head as a request with opts carries it,
// under the settings in force there.
func (s *Session) View(opts core.RequestOptions) (View, error) {
	h, err := s.History()
	if err != nil {
		return View{}, err
	}
	return s.Store.view(h, s.Identity, SettingsAt(h.Messages), opts, s), nil
}

// UseReceipt makes receipt the window the next reply records.
func (s *Session) UseReceipt(receipt *contextstate.Receipt) {
	s.receipt = receipt
}

// SaveReply saves a model's reply after the head, with the receipt of the
// window the request was sent with.
func (s *Session) SaveReply(ctx context.Context, response core.Response, model string) (Message, error) {
	entry := Entry{
		Role: core.RoleAssistant, Model: model, Receipt: s.receipt,
		Incomplete: response.IncompleteReason != "" || response.CompletionError != nil,
		Content:    Content{Blocks: core.ReplyBlocks(response)},
	}
	if len(response.ToolCalls) > 0 {
		entry.Content.Tools = &core.ToolInteraction{Calls: response.ToolCalls}
	}
	return s.appendOne(ctx, entry)
}

// SaveToolResults saves the results of a round of tool calls, with a note
// the host adds, after the head. A result saved without its tool's name
// takes it from the call when the history is read.
func (s *Session) SaveToolResults(ctx context.Context, results []core.ToolResult, note string) (Message, error) {
	return s.appendOne(ctx, Entry{
		Role: core.RoleUser,
		Content: Content{
			Blocks: core.ToolResultsMessageBlocks(results, note, nil),
			Tools:  &core.ToolInteraction{Results: results},
		},
	})
}

func (s *Session) appendOne(ctx context.Context, entry Entry) (Message, error) {
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}
	committed, err := s.Append(ctx, entry)
	if err != nil || len(committed) == 0 {
		return Message{}, err
	}
	return committed[0], nil
}

// ToolStore adapts the session to the store the tool loop saves through.
func (s *Session) ToolStore() core.SessionStore {
	return toolStore{s}
}

type toolStore struct{ s *Session }

func (t toolStore) SaveAssistant(ctx context.Context, text string, calls []core.ToolCall, model string) (string, string, error) {
	return t.SaveAssistantResponse(ctx, core.Response{Text: text, ToolCalls: calls}, model)
}

func (t toolStore) SaveAssistantWithThoughtSignature(ctx context.Context, text string, calls []core.ToolCall, model, signature string) (string, string, error) {
	return t.SaveAssistantResponse(ctx, core.Response{Text: text, ToolCalls: calls, ThoughtSignature: signature}, model)
}

func (t toolStore) SaveAssistantResponse(ctx context.Context, response core.Response, model string) (string, string, error) {
	m, err := t.s.SaveReply(ctx, response, model)
	return t.GetPath(), m.ID, err
}

func (t toolStore) SaveToolResults(ctx context.Context, results []core.ToolResult, note string) (string, string, error) {
	m, err := t.s.SaveToolResults(ctx, results, note)
	return t.GetPath(), m.ID, err
}

func (t toolStore) GetPath() string {
	return t.s.Ref().String()
}
