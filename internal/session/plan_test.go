package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
	"lmtools/internal/mcp"
	"lmtools/internal/prompts"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// lines writes each message as role:text, the text of its text blocks.
func lines(messages []core.TypedMessage) []string {
	var out []string
	for i, text := range textsOf(messages) {
		out = append(out, messages[i].Role+":"+text)
	}
	return out
}

// plainOpts are a run's options on a wire that carries developer messages.
func plainOpts() core.RequestOptions {
	opts := core.NewTestRequestConfig()
	opts.Provider = constants.ProviderOpenAI
	return opts
}

func prepare(t *testing.T, s *Store, start Ref, change Change, turn Turn) *Plan {
	t.Helper()
	p, err := s.Prepare(context.Background(), start, change, turn, plainOpts(), PendingToolSkip)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func commit(t *testing.T, p *Plan) *Session {
	t.Helper()
	sess, err := p.Commit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func reply(t *testing.T, sess *Session, answer string) Message {
	t.Helper()
	m, err := sess.SaveReply(context.Background(), core.Response{Text: answer}, "model")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func custom(prompt string) *string { return &prompt }

// conversation starts a session with prompt "q1" under -s "sys" and answers
// it, and returns the session at the answer.
func conversation(t *testing.T, s *Store) *Session {
	t.Helper()
	sess := commit(t, prepare(t, s, Ref{}, Change{System: custom("sys")}, Turn{Prompt: "q1"}))
	reply(t, sess, "a1")
	return sess
}

// roles lists the role of each message of a session's lineage at ref.
func roles(t *testing.T, s *Store, at Ref) []string {
	t.Helper()
	lineage, err := s.Lineage(at)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range lineage {
		out = append(out, string(m.Role))
	}
	return out
}

// A request carries the history of its start point under the settings in
// force there, then the run's developer instruction and prompt; the commit
// writes a settings record only for a new session or a change.
func TestPrepareCarriesTheStartPointsHistory(t *testing.T) {
	s := newStore(t)
	first := prepare(t, s, Ref{}, Change{System: custom("sys")}, Turn{Prompt: "q1"})
	if got := lines(first.Messages); !reflect.DeepEqual(got, []string{"system:sys", "user:q1"}) {
		t.Fatalf("first request %q", got)
	}
	sess := commit(t, first)
	answer := reply(t, sess, "a1")
	if got := roles(t, s, sess.Ref()); !reflect.DeepEqual(got, []string{"system", "user", "assistant"}) {
		t.Fatalf("session %q", got)
	}
	at := sess.Ref()

	next := prepare(t, s, at, Change{}, Turn{Prompt: "q2", Developer: "be brief"})
	if got := lines(next.Messages); !reflect.DeepEqual(got, []string{"system:sys", "user:q1", "assistant:a1", "developer:be brief", "user:q2"}) {
		t.Fatalf("next request %q", got)
	}
	if len(next.Changed) != 0 || len(next.Refs) != 3 {
		t.Fatalf("changed %q, %d refs", next.Changed, len(next.Refs))
	}
	if after := commit(t, next); !reflect.DeepEqual(roles(t, s, after.Ref()), []string{"system", "user", "assistant", "developer", "user"}) {
		t.Fatalf("after the commit %q", roles(t, s, after.Ref()))
	}

	changed := prepare(t, s, at, Change{System: custom("new sys")}, Turn{Prompt: "q3"})
	if got := lines(changed.Messages); !reflect.DeepEqual(got, []string{"system:new sys", "user:q1", "assistant:a1", "user:q3"}) || !reflect.DeepEqual(changed.Changed, []string{"system prompt"}) {
		t.Fatalf("changed request %q, changed %q", got, changed.Changed)
	}
	after := commit(t, changed)
	lineage, err := s.Lineage(after.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if record := lineage[len(lineage)-2]; record.Settings == nil || *record.Settings.System != "new sys" || lineage[len(lineage)-2].Parent != answer.ID {
		t.Fatalf("the change was recorded as %+v", record)
	}
}

// Editing a question resumes at its parent. With a developer instruction
// right before the question, that instruction is the parent and stays, and
// so do an earlier unanswered question and a pin event.
func TestEditingAQuestionKeepsWhatCameBefore(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := commit(t, prepare(t, s, Ref{}, Change{System: custom("sys")}, Turn{Prompt: "q0"}))
	pin, err := PinEntry(contextstate.PinChange{Action: "add"}, "use meters", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Append(ctx, pin); err != nil {
		t.Fatal(err)
	}
	asked := commit(t, prepare(t, s, sess.Ref(), Change{}, Turn{Prompt: "q1", Developer: "cite sources"}))
	reply(t, asked, "a1")
	lineage, err := s.Lineage(asked.Ref())
	if err != nil {
		t.Fatal(err)
	}
	instruction := lineage[len(lineage)-3]
	if instruction.Role != core.RoleDeveloper {
		t.Fatalf("the question follows %s", instruction.Role)
	}
	edit := prepare(t, s, sess.Ref().At(instruction.ID), Change{}, Turn{Prompt: "edited q1"})
	got := lines(edit.Messages)
	want := []string{"system:sys", "user:q0", "user:Standing user instruction " + pin.Pin.ID + " (this branch until withdrawn):\nuse meters", "developer:cite sources", "user:edited q1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("edit sends\n%q\nwant\n%q", got, want)
	}
}

// A run without a prompt answers its start point when the history ends with
// a question or tool results, though the question has answers already. It
// is refused after a completed answer, a settings record or a pin event.
func TestRunWithoutAPromptAnswersTheStartPoint(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := conversation(t, s)
	lineage, err := s.Lineage(sess.Ref())
	if err != nil {
		t.Fatal(err)
	}
	question := sess.Ref().At(lineage[1].ID)
	again := prepare(t, s, question, Change{}, Turn{})
	if got := lines(again.Messages); !reflect.DeepEqual(got, []string{"system:sys", "user:q1"}) {
		t.Fatalf("answering again sends %q", got)
	}
	alt := commit(t, again)
	if second := reply(t, alt, "a1 again"); second.Parent != question.Message {
		t.Fatalf("the second answer follows %s", second.Parent)
	}

	refused := func(at Ref) {
		t.Helper()
		if _, err := s.Prepare(ctx, at, Change{}, Turn{}, plainOpts(), PendingToolSkip); err == nil || !strings.Contains(err.Error(), "nothing to answer") {
			t.Fatalf("a run without a prompt at %s: %v", at, err)
		}
	}
	refused(sess.Ref())
	if _, err := s.Prepare(ctx, question, Change{}, Turn{Developer: "be brief"}, plainOpts(), PendingToolSkip); err == nil || !strings.Contains(err.Error(), "needs a prompt") {
		t.Fatalf("a developer instruction without a prompt: %v", err)
	}
	settings := Settings{Policy: "full"}
	record, err := s.Append(ctx, sess.Ref(), Entry{Role: core.RoleSystem, Settings: &settings})
	if err != nil {
		t.Fatal(err)
	}
	refused(sess.Ref().At(record[0].ID))
	pin, err := PinEntry(contextstate.PinChange{Action: "add"}, "use meters", nil)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := s.Append(ctx, sess.Ref(), pin)
	if err != nil {
		t.Fatal(err)
	}
	refused(sess.Ref().At(pinned[0].ID))
}

// A turn whose commit stopped after its settings record and developer
// instruction is retried from where it stopped, with the same flags, and
// saves no settings or instruction twice.
func TestRetryFromAPartlyCommittedTurn(t *testing.T) {
	s := newStore(t)
	sess := conversation(t, s)
	change, turn := Change{Policy: custom("full")}, Turn{Prompt: "q2", Developer: "be brief"}
	writeForTest = func(m Message) error {
		if m.Role == core.RoleUser {
			return errors.New("disk full")
		}
		return nil
	}
	t.Cleanup(func() { writeForTest = nil })
	stopped, err := prepare(t, s, sess.Ref(), change, turn).Commit(context.Background())
	var saveErr *SaveError
	if !errors.As(err, &saveErr) || saveErr.Saved != stopped.Ref() || !strings.Contains(err.Error(), "resume there with the same prompt") {
		t.Fatalf("stopped at %v: %v", stopped.Ref(), err)
	}
	writeForTest = nil
	retry := prepare(t, s, saveErr.Saved, change, turn)
	if len(retry.Changed) != 0 || len(retry.entries) != 1 {
		t.Fatalf("the retry changes %q and writes %d messages", retry.Changed, len(retry.entries))
	}
	done := commit(t, retry)
	if got := roles(t, s, done.Ref()); !reflect.DeepEqual(got, []string{"system", "user", "assistant", "system", "developer", "user"}) {
		t.Fatalf("lineage %q", got)
	}
}

// Settings are read at the start point: a branch from before a change keeps
// the earlier settings, so a notebook attached later does not reach it.
// Each setting can be cleared.
func TestSettingsFollowTheLineage(t *testing.T) {
	s := newStore(t)
	sess := conversation(t, s)
	before := sess.Ref()
	notebook := commit(t, prepare(t, s, before, Change{Notebook: custom("/notes"), View: custom("full"), Catalog: custom("CATALOG"), Window: intp(8192)}, Turn{Prompt: "q2"}))
	if p := prepare(t, s, notebook.Ref(), Change{}, Turn{Prompt: "q3"}); p.Settings.Notebook != "/notes" || p.Settings.Catalog != "CATALOG" {
		t.Fatalf("after the change: %+v", p.Settings)
	}
	if p := prepare(t, s, before, Change{}, Turn{Prompt: "q2 again"}); p.Settings.Notebook != "" || *p.Settings.System != "sys" {
		t.Fatalf("a branch from before the change: %+v", p.Settings)
	}
	cleared, changed := prepare(t, s, notebook.Ref(), Change{}, Turn{Prompt: "x"}).Settings.Apply(Change{Builtin: true, Notebook: custom(""), Window: intp(0)})
	if cleared.System != nil || cleared.Notebook != "" || cleared.View != "" || cleared.Catalog != "" || cleared.Window != 0 {
		t.Fatalf("cleared settings %+v", cleared)
	}
	if !reflect.DeepEqual(changed, []string{"system prompt", "notebook", "notebook view", "context window cap"}) {
		t.Fatalf("changed %q", changed)
	}
	if same, changed := cleared.Apply(Change{Catalog: custom("ANOTHER")}); len(changed) != 0 || same.Catalog != "" {
		t.Fatalf("a catalog alone changed %q", changed)
	}
	kept := Settings{Notebook: "/notes", Catalog: "CATALOG"}
	if same, changed := kept.Apply(Change{Notebook: custom("/notes"), Catalog: custom("NEWER")}); len(changed) != 0 || same.Catalog != "CATALOG" {
		t.Fatalf("an unchanged notebook took a new catalog %q: %q", same.Catalog, changed)
	}
}

func intp(n int) *int { return &n }

// The built-in prompt is built from the tools a run advertises; a custom
// prompt stays, and an empty one sends no system message.
func TestSystemPromptFollowsTheRunsTools(t *testing.T) {
	tools := plainOpts()
	tools.ToolEnabled = true
	if got := SystemPrompt(Settings{}, plainOpts()); got != prompts.DefaultSystemPrompt {
		t.Fatalf("without tools %q", got)
	}
	if got := SystemPrompt(Settings{}, tools); got != core.ToolSystemPromptFor(nil) {
		t.Fatalf("with tools %q", got)
	}
	if got := SystemPrompt(Settings{System: custom("mine")}, tools); got != "mine" {
		t.Fatalf("custom %q", got)
	}
	withServer := tools
	withServer.MCP = oneMCPTool{}
	if got := SystemPrompt(Settings{System: custom("mine")}, withServer); !strings.HasPrefix(got, "mine") || !strings.Contains(got, "MCP servers") {
		t.Fatalf("a custom prompt without what the servers add: %q", got)
	}
	if got := SystemPrompt(Settings{System: custom("")}, withServer); got != core.MCPSystemPromptAddendum(withServer.MCP) {
		t.Fatalf("an empty prompt with a server sends %q", got)
	}
	s := newStore(t)
	p := prepare(t, s, Ref{}, Change{System: custom("")}, Turn{Prompt: "q"})
	if got := lines(p.Messages); !reflect.DeepEqual(got, []string{"user:q"}) {
		t.Fatalf("an empty prompt sends %q", got)
	}
}

// A session whose first commit failed is empty, and the first commit that
// succeeds there records the settings before its message, as a new
// session's does.
func TestAnEmptySessionGetsItsSettingsRecord(t *testing.T) {
	s := newStore(t)
	empty := create(t, s)
	sess := commit(t, prepare(t, s, Ref{Session: empty.Number}, Change{}, Turn{Prompt: "q"}))
	if got := strings.Join(roles(t, s, sess.Ref()), ","); got != "system,user" {
		t.Fatalf("the first commit wrote %s", got)
	}
}

// oneMCPTool is a connected server with one tool.
type oneMCPTool struct{}

func (oneMCPTool) Tools() []mcp.QualifiedTool {
	return []mcp.QualifiedTool{{Name: "mcp__files__read", Server: "files", Tool: mcp.Tool{Name: "read"}}}
}
func (oneMCPTool) Instructions() []mcp.ServerInstructions { return nil }
func (oneMCPTool) Call(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
	return nil, nil
}

// Pending calls at the start point are previewed with placeholders, or must
// be resolved before a run's request is prepared.
func TestPendingCallsAtTheStartPoint(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	call := core.ToolCall{ID: "call_1", Name: "universal_command", Args: json.RawMessage(`{"command":["true"]}`), InvocationID: core.NewInvocationID()}
	sess := pendingCallSession(t, s, []core.ToolCall{call})
	opts := pendingToolTestConfig()
	if _, err := s.Prepare(ctx, sess.Ref(), Change{}, Turn{}, opts, PendingToolExecute); err == nil || !strings.Contains(err.Error(), "must be resolved") {
		t.Fatalf("execute mode: %v", err)
	}
	preview, err := s.Prepare(ctx, sess.Ref(), Change{}, Turn{}, opts, PendingToolPreview)
	if err != nil || !preview.Pending {
		t.Fatalf("preview %+v: %v", preview, err)
	}
	last := preview.Messages[len(preview.Messages)-1]
	if result, ok := last.Blocks[0].(core.ToolResultBlock); !ok || !strings.Contains(result.Content, "placeholder") || result.Name != call.Name {
		t.Fatalf("the preview ends with %+v", last)
	}
	if _, err := s.Prepare(ctx, sess.Ref(), Change{}, Turn{}, plainOpts(), PendingToolPreview); err == nil {
		t.Fatal("previewing pending calls without -tool")
	}
}

// The messages a commit saves are dated when the plan was made, before the
// provider was asked.
func TestCommitDatesTheTurnWhenItWasPrepared(t *testing.T) {
	s := newStore(t)
	sess := conversation(t, s)
	p := prepare(t, s, sess.Ref(), Change{System: custom("other")}, Turn{Developer: "be brief", Prompt: "q2"})
	prepared := time.Now().UTC()
	time.Sleep(20 * time.Millisecond)
	next := commit(t, p)
	lineage, err := s.Lineage(next.Ref())
	if err != nil {
		t.Fatal(err)
	}
	saved := lineage[len(lineage)-3:]
	for _, m := range saved {
		if m.Time.After(prepared) {
			t.Errorf("%s message %s is dated %v, after the plan was made at %v", m.Role, m.ID, m.Time, prepared)
		}
	}
	if got := []core.Role{saved[0].Role, saved[1].Role, saved[2].Role}; !reflect.DeepEqual(got, []core.Role{core.RoleSystem, core.RoleDeveloper, core.RoleUser}) {
		t.Fatalf("the commit saved %v", got)
	}
}

// The session a commit returns keeps the history its plan was made from,
// with the messages it saves as the store reads them, so its views and
// plans read no saved message again; once its head is deleted, it reads the
// store and finds the message gone.
func TestSessionKeepsItsHistory(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := conversation(t, s)
	question := Ref{Session: sess.Number, Message: "0002"}
	saved, err := os.ReadFile(s.contentPath(question))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.contentPath(question)); err != nil {
		t.Fatal(err)
	}
	image := core.ImageBlock{URL: "data:image/png;base64,aW1hZ2U=", Name: "shot.png"}
	call := core.ToolCall{ID: "call_1", Name: "universal_command", Args: json.RawMessage(`{"command":["true"]}`)}
	if _, err := sess.SaveReply(ctx, core.Response{Text: "running", ToolCalls: []core.ToolCall{call}}, "model"); err != nil {
		t.Fatal(err)
	}
	results := []core.ToolResult{{ID: "call_1", Output: "done", Images: []core.ImageBlock{image}}}
	if _, err := sess.SaveToolResults(ctx, results, "a note"); err != nil {
		t.Fatal(err)
	}
	reply(t, sess, "a2")
	view, err := sess.View(plainOpts())
	if err != nil {
		t.Fatalf("the session read its history again: %v", err)
	}
	if got := strings.Join(lines(view.Messages), "|"); !strings.HasPrefix(got, "system:sys|user:q1|assistant:a1|assistant:running") || !strings.HasSuffix(got, "assistant:a2") {
		t.Fatalf("view %s", got)
	}
	if _, err := sess.Prepare(ctx, Change{}, Turn{Prompt: "q3"}, plainOpts(), PendingToolSkip); err != nil {
		t.Fatalf("the session prepared from the store: %v", err)
	}
	if err := os.WriteFile(s.contentPath(question), saved, 0o600); err != nil {
		t.Fatal(err)
	}
	kept, err := sess.History()
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.History(sess.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(kept, read) {
		t.Fatalf("the kept history differs from the stored one:\n%+v\n%+v", kept, read)
	}
	if err := s.Delete(ctx, sess.Ref()); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.History(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the history of a deleted head: %v", err)
	}
}

// Copies of a session that write after the same head keep histories of
// their own, whatever room the kept arrays have past their ends, and so
// does a caller that appends to a history a session returned.
func TestSessionCopiesKeepTheirOwnHistories(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := conversation(t, s)
	if _, err := sess.History(); err != nil {
		t.Fatal(err)
	}
	sess.kept.messages = slices.Grow(sess.kept.messages, 4)
	sess.kept.contents = slices.Grow(sess.kept.contents, 4)
	returned, err := sess.History()
	if err != nil {
		t.Fatal(err)
	}
	returned.Messages = append(returned.Messages, Message{ID: "ffff"})
	other := *sess
	if _, err := sess.Append(ctx, text("q0"), text("q1")); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Append(ctx, text("other q0")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		sess *Session
		want string
	}{{sess, "user:q0|user:q1"}, {&other, "user:other q0"}} {
		view, err := c.sess.View(plainOpts())
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(lines(view.Messages), "|"); !strings.HasSuffix(got, "assistant:a1|"+c.want) {
			t.Errorf("history %s, want it to end with %s", got, c.want)
		}
	}
	if id := returned.Messages[len(returned.Messages)-1].ID; id != "ffff" {
		t.Errorf("the caller's history ends with %s", id)
	}
}

// Extending a session's kept history by a message copies none of it while
// no copy of the session has written after the same head.
func TestExtendingAKeptHistoryCopiesNone(t *testing.T) {
	s := newStore(t)
	sess := conversation(t, s)
	h, err := sess.History()
	if err != nil {
		t.Fatal(err)
	}
	long := History{Session: h.Session}
	for i := 0; i < 10000; i++ {
		long.Messages = append(long.Messages, Message{ID: fmt.Sprintf("%04x", i+0x1000), Role: core.RoleUser})
		long.Contents = append(long.Contents, Content{})
	}
	long.Messages = append(long.Messages, h.Messages...)
	long.Contents = append(long.Contents, h.Contents...)
	sess.keep(long)
	extend := func(i int) {
		m := Message{ID: fmt.Sprintf("%04x", i+0x9000), Parent: sess.Head, Role: core.RoleUser, Time: time.Now().UTC()}
		sess.extend([]Message{m}, []Entry{text("more")})
		sess.Head = m.ID
	}
	// The first extension grows the arrays the history was read into.
	extend(0)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 1; i <= 10; i++ {
		extend(i)
	}
	runtime.ReadMemStats(&after)
	if each := (after.TotalAlloc - before.TotalAlloc) / 10; each > 64<<10 {
		t.Fatalf("each extension of a history of %d messages allocated %d bytes", len(long.Messages), each)
	}
	if got := sess.kept.prefix(sess.keptLen); len(got.Messages) != len(long.Messages)+11 || got.Messages[len(got.Messages)-1].ID != sess.Head {
		t.Fatalf("the kept history has %d messages, ending with %s", len(got.Messages), got.Messages[len(got.Messages)-1].ID)
	}
}

// A plan made from a session's kept history commits through it: the
// session the commit returns extends the history in place, copying none of
// it, while no other copy of the session has written after the same head.
func TestCommitExtendsTheKeptHistoryInPlace(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	sess := conversation(t, s)
	h, err := sess.History()
	if err != nil {
		t.Fatal(err)
	}
	long := History{Session: h.Session}
	for i := 0; i < 10000; i++ {
		long.Messages = append(long.Messages, Message{ID: fmt.Sprintf("%04x", i+0x1000), Role: core.RoleUser})
		long.Contents = append(long.Contents, Content{})
	}
	long.Messages = append(long.Messages, h.Messages...)
	long.Contents = append(long.Contents, h.Contents...)
	sess.keep(long)
	// Room past the end, as earlier extensions leave it.
	sess.kept.messages = slices.Grow(sess.kept.messages, 16)
	sess.kept.contents = slices.Grow(sess.kept.contents, 16)
	p, err := sess.Prepare(ctx, Change{}, Turn{Prompt: "q2"}, plainOpts(), PendingToolSkip)
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	next := commit(t, p)
	runtime.ReadMemStats(&after)
	if used := after.TotalAlloc - before.TotalAlloc; used > 256<<10 {
		t.Fatalf("the commit after a history of %d messages allocated %d bytes", len(long.Messages), used)
	}
	if next.kept != sess.kept || next.keptLen != len(long.Messages)+1 {
		t.Fatalf("the committed session keeps %d messages of its own history", next.keptLen)
	}
}

// A reply records the receipt of the window its request used; the plan's
// transcript hands the receipt to the session its commit makes.
func TestReplyRecordsTheRequestsReceipt(t *testing.T) {
	s := newStore(t)
	p := prepare(t, s, Ref{}, Change{}, Turn{Prompt: "q"})
	p.Transcript.Used(contextstate.Receipt{Count: 0, Tag: "ab"})
	sess := commit(t, p)
	m := reply(t, sess, "a")
	if m.Receipt == nil || m.Receipt.Tag != "ab" {
		t.Fatalf("receipt %+v", m.Receipt)
	}
}

// A pin event is added on its own: a turn with a pin and a prompt or an
// instruction is refused, and a pin turn writes the run's settings record,
// for a new session, before the event.
func TestPinTurnsAddTheEventAlone(t *testing.T) {
	s := newStore(t)
	entry, err := PinEntry(contextstate.PinChange{Action: "add"}, "keep this", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range []Turn{{Pin: &entry, Prompt: "q"}, {Pin: &entry, Developer: "d", Prompt: "q"}} {
		if _, err := s.Prepare(context.Background(), Ref{}, Change{}, turn, plainOpts(), PendingToolSkip); err == nil || !strings.Contains(err.Error(), "on its own") {
			t.Fatalf("%+v: %v", turn, err)
		}
	}
	sess := commit(t, prepare(t, s, Ref{}, Change{System: custom("sys")}, Turn{Pin: &entry}))
	if got := strings.Join(roles(t, s, sess.Ref()), ","); got != "system,user" {
		t.Fatalf("a pin turn wrote %s", got)
	}
	if m, err := s.Message(sess.Ref()); err != nil || m.Pin == nil {
		t.Fatalf("the newest message %+v: %v", m, err)
	}
}
