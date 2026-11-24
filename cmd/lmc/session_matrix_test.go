//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/core"
	"lmtools/internal/notebook"
	"lmtools/internal/prompts"
	"lmtools/internal/session"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// matrix drives the lmc binary against a mock OpenAI provider with its own
// sessions directory. The provider answers the nth conversation request
// with "answer n" and a checkpoint writer with "HANDOFF-TEXT"; a test can
// queue other replies and failures.
type matrix struct {
	t      *testing.T
	bin    string
	dir    string
	key    string
	server *httptest.Server
	store  *session.Store

	mu       sync.Mutex
	bodies   []string
	answers  int
	writers  int
	replies  []func() (map[string]any, int)
	failNext int
}

// matrixBinary copies the lmc binary to a directory beside a notebook
// binary, which notebook runs start.
var (
	matrixOnce sync.Once
	matrixBin  string
	matrixErr  error
)

func newMatrix(t *testing.T) *matrix {
	t.Helper()
	matrixOnce.Do(func() {
		dir, err := os.MkdirTemp("", "lmc-matrix-*") //nolint:usetesting // shared by every matrix test; TestMain removes it
		if err != nil {
			matrixErr = err
			return
		}
		sharedTestDirs = append(sharedTestDirs, dir)
		raw, err := os.ReadFile(getLmcBinary(t))
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, "lmc"), raw, 0o700)
		}
		if err == nil {
			out, buildErr := exec.Command("go", "build", "-o", filepath.Join(dir, "notebook"), "../notebook").CombinedOutput()
			if buildErr != nil {
				err = fmt.Errorf("build notebook: %v %s", buildErr, out)
			}
		}
		matrixBin, matrixErr = filepath.Join(dir, "lmc"), err
	})
	if matrixErr != nil {
		t.Fatal(matrixErr)
	}
	m := &matrix{t: t, bin: matrixBin, dir: filepath.Join(t.TempDir(), "sessions"), key: writeTestAPIKeyFile(t, "mock")}
	m.store = session.Open(m.dir)
	m.server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.server.Close)
	return m
}

func (m *matrix) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
		return
	}
	body, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	defer m.mu.Unlock()
	message := map[string]any{"role": "assistant"}
	finish := "stop"
	switch {
	case strings.Contains(systemOf(string(body)), "Write a handoff"):
		m.writers++
		message["content"] = "HANDOFF-TEXT"
	case m.failNext > 0:
		m.failNext--
		m.bodies = append(m.bodies, string(body))
		http.Error(w, `{"error":{"message":"provider down"}}`, http.StatusInternalServerError)
		return
	case len(m.replies) > 0:
		m.bodies = append(m.bodies, string(body))
		reply, status := m.replies[0]()
		m.replies = m.replies[1:]
		if status != http.StatusOK {
			http.Error(w, `{"error":{"message":"refused"}}`, status)
			return
		}
		message, finish = reply, "tool_calls"
	default:
		m.bodies = append(m.bodies, string(body))
		m.answers++
		message["content"] = fmt.Sprintf("answer %d", m.answers)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "mock", "model": "gpt-test", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}})
}

// run runs lmc with input on stdin and the matrix's provider and sessions.
func (m *matrix) run(input string, flags ...string) (stdout, stderr string, err error) {
	m.t.Helper()
	args := append([]string{"-provider", "openai", "-model", "gpt-test", "-provider-url", m.server.URL + "/v1", "-api-key-file", m.key, "-sessions-dir", m.dir, "-retries", "0"}, flags...)
	return runLmcCommand(m.t, m.bin, args, input)
}

// ok runs lmc and fails the test unless it succeeds, returning where the
// run says it saved.
func (m *matrix) ok(input string, flags ...string) session.Ref {
	m.t.Helper()
	stdout, stderr, err := m.run(input, flags...)
	if err != nil {
		m.t.Fatalf("lmc %v: %v\nstdout: %s\nstderr: %s", flags, err, stdout, stderr)
	}
	return savedAt(m.t, stderr)
}

var savedAtPattern = regexp.MustCompile(`Saved at ([0-9a-f]{4,}/[0-9a-f]{4,})\.`)

// savedAt reads the "Saved at S/M." a run reports.
func savedAt(t *testing.T, stderr string) session.Ref {
	t.Helper()
	match := savedAtPattern.FindStringSubmatch(stderr)
	if match == nil {
		t.Fatalf("no saved position in:\n%s", stderr)
	}
	ref, err := session.ParseRef(match[1])
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

// last returns the body of the newest conversation request.
func (m *matrix) last() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.bodies) == 0 {
		m.t.Fatal("no request was sent")
	}
	return m.bodies[len(m.bodies)-1]
}

func (m *matrix) requests() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.bodies)
}

// parent returns the parent of the message at.
func (m *matrix) parent(at session.Ref) string {
	m.t.Helper()
	msg, err := m.store.Message(at)
	if err != nil {
		m.t.Fatalf("message %s: %v", at, err)
	}
	return msg.Parent
}

// lineage returns the history ending at at, as role:text lines.
func (m *matrix) lineage(at session.Ref) []string {
	m.t.Helper()
	return lmcTypedTextLines(historyAt(m.t, m.store, at).Typed())
}

// children returns the children of message id in at's session.
func (m *matrix) children(at session.Ref, id string) []string {
	m.t.Helper()
	tree, err := m.store.Tree(at.Session)
	if err != nil {
		m.t.Fatal(err)
	}
	return tree.Children[id]
}

// systemOf returns the system message a chat completions request carries.
func systemOf(body string) string {
	var request struct {
		Messages []struct {
			Role    string
			Content json.RawMessage
		}
	}
	if json.Unmarshal([]byte(body), &request) != nil || len(request.Messages) == 0 || request.Messages[0].Role != "system" {
		return ""
	}
	var text string
	if json.Unmarshal(request.Messages[0].Content, &text) == nil {
		return text
	}
	return string(request.Messages[0].Content)
}

func requireContains(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Fatalf("request lacks %q:\n%s", w, body)
		}
	}
}

func requireLacks(t *testing.T, body string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(body, u) {
			t.Fatalf("request carries %q:\n%s", u, body)
		}
	}
}

// 1. Two turns, then -resume S, which reports the saved S/M.
func TestMatrixResumeContinuesAfterTheNewestMessage(t *testing.T) {
	m := newMatrix(t)
	first := m.ok("first question")
	second := m.ok("second question", "-resume", first.Session)
	if m.parent(second) == "" || m.parent(second.At(m.parent(second))) != first.Message {
		t.Fatalf("the second turn does not follow %s", first)
	}
	requireContains(t, m.last(), "first question", "answer 1", "second question")
	if got := strings.Join(m.lineage(second), "|"); got != "user:first question|assistant:answer 1|user:second question|assistant:answer 2" {
		t.Fatalf("lineage %q", got)
	}
}

// 2. -resume S/A at an earlier answer with a prompt makes an alternative.
// 3. An empty stdin at a question answers it again, and both answers are
// children of the question.
func TestMatrixResumeAtAnEarlierMessage(t *testing.T) {
	m := newMatrix(t)
	first := m.ok("first question")
	second := m.ok("second question", "-resume", first.Session)
	question := m.parent(second)

	other := m.ok("other second question", "-resume", first.String())
	requireContains(t, m.last(), "first question", "answer 1", "other second question")
	requireLacks(t, m.last(), "answer 2", "\"second question")
	if m.parent(other.At(m.parent(other))) != first.Message {
		t.Fatalf("the other question does not follow %s", first)
	}

	again := m.ok("", "-resume", second.At(question).String())
	if m.parent(again) != question {
		t.Fatalf("the new answer %s follows %s, want the question %s", again, m.parent(again), question)
	}
	requireContains(t, m.last(), "second question")
	requireLacks(t, m.last(), "answer 2")
	if children := m.children(second, question); len(children) != 2 || children[0] != second.Message || children[1] != again.Message {
		t.Fatalf("the question's children %v, want both answers", children)
	}
}

// 4. A question with a developer instruction: edit it by resuming at the
// instruction, then answer the edited question again. Both answers descend
// from the edited question, the provider receives the instruction and the
// edited text both times, and the original exchange still reads in full.
func TestMatrixEditAQuestionAfterItsInstruction(t *testing.T) {
	m := newMatrix(t)
	original := m.ok("original question", "-developer", "be brief")
	question := original.At(m.parent(original))
	instruction := original.At(m.parent(question))
	if msg, err := m.store.Message(instruction); err != nil || msg.Role != core.RoleDeveloper {
		t.Fatalf("message %s is %+v: %v", instruction, msg, err)
	}

	edited := m.ok("edited question", "-resume", instruction.String())
	editedQuestion := edited.At(m.parent(edited))
	if m.parent(editedQuestion) != instruction.Message {
		t.Fatalf("the edited question follows %s", m.parent(editedQuestion))
	}
	requireContains(t, m.last(), `"role":"developer"`, "be brief", "edited question")
	requireLacks(t, m.last(), "original question")
	again := m.ok("", "-resume", editedQuestion.String())
	if m.parent(again) != editedQuestion.Message {
		t.Fatalf("the second answer follows %s", m.parent(again))
	}
	requireContains(t, m.last(), `"role":"developer"`, "be brief", "edited question")
	requireLacks(t, m.last(), "original question")
	shown, _, err := m.run("", "-show", original.String())
	if err != nil || !strings.Contains(shown, "original question") || !strings.Contains(shown, "answer 1") || !strings.Contains(shown, "be brief") {
		t.Fatalf("the original exchange: %v\n%s", err, shown)
	}
}

// 5. A new -s mid conversation applies from its turn on, a resume without
// -s keeps it, a resume from before the change does not have it, and
// -builtin-system restores the built-in prompt.
func TestMatrixSystemPromptFollowsTheLineage(t *testing.T) {
	m := newMatrix(t)
	_, stderr, err := m.run("q0", "-s", "a new session's prompt")
	if err != nil || strings.Contains(stderr, "Changed the") {
		t.Fatalf("a new session announced its settings: %v\n%s", err, stderr)
	}
	first := m.ok("q1")
	if got := systemOf(m.last()); got != prompts.DefaultSystemPrompt {
		t.Fatalf("first system prompt %q", got)
	}
	_, stderr, err = m.run("q2", "-resume", first.Session, "-s", "be terse")
	if err != nil || !strings.Contains(stderr, "Changed the system prompt from message "+first.Session+"/") {
		t.Fatalf("the change was not announced: %v\n%s", err, stderr)
	}
	if got := systemOf(m.last()); got != "be terse" {
		t.Fatalf("system prompt after -s: %q", got)
	}
	third := m.ok("q3", "-resume", first.Session)
	if got := systemOf(m.last()); got != "be terse" {
		t.Fatalf("system prompt on a later resume: %q", got)
	}
	m.ok("q2b", "-resume", first.String())
	if got := systemOf(m.last()); got != prompts.DefaultSystemPrompt {
		t.Fatalf("system prompt before the change: %q", got)
	}
	m.ok("q4", "-resume", third.String(), "-builtin-system")
	if got := systemOf(m.last()); got != prompts.DefaultSystemPrompt {
		t.Fatalf("system prompt after -builtin-system: %q", got)
	}
}

// 6. Alternating tool sets changes the built-in prompt of each run and
// records no settings.
func TestMatrixToolSetsAreRunOptions(t *testing.T) {
	m := newMatrix(t)
	first := m.ok("q1")
	m.ok("q2", "-resume", first.Session, "-tool")
	if got := systemOf(m.last()); !strings.Contains(got, "universal_command") {
		t.Fatalf("the tool run's system prompt %q", got)
	}
	last := m.ok("q3", "-resume", first.Session)
	if got := systemOf(m.last()); got != prompts.DefaultSystemPrompt {
		t.Fatalf("the run without tools sent %q", got)
	}
	records := 0
	for _, msg := range historyAt(t, m.store, last).Messages {
		if msg.Settings != nil {
			records++
		}
	}
	if records != 1 {
		t.Fatalf("%d settings records, want the session's first alone", records)
	}
}

// 7. A notebook attached later is announced and applies from its turn on, a
// resume from before it has none, and -notebook ” detaches it.
func TestMatrixNotebookAttachedAndDetached(t *testing.T) {
	m := newMatrix(t)
	book := t.TempDir()
	first := m.ok("q1")
	requireLacks(t, m.last(), "mcp__notebook__")
	_, stderr, err := m.run("q2", "-resume", first.Session, "-notebook", book, "-context-policy", "full")
	if err != nil || !strings.Contains(stderr, "notebook") || !strings.Contains(stderr, "Changed the ") {
		t.Fatalf("attaching was not announced: %v\n%s", err, stderr)
	}
	requireContains(t, m.last(), "mcp__notebook__")
	third := m.ok("q3", "-resume", first.Session)
	requireContains(t, m.last(), "mcp__notebook__")
	m.ok("q2b", "-resume", first.String())
	requireLacks(t, m.last(), "mcp__notebook__")
	_, stderr, err = m.run("q4", "-resume", third.String(), "-notebook", "")
	if err != nil || !strings.Contains(stderr, "Changed the notebook") {
		t.Fatalf("detaching was not announced: %v\n%s", err, stderr)
	}
	requireLacks(t, m.last(), "mcp__notebook__")
}

// 8. Two runs started together after one message each report their own
// S/M, and both follow the message.
func TestMatrixConcurrentRunsBranch(t *testing.T) {
	m := newMatrix(t)
	first := m.ok("q1")
	saved := make([]session.Ref, 2)
	var wg sync.WaitGroup
	for i := range saved {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, stderr, err := m.run(fmt.Sprintf("branch %d", i), "-resume", first.String())
			if err != nil {
				t.Errorf("run %d: %v\n%s", i, err, stderr)
				return
			}
			match := savedAtPattern.FindStringSubmatch(stderr)
			if match == nil {
				t.Errorf("run %d reported no position:\n%s", i, stderr)
				return
			}
			saved[i], _ = session.ParseRef(match[1])
		}(i)
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	if saved[0] == saved[1] {
		t.Fatalf("both runs report %s", saved[0])
	}
	if children := m.children(first, first.Message); len(children) != 2 {
		t.Fatalf("%s has children %v, want both questions", first, children)
	}
	for _, s := range saved {
		if m.parent(s.At(m.parent(s))) != first.Message {
			t.Fatalf("%s does not follow %s", s, first)
		}
	}
}

// 9. -delete S/M removes the message and what follows it; resuming the
// deleted message fails, and a run after deleting the newest message takes
// a new ID.
func TestMatrixDeleteAMessage(t *testing.T) {
	m := newMatrix(t)
	first := m.ok("q1")
	second := m.ok("q2", "-resume", first.Session)
	question := second.At(m.parent(second))
	if _, stderr, err := m.run("", "-delete", question.String()); err != nil {
		t.Fatalf("-delete: %v\n%s", err, stderr)
	}
	if _, stderr, err := m.run("q3", "-resume", second.String()); err == nil || !strings.Contains(stderr, "not found") {
		t.Fatalf("resuming a deleted message: %v\n%s", err, stderr)
	}
	third := m.ok("q3", "-resume", first.Session)
	newQuestion := third.At(m.parent(third))
	if m.parent(newQuestion) != first.Message || !olderRef(second, newQuestion) {
		t.Fatalf("the new question %s follows %s; the deleted messages ended at %s", newQuestion, m.parent(newQuestion), second)
	}
}

// olderRef reports whether a's message ID is lower than b's.
func olderRef(a, b session.Ref) bool {
	return len(a.Message) < len(b.Message) || len(a.Message) == len(b.Message) && a.Message < b.Message
}

// 10. Calls pending at a resumed answer run and their results are saved
// before the request; a failed request is retried from the results without
// running them again. A pending notebook call under another notebook stays
// pending and names its notebook.
func TestMatrixPendingCallsAtAResumedAnswer(t *testing.T) {
	m := newMatrix(t)
	counter := filepath.Join(t.TempDir(), "runs")
	sess := newTestSession(t, m.store, textEntry(core.RoleUser, "count once"))
	saveToolCalls(t, sess, "counting", claimedCalls(t, m.store, []core.ToolCall{{
		ID: "call_count", Name: "universal_command",
		Args: json.RawMessage(fmt.Sprintf(`{"command":["sh","-c","echo run >> \"$1\"; echo ran","sh",%q]}`, counter)),
	}}))
	m.mu.Lock()
	m.failNext = 1
	m.mu.Unlock()
	_, stderr, err := m.run("", "-resume", sess.Ref().String(), "-tool", "-tool-auto-approve")
	if err == nil || !strings.Contains(stderr, "1 tool result; resume there with empty input to get its answer") {
		t.Fatalf("failed request: %v\n%s", err, stderr)
	}
	results := session.Ref{Session: sess.Number, Message: regexp.MustCompile(`Saved through [0-9a-f]+/([0-9a-f]+)`).FindStringSubmatch(stderr)[1]}
	if m.parent(results) != sess.Head {
		t.Fatalf("the results follow %s, want the calls %s", m.parent(results), sess.Head)
	}
	answer := m.ok("", "-resume", results.String(), "-tool", "-tool-auto-approve")
	if m.parent(answer) != results.Message {
		t.Fatalf("the answer follows %s", m.parent(answer))
	}
	requireContains(t, m.last(), "ran")
	if data, err := os.ReadFile(counter); err != nil || strings.Count(string(data), "\n") != 1 {
		t.Fatalf("the call ran %q times: %v", data, err)
	}

	bookA, bookB := t.TempDir(), t.TempDir()
	settings := session.Settings{Notebook: bookA}
	noted := newTestSession(t, m.store, session.Entry{Role: core.RoleSystem, Settings: &settings}, textEntry(core.RoleUser, "read the note"))
	saveToolCalls(t, noted, "", claimedCalls(t, m.store, []core.ToolCall{{
		ID: "call_note", Name: "mcp__notebook__list", MCPServer: "notebook", MCPTool: "list", Args: json.RawMessage(`{}`),
	}}))
	before := m.requests()
	_, stderr, err = m.run("", "-resume", noted.Ref().String(), "-notebook", bookB, "-context-policy", "full")
	if err == nil || !strings.Contains(stderr, "was made for the notebook \""+bookA+"\"") {
		t.Fatalf("a notebook call under another notebook: %v\n%s", err, stderr)
	}
	if newest, _ := m.store.Newest(noted.Number); newest != noted.Head || m.requests() != before {
		t.Fatal("the call did not stay pending")
	}
}

// 11. A run that resumes after a checkpoint's boundary uses it. Under the
// notebook it was written with, a request carries that notebook's catalog;
// after a switch to another notebook it carries that one's, and after a
// detach none.
func TestMatrixCheckpointCatalogFollowsTheNotebook(t *testing.T) {
	m := newMatrix(t)
	bookA, bookB := t.TempDir(), t.TempDir()
	if _, err := (notebook.Store{Root: bookA}).Put(notebook.Edit{ID: "alpha", Title: "ALPHA-NOTE", Summary: "alpha summary", Body: "a", Sources: []string{"test"}, Expected: "new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (notebook.Store{Root: bookB}).Put(notebook.Edit{ID: "bravo", Title: "BRAVO-NOTE", Summary: "bravo summary", Body: "b", Sources: []string{"test"}, Expected: "new"}); err != nil {
		t.Fatal(err)
	}
	padding := strings.Repeat("padding ", 800)
	first := m.ok(padding+"first"+padding, "-notebook", bookA, "-context-policy", "handoff", "-context-window", "65536", "-context-trigger", "4200")
	requireContains(t, m.last(), "ALPHA-NOTE")
	second := m.ok("second", "-resume", first.Session)
	m.mu.Lock()
	writers := m.writers
	m.mu.Unlock()
	if writers != 1 {
		t.Fatalf("%d checkpoints written, want one", writers)
	}
	requireContains(t, m.last(), "HANDOFF-TEXT", "ALPHA-NOTE")
	requireLacks(t, m.last(), padding)

	m.ok("third", "-resume", second.Session, "-notebook", bookB)
	requireContains(t, m.last(), "HANDOFF-TEXT", "BRAVO-NOTE")
	requireLacks(t, m.last(), "ALPHA-NOTE", padding)
	m.ok("fourth", "-resume", second.Session, "-notebook", "")
	requireContains(t, m.last(), "HANDOFF-TEXT")
	requireLacks(t, m.last(), "ALPHA-NOTE", "BRAVO-NOTE", padding)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.writers != 1 {
		t.Fatalf("%d checkpoints written, want the first reused", m.writers)
	}
}

// 12. Empty stdin is refused after a completed answer, and after a lone
// settings change, which is not recorded.
func TestMatrixEmptyInputNeedsSomethingToAnswer(t *testing.T) {
	m := newMatrix(t)
	first := m.ok("q1")
	for _, flags := range [][]string{{"-resume", first.Session}, {"-resume", first.Session, "-s", "new prompt"}} {
		_, stderr, err := m.run("", flags...)
		if err == nil || !strings.Contains(stderr, "needs a prompt") {
			t.Fatalf("%v: %v\n%s", flags, err, stderr)
		}
	}
	if newest, _ := m.store.Newest(first.Session); newest != first.Message || m.requests() != 1 {
		t.Fatalf("the refused runs wrote %s or sent requests", newest)
	}
}

// blockContent makes writing the content of message id in session number
// fail, by putting a directory where its file goes.
func blockContent(t *testing.T, m *matrix, number, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(m.dir, number, id+".content.json"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// nextID returns the ID n places after the newest message's.
func nextID(t *testing.T, newest string, n uint64) string {
	t.Helper()
	var id uint64
	if _, err := fmt.Sscanf(newest, "%x", &id); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%04x", id+n)
}

// 13. A commit that stops after the developer instruction says to retry with
// the same prompt, and one whose question was saved without its answer says
// to resume at the question with empty input. Following either adds no
// duplicates.
func TestMatrixFailedWritesNameTheirRecovery(t *testing.T) {
	m := newMatrix(t)
	first := m.ok("q1")
	blockContent(t, m, first.Session, nextID(t, first.Message, 2))
	_, stderr, err := m.run("q2", "-resume", first.Session, "-developer", "be brief")
	instruction := first.At(nextID(t, first.Message, 1))
	if err == nil || !strings.Contains(stderr, "saved through "+instruction.String()+"; resume there with the same prompt to retry") || strings.Contains(stderr, "empty input") {
		t.Fatalf("a commit stopped after the instruction: %v\n%s", err, stderr)
	}
	retried := m.ok("q2", "-resume", instruction.String(), "-developer", "be brief")
	lines := m.lineage(retried)
	if strings.Count(strings.Join(lines, "|"), "developer:be brief") != 1 || m.parent(retried.At(m.parent(retried))) != instruction.Message {
		t.Fatalf("the retry's lineage %q", lines)
	}

	newest, err := m.store.Newest(first.Session)
	if err != nil {
		t.Fatal(err)
	}
	blockContent(t, m, first.Session, nextID(t, newest, 2))
	_, stderr, err = m.run("q3", "-resume", retried.String())
	question := first.At(nextID(t, newest, 1))
	if !strings.Contains(stderr, "Saved through "+question.String()+", your question; resume there with empty input to get its answer") {
		t.Fatalf("an answer that was not saved: %v\n%s", err, stderr)
	}
	answer := m.ok("", "-resume", question.String())
	if m.parent(answer) != question.Message || strings.Count(strings.Join(m.lineage(answer), "|"), "user:q3") != 1 {
		t.Fatalf("the answer's lineage %q", m.lineage(answer))
	}
}

// A sessions directory that is a file is refused before the question is
// sent, since the run could not save its answer there.
func TestMatrixSessionsDirThatIsAFile(t *testing.T) {
	m := newMatrix(t)
	if err := os.WriteFile(m.dir, []byte("not sessions"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := m.run("q1")
	if err == nil || !strings.Contains(stderr, "is not a directory") {
		t.Fatalf("a run with a file for its sessions directory: %v\n%s", err, stderr)
	}
	if n := m.requests(); n != 0 {
		t.Fatalf("the run sent %d requests", n)
	}
}

// 14. A piped REPL resumed at the instruction of Q0 → instruction → Q1, with
// Q0 unanswered: a first line of text is the edited question after the
// instruction, and an empty first line in a second run answers Q0.
func TestMatrixPipedREPLFirstInputDecides(t *testing.T) {
	m := newMatrix(t)
	sess := newTestSession(t, m.store, textEntry(core.RoleUser, "Q0 text"), textEntry(core.RoleDeveloper, "the instruction"), textEntry(core.RoleUser, "Q1 text"), textEntry(core.RoleAssistant, "A1 text"))
	instruction := historyAt(t, m.store, sess.Ref()).Ref(1)
	_, stderr, err := m.run("Q1 edited\n", "-repl", "-no-stream", "-resume", instruction.String())
	if err != nil || !strings.Contains(stderr, "awaits an answer") {
		t.Fatalf("first REPL: %v\n%s", err, stderr)
	}
	requireContains(t, m.last(), "Q0 text", "the instruction", "Q1 edited")
	requireLacks(t, m.last(), "Q1 text", "A1 text")
	if children := m.children(instruction, instruction.Message); len(children) != 2 {
		t.Fatalf("the instruction's children %v, want Q1 and the edit", children)
	}

	_, stderr, err = m.run("\n", "-repl", "-no-stream", "-resume", instruction.String())
	if err != nil {
		t.Fatalf("second REPL: %v\n%s", err, stderr)
	}
	body := m.last()
	requireContains(t, body, "Q0 text", "the instruction")
	requireLacks(t, body, "Q1 text", "Q1 edited")
	if children := m.children(instruction, instruction.Message); len(children) != 3 {
		t.Fatalf("the instruction's children %v, want the answer to Q0 as well", children)
	}
}

// The first run that writes into a directory of old sessions says how many
// there are and how to remove them, once.
func TestMatrixUpgradeNotice(t *testing.T) {
	m := newMatrix(t)
	old := filepath.Join(m.dir, "0001")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "0001.json"), []byte(`{"role":"user"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := m.run("q1")
	if err != nil || !strings.Contains(stderr, m.dir+" holds 1 session written by an older lmc") || !strings.Contains(stderr, "-delete") {
		t.Fatalf("first run: %v\n%s", err, stderr)
	}
	if saved := savedAt(t, stderr); saved.Session != "0002" {
		t.Fatalf("the new session took %s", saved.Session)
	}
	if _, stderr, err = m.run("q2"); err != nil || strings.Contains(stderr, "older lmc") {
		t.Fatalf("second run: %v\n%s", err, stderr)
	}
	listing, _, err := m.run("", "-show-sessions")
	if err != nil || !strings.Contains(listing, "0001 • older format") {
		t.Fatalf("listing: %v\n%s", err, listing)
	}
}
