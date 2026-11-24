package session

import (
	"context"
	"encoding/json"
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
	"slices"
	"sync"
	"time"
)

// Session is where a run writes in a stored conversation: Head is the
// message its next write follows, empty for the session root, and every
// write moves Head to the message it saved. Another run that writes after
// the same message makes a separate branch, so nothing a run writes depends
// on what other runs do.
//
// A session keeps the history of its head once it has read it, and adds
// each message it saves, so a run reads every message once. A Session is
// used by one goroutine at a time.
type Session struct {
	Store    *Store
	Number   string
	Identity string
	Head     string
	// receipt is the window the next reply was produced with.
	receipt *contextstate.Receipt
	// kept, when it is not nil, holds the history of the message its first
	// keptLen messages end with, as the store reads it. Copies of the
	// session share it.
	kept    *keptHistory
	keptLen int
}

// keptHistory is a history that copies of a session share, each owning a
// prefix of it. A copy whose prefix is the whole history extends the arrays
// in place; a copy whose prefix another copy has extended copies its prefix
// first, so the two continue in arrays of their own.
type keptHistory struct {
	mu       sync.Mutex
	session  string
	messages []Message
	contents []Content
}

// prefix returns the first n messages as a history whose slices end at n,
// so appending to them leaves the arrays alone.
func (k *keptHistory) prefix(n int) History {
	k.mu.Lock()
	defer k.mu.Unlock()
	return History{Session: k.session, Messages: k.messages[:n:n], Contents: k.contents[:n:n]}
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
		s.extend(committed, entries)
		s.Head = committed[n-1].ID
	}
	return committed, err
}

// History returns the lineage of the head with its contents. A kept
// history is returned while the head exists: deletion removes a message
// after its descendants, so the whole lineage exists then.
func (s *Session) History() (History, error) {
	if !s.keepsHead() || s.Head != "" && !exists(s.Store.metadataPath(s.Ref())) {
		h, err := s.Store.History(s.Ref())
		if err != nil {
			return History{}, err
		}
		s.keep(h)
	}
	return s.kept.prefix(s.keptLen), nil
}

// keep makes h, the history of the head, the history the session keeps.
func (s *Session) keep(h History) {
	h = h.clipped()
	s.kept = &keptHistory{session: h.Session, messages: h.Messages, contents: h.Contents}
	s.keptLen = len(h.Messages)
}

// keepsHead reports whether the kept history is that of the head.
func (s *Session) keepsHead() bool {
	if s.kept == nil {
		return false
	}
	if s.keptLen == 0 {
		return s.Head == ""
	}
	s.kept.mu.Lock()
	defer s.kept.mu.Unlock()
	return s.kept.messages[s.keptLen-1].ID == s.Head
}

// extend adds the messages an append after the head committed, with the
// content of their entries, to the kept history, each as the store reads it
// back. A message that does not read back drops the kept history, and the
// next read loads it.
func (s *Session) extend(committed []Message, entries []Entry) {
	if !s.keepsHead() {
		return
	}
	messages := make([]Message, len(committed))
	contents := make([]Content, len(committed))
	for i, m := range committed {
		var err error
		if messages[i], contents[i], err = readBack(m, entries[i].Content); err != nil {
			s.kept = nil
			return
		}
	}
	k := s.kept
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.messages) != s.keptLen {
		// Another copy of the session wrote after the same head.
		s.kept = &keptHistory{
			session:  k.session,
			messages: append(slices.Clone(k.messages[:s.keptLen]), messages...),
			contents: append(slices.Clone(k.contents[:s.keptLen]), contents...),
		}
		s.keptLen += len(messages)
		return
	}
	k.messages = append(k.messages, messages...)
	k.contents = append(k.contents, contents...)
	s.keptLen = len(k.messages)
}

// readBack returns message m and its content c as the store reads them from
// the files it writes.
func readBack(m Message, c Content) (Message, Content, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return Message{}, Content{}, err
	}
	var read Message
	if err := json.Unmarshal(data, &read); err != nil {
		return Message{}, Content{}, err
	}
	if data, err = encodeContent(c); err != nil {
		return Message{}, Content{}, err
	}
	content, err := decodeContent(data)
	return read, content, err
}

// Prepare builds the request a run sends after the head, as Store.Prepare
// does, from the history the session keeps.
func (s *Session) Prepare(_ context.Context, change Change, turn Turn, opts core.RequestOptions, mode PendingToolMode) (*Plan, error) {
	h, err := s.History()
	if err != nil {
		return nil, err
	}
	p, err := s.Store.prepare(s.Ref(), s.Identity, h, change, turn, opts, mode)
	if err != nil {
		return nil, err
	}
	p.kept, p.keptLen = s.kept, s.keptLen
	return p, nil
}

// AwaitsAnswer reports whether a run without a prompt has something to send
// at the head: the history ends with a question or tool results, or holds
// tool calls whose results are pending.
func (s *Session) AwaitsAnswer() (bool, error) {
	h, err := s.History()
	if err != nil {
		return false, err
	}
	calls, _ := pendingCalls(h)
	return awaitsAnswer(h) || len(calls) > 0, nil
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
