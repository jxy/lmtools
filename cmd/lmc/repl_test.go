package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"lmtools/internal/mockserver"
	"lmtools/internal/session"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingNotifier records what it is given, formatted, and lets a test
// wait for a message.
type recordingNotifier struct {
	mu       sync.Mutex
	messages []string
	update   chan struct{}
}

func newRecordingNotifier() *recordingNotifier {
	return &recordingNotifier{update: make(chan struct{})}
}

func (n *recordingNotifier) record(kind, format string, args ...interface{}) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.messages = append(n.messages, kind+fmt.Sprintf(format, args...))
	close(n.update)
	n.update = make(chan struct{})
}

func (n *recordingNotifier) Infof(format string, args ...interface{}) {
	n.record("Note: ", format, args...)
}

func (n *recordingNotifier) Warnf(format string, args ...interface{}) {
	n.record("Warning: ", format, args...)
}

func (n *recordingNotifier) Errorf(format string, args ...interface{}) {
	n.record("Error: ", format, args...)
}

func (n *recordingNotifier) Promptf(format string, args ...interface{}) {
	n.record("", format, args...)
}

func (n *recordingNotifier) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.messages...)
}

func (n *recordingNotifier) count(want string) int {
	count := 0
	for _, message := range n.all() {
		if strings.Contains(message, want) {
			count++
		}
	}
	return count
}

// waitFor waits until a message contains want.
func (n *recordingNotifier) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		n.mu.Lock()
		update := n.update
		n.mu.Unlock()
		if n.count(want) > 0 {
			return
		}
		select {
		case <-update:
		case <-deadline:
			t.Fatalf("no message says %q; the messages are %q", want, n.all())
		}
	}
}

// readTurnsFrom feeds input to readTurn through an owner reading a pipe, and
// returns the turns it read, how the reading ended, and the notes.
func readTurnsFrom(t *testing.T, input string) ([]string, turnRead, []string) {
	t.Helper()
	owner, w := pipeOwner(t)
	if _, err := w.WriteString(input); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = w.Close()
	var turns, notes []string
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		turn, how, err := readTurn(ctx, owner, func(string) {}, func(format string, args ...interface{}) {
			notes = append(notes, fmt.Sprintf(format, args...))
		}, func() {})
		if err != nil {
			t.Fatalf("readTurn() error = %v", err)
		}
		if how != readTurnText {
			return turns, how, notes
		}
		turns = append(turns, turn)
	}
}

// The input contract, rule by rule, over input that is not a terminal,
// which a loop reads under the same rules as typed input.
func TestReadTurnInputContract(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		turns []string
		end   turnRead
	}{
		{name: "a line is a turn", input: "one\ntwo\n", turns: []string{"one", "two"}, end: readEnd},
		{name: "a backslash at the end continues the turn", input: "one\\\ntwo\n", turns: []string{"one\ntwo"}, end: readEnd},
		{name: "a doubled backslash ends the turn with one", input: "path\\\\\nnext\n", turns: []string{`path\`, "next"}, end: readEnd},
		{name: "an empty line at a fresh prompt sends nothing", input: "\n\none\n", turns: []string{"one"}, end: readEnd},
		{name: "a line of spaces at a fresh prompt sends nothing", input: "   \none\n", turns: []string{"one"}, end: readEnd},
		{name: "an empty line inside a turn is part of it", input: "a\\\n\\\nb\n", turns: []string{"a\n\nb"}, end: readEnd},
		{name: "an empty line ends a continuation as its last line", input: "a\\\n\nnext\n", turns: []string{"a\n", "next"}, end: readEnd},
		{name: "the text is sent as typed", input: "  indented  \n", turns: []string{"  indented  "}, end: readEnd},
		{name: "a carriage return before the newline", input: "hello\r\n", turns: []string{"hello"}, end: readEnd},
		{name: "pasted lines are turns one by one", input: "a\nb\nc\n", turns: []string{"a", "b", "c"}, end: readEnd},
		{name: "the end of input at a fresh prompt", input: "", end: readEnd},
		{name: "the end of input inside a continuation", input: "done\na\\\n", turns: []string{"done"}, end: readEndInsideTurn},
		{name: "the end of input after text without a newline", input: "abc", end: readEndInsideTurn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turns, end, notes := readTurnsFrom(t, tc.input)
			if strings.Join(turns, "|") != strings.Join(tc.turns, "|") || len(turns) != len(tc.turns) || end != tc.end {
				t.Fatalf("turns = %q ending %d, want %q ending %d", turns, end, tc.turns, tc.end)
			}
			if len(notes) != 0 {
				t.Fatalf("notes = %q, want none", notes)
			}
		})
	}
}

// A turn past the cap is discarded once it ends, a continuation included,
// with a note, and the next turn is read whole. A line past the input
// owner's own limit keeps its tail, so a backslash ending it still
// continues the turn being discarded.
func TestReadTurnDiscardsATurnPastTheCap(t *testing.T) {
	defer func(size int) { maxTurnBytes = size }(maxTurnBytes)
	maxTurnBytes = 10
	for _, tc := range []struct {
		name  string
		input string
	}{
		{name: "one line", input: "0123456789a\nok\n"},
		{name: "a continuation", input: "abcde\\\nfghijkl\\\nmn\nok\n"},
		{name: "a line past the owner's limit, continued", input: "0123456789abcdef\\\ntail\nok\n"},
		{name: "a line past the owner's limit, continued with a carriage return", input: "0123456789abcdef\\\r\ntail\nok\n"},
		{name: "a line past the owner's limit, ended by a doubled backslash", input: "0123456789abcdef\\\\\nok\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turns, end, notes := readTurnsFrom(t, tc.input)
			if len(turns) != 1 || turns[0] != "ok" || end != readEnd {
				t.Fatalf("turns = %q ending %d, want only ok", turns, end)
			}
			if len(notes) != 1 || !strings.Contains(notes[0], "longer than 10 bytes") {
				t.Fatalf("notes = %q, want the discarded turn", notes)
			}
		})
	}
}

// A turn exactly at the cap is sent: the cap counts the newlines between
// its lines, and not a final one the turn does not have.
func TestReadTurnSendsATurnAtTheCap(t *testing.T) {
	defer func(size int) { maxTurnBytes = size }(maxTurnBytes)
	maxTurnBytes = 10
	for _, tc := range []struct {
		name  string
		input string
		turn  string
	}{
		{name: "one line", input: "0123456789\n", turn: "0123456789"},
		{name: "a continuation", input: "01234\\\n6789\n", turn: "01234\n6789"},
		{name: "a doubled backslash", input: "012345678\\\\\n", turn: `012345678\`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turns, end, notes := readTurnsFrom(t, tc.input)
			if len(turns) != 1 || turns[0] != tc.turn || end != readEnd || len(notes) != 0 {
				t.Fatalf("turns = %q ending %d, notes %q; want %q", turns, end, notes, tc.turn)
			}
		})
	}
}

// An interrupt while a turn is read discards what was typed of it; at a
// fresh prompt it is reported as such.
func TestReadTurnInterrupted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		typed string
		want  turnRead
	}{
		{name: "at a fresh prompt", want: readInterrupted},
		{name: "inside a continuation", typed: "half of a turn\\\n", want: readInterruptedInsideTurn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, w := pipeOwner(t)
			if _, err := w.WriteString(tc.typed); err != nil {
				t.Fatalf("write: %v", err)
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			prompts := make(chan string, 4)
			results := make(chan turnRead, 1)
			go func() {
				_, how, _ := readTurn(ctx, owner, func(prompt string) { prompts <- prompt }, func(string, ...interface{}) {}, func() {})
				results <- how
			}()
			nextPrompt := func() string {
				select {
				case prompt := <-prompts:
					return prompt
				case how := <-results:
					t.Fatalf("readTurn() ended %d before the interrupt", how)
				case <-time.After(10 * time.Second):
					t.Fatal("readTurn() showed no prompt")
				}
				return ""
			}
			// The prompt for the line after what was typed.
			wantPrompt := freshPrompt
			if tc.typed != "" {
				nextPrompt()
				wantPrompt = continuationPrompt
			}
			if got := nextPrompt(); got != wantPrompt {
				t.Fatalf("prompt = %q, want %q", got, wantPrompt)
			}
			cancel(errInterrupted)
			select {
			case how := <-results:
				if how != tc.want {
					t.Fatalf("readTurn() ended %d, want %d", how, tc.want)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("readTurn() did not end")
			}
		})
	}
}

// newTestLoop makes a loop over te's environment, reading from a pipe as
// lmc -repl reads input that is not a terminal, with notes recorded.
func newTestLoop(t *testing.T, te turnTestEnv) (*replLoop, *os.File, *recordingNotifier) {
	t.Helper()
	owner, w := pipeOwner(t)
	notifier := newRecordingNotifier()
	te.env.notifier = notifier
	return &replLoop{
		env:     te.env,
		base:    te.opts,
		input:   owner,
		signals: newREPLSignals(),
		diag:    &bytes.Buffer{},
		images:  te.opts.Images,
	}, w, notifier
}

func typeTurns(t *testing.T, w *os.File, input string, end bool) {
	t.Helper()
	if _, err := w.WriteString(input); err != nil {
		t.Fatalf("write: %v", err)
	}
	if end {
		_ = w.Close()
	}
}

// numberedAnswers answers the nth request with "answer n".
func numberedAnswers() mockserver.MockServerOption {
	var mu sync.Mutex
	count := 0
	return mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		mu.Lock()
		defer mu.Unlock()
		count++
		return openAITextResponse(fmt.Sprintf("answer %d", count)), http.StatusOK, nil
	})
}

func sessionLines(t *testing.T, sess *session.Session) []string {
	t.Helper()
	if sess == nil {
		t.Fatal("the loop holds no session")
	}
	messages, err := session.BuildMessagesForSession(context.Background(), sess)
	if err != nil {
		t.Fatalf("BuildMessagesForSession() error = %v", err)
	}
	return lmcTypedTextLines(messages)
}

// Consecutive turns continue one session: the second request carries the
// first turn, and the session holds both.
func TestREPLConsecutiveTurnsShareOneSession(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	te := newTurnTestEnv(t, server)
	loop, w, _ := newTestLoop(t, te)
	typeTurns(t, w, "first question\nsecond question\n", true)

	if err := loop.run(false); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	requests := server.GetRequests()
	if len(requests) != 2 {
		t.Fatalf("%d requests, want 2", len(requests))
	}
	for _, want := range []string{"first question", "answer 1", "second question"} {
		if !strings.Contains(requests[1].Body, want) {
			t.Fatalf("the second request lacks %q: %s", want, requests[1].Body)
		}
	}
	if count := sessionDirCount(t, te.sessionsDir); count != 1 {
		t.Fatalf("%d sessions, want 1", count)
	}
	want := "user:first question|assistant:answer 1|user:second question|assistant:answer 2"
	if got := strings.Join(sessionLines(t, loop.from), "|"); !strings.HasSuffix(got, want) {
		t.Fatalf("session = %q, want it to end %q", got, want)
	}
	if got := te.stdout.String(); !strings.Contains(got, "answer 1") || !strings.Contains(got, "answer 2") {
		t.Fatalf("stdout = %q, want both answers", got)
	}
}

// A turn whose request fails is reported and commits nothing, so the loop
// keeps its starting point: the next turn continues the resumed session,
// without the failed prompt, and no new session appears.
func TestREPLFailedTurnKeepsTheStartingPoint(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if attempts == 1 {
			return nil, http.StatusInternalServerError, errors.New("provider down")
		}
		return openAITextResponse("answer after the failure"), http.StatusOK, nil
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server)
	sess, err := session.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	appendTestSessionMessage(t, context.Background(), sess, core.RoleUser, "earlier question")
	appendTestSessionMessage(t, context.Background(), sess, core.RoleAssistant, "earlier answer")
	te.opts.Resume = session.GetSessionID(sess.Path)
	loop, w, notifier := newTestLoop(t, te)
	typeTurns(t, w, "first try\nsecond try\n", true)

	if err := loop.run(false); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if notifier.count("Error: ") != 1 {
		t.Fatalf("messages = %q, want the failure reported once", notifier.all())
	}
	requests := server.GetRequests()
	if len(requests) != 2 || !strings.Contains(requests[1].Body, "earlier answer") || strings.Contains(requests[1].Body, "first try") {
		t.Fatalf("second request = %v, want the resumed session without the failed prompt", requests)
	}
	if count := sessionDirCount(t, te.sessionsDir); count != 1 {
		t.Fatalf("%d sessions, want the resumed one alone", count)
	}
	if got := strings.Join(sessionLines(t, loop.from), "|"); !strings.HasSuffix(got, "user:second try|assistant:answer after the failure") ||
		!strings.Contains(got, "earlier answer") || session.GetRootSession(loop.from.Path) != session.GetRootSession(sess.Path) {
		t.Fatalf("session %s = %q, want the resumed session continued", loop.from.Path, got)
	}
}

// replPNG is a PNG header, enough for -image to take the file as a PNG.
var replPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}

// A resumed session holds a pending call. Before the first prompt the loop
// runs it and commits its result, and the request that follows fails. The
// first prompt then continues from the head that commit left, runs nothing
// again, and still carries the -image attachment, which only a committed
// user message consumes.
func TestREPLStartupRecoveryKeepsTheImagesForThePrompt(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if attempts == 1 {
			return nil, http.StatusInternalServerError, errors.New("provider down")
		}
		return openAITextResponse("saw the picture"), http.StatusOK, nil
	}))
	defer server.Close()
	image := filepath.Join(t.TempDir(), "picture.png")
	if err := os.WriteFile(image, replPNG, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	te := newTurnTestEnv(t, server, "-tool", "-tool-auto-approve", "-image", image)

	counter := filepath.Join(t.TempDir(), "runs")
	calls := []core.ToolCall{{
		ID:   "call_count",
		Name: "universal_command",
		Args: json.RawMessage(fmt.Sprintf(`{"command":["sh","-c","echo run >> \"$1\"; echo ran","sh",%q]}`, counter)),
	}}
	core.AssignInvocationIDs(calls)
	claim, err := session.NewToolJournal().Claim(calls)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	claim.Release()
	sess, err := session.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	appendTestSessionMessage(t, context.Background(), sess, core.RoleUser, "count once")
	if _, err := session.SaveAssistantResponseWithTools(context.Background(), sess, "counting", calls, "gpt-test"); err != nil {
		t.Fatalf("SaveAssistantResponseWithTools() error = %v", err)
	}
	te.opts.Resume = session.GetSessionID(sess.Path)
	loop, w, _ := newTestLoop(t, te)
	typeTurns(t, w, "what is in the picture?\n", true)

	if err := loop.run(false); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	requests := server.GetRequests()
	if len(requests) != 2 {
		t.Fatalf("%d requests, want the recovery's and the prompt's", len(requests))
	}
	if !strings.Contains(requests[0].Body, "ran") || strings.Contains(requests[0].Body, "data:image/png") {
		t.Fatalf("the recovery's request = %s, want the call's result without the image", requests[0].Body)
	}
	for _, want := range []string{"ran", "what is in the picture?", "data:image/png"} {
		if !strings.Contains(requests[1].Body, want) {
			t.Fatalf("the prompt's request lacks %q: %s", want, requests[1].Body)
		}
	}
	data, err := os.ReadFile(counter)
	if err != nil || strings.Count(string(data), "\n") != 1 {
		t.Fatalf("the pending call ran %q times (err %v), want once", data, err)
	}
	if loop.images != nil {
		t.Fatal("the loop kept the images after the prompt carrying them committed")
	}
}

// With stdout not a terminal, each answer ends with a newline and an empty
// line separates it from the next. A turn that wrote nothing adds nothing.
func TestREPLSeparatesAnswersOnARedirectedStdout(t *testing.T) {
	var mu sync.Mutex
	count := 0
	answers := []string{"one", "two\n", "", "three\n\n"}
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		mu.Lock()
		defer mu.Unlock()
		count++
		if answers[count-1] == "" {
			return nil, http.StatusInternalServerError, errors.New("provider down")
		}
		return openAITextResponse(answers[count-1]), http.StatusOK, nil
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server)
	loop, w, _ := newTestLoop(t, te)
	var file bytes.Buffer
	loop.answers = &separatedAnswers{out: &file}
	te.env.stdout = loop.answers
	te.env.answersOnScreen = false
	typeTurns(t, w, "a\nb\nc\nd\n", true)

	if err := loop.run(false); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got, want := file.String(), "one\n\ntwo\n\nthree\n\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

// A turn that showed an answer the session does not hold stops the loop,
// which says so: the next turn would continue from a transcript without it.
func TestREPLStopsAfterAnAnswerItCouldNotSave(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	te := newTurnTestEnv(t, server)
	afterTurnCommitForTest = func(sess *session.Session) {
		if err := os.Rename(sess.Path, sess.Path+".moved"); err != nil {
			t.Errorf("Rename() error = %v", err)
		}
		if err := os.WriteFile(sess.Path, nil, 0o600); err != nil {
			t.Errorf("WriteFile() error = %v", err)
		}
	}
	t.Cleanup(func() { afterTurnCommitForTest = nil })
	loop, w, _ := newTestLoop(t, te)
	typeTurns(t, w, "first\nsecond\n", true)

	err := loop.run(false)
	if err == nil || !strings.Contains(err.Error(), "without the answer shown") {
		t.Fatalf("run() error = %v, want the loop stopped over the unsaved answer", err)
	}
	if requests := server.GetRequests(); len(requests) != 1 {
		t.Fatalf("%d requests, want the loop stopped before the second turn", len(requests))
	}
}

// An interrupt during a turn cancels it and the loop goes on at the prompt,
// reporting that nothing was saved. Another interrupt at the idle prompt
// ends the loop.
func TestREPLInterruptCancelsTheTurnAndASecondExits(t *testing.T) {
	var loop *replLoop
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(r *http.Request) (interface{}, int, error) {
		loop.signals.interrupt()
		<-r.Context().Done()
		return nil, http.StatusServiceUnavailable, errors.New("cancelled")
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server)
	loop, w, notifier := newTestLoop(t, te)
	typeTurns(t, w, "slow question\n", false)

	result := make(chan error, 1)
	go func() { result <- loop.run(false) }()
	notifier.waitFor(t, "Turn cancelled; nothing was saved.")
	loop.signals.interrupt()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("run() error = %v, want the loop ended by the second interrupt", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the second interrupt did not end the loop")
	}
	if count := sessionDirCount(t, te.sessionsDir); count != 0 {
		t.Fatalf("%d sessions, want none from the cancelled turn", count)
	}
}

// promptWriter records the prompts a loop shows, and lets a test wait for
// the nth one.
type promptWriter struct {
	mu      sync.Mutex
	prompts int
	update  chan struct{}
}

func newPromptWriter() *promptWriter {
	return &promptWriter{update: make(chan struct{})}
}

func (p *promptWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s := string(b); s == freshPrompt || s == continuationPrompt {
		p.prompts++
		close(p.update)
		p.update = make(chan struct{})
	}
	return len(b), nil
}

func (p *promptWriter) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		p.mu.Lock()
		prompts, update := p.prompts, p.update
		p.mu.Unlock()
		if prompts >= n {
			return
		}
		select {
		case <-update:
		case <-deadline:
			t.Fatalf("%d prompts shown, want %d", prompts, n)
		}
	}
}

// An interrupt at an idle prompt says how to exit and prompts again; a
// second one in a row ends the loop. Any line read in between disarms it,
// whether it is sent, sends nothing, or is discarded.
func TestREPLIdleInterrupt(t *testing.T) {
	defer func(size int) { maxTurnBytes = size }(maxTurnBytes)
	maxTurnBytes = 10
	for _, tc := range []struct {
		name  string
		line  string
		sends bool
	}{
		{name: "a turn", line: "a question\n", sends: true},
		{name: "an empty line", line: "\n"},
		{name: "a line of spaces", line: "   \n"},
		{name: "a turn past the cap", line: "0123456789abc\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := mockserver.NewMockServer(numberedAnswers())
			defer server.Close()
			te := newTurnTestEnv(t, server)
			loop, w, notifier := newTestLoop(t, te)
			prompts := newPromptWriter()
			loop.terminal, loop.diag = true, prompts

			result := make(chan error, 1)
			go func() { result <- loop.run(false) }()
			prompts.waitFor(t, 1)
			loop.signals.interrupt()
			prompts.waitFor(t, 2)
			typeTurns(t, w, tc.line, false)
			prompts.waitFor(t, 3)
			loop.signals.interrupt()
			prompts.waitFor(t, 4)
			if notifier.count("Press Ctrl-C again") != 2 {
				t.Fatalf("messages = %q, want the hint again after the line disarmed the first interrupt", notifier.all())
			}
			loop.signals.interrupt()
			select {
			case err := <-result:
				if err != nil {
					t.Fatalf("run() error = %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the second interrupt in a row did not end the loop")
			}
			if requests := len(server.GetRequests()); (requests == 1) != tc.sends || requests > 1 {
				t.Fatalf("%d requests, want %v for the line", requests, tc.sends)
			}
		})
	}
}

// SIGTERM or SIGHUP ends the process's context: the turn in flight is
// cancelled, what it saved stays, and the loop stops with the cause.
func TestREPLTerminationStopsTheLoop(t *testing.T) {
	var loop *replLoop
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(r *http.Request) (interface{}, int, error) {
		loop.signals.terminate(os.Interrupt)
		<-r.Context().Done()
		return nil, http.StatusServiceUnavailable, errors.New("stopped")
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server)
	loop, w, notifier := newTestLoop(t, te)
	typeTurns(t, w, "question\nnever sent\n", true)

	err := loop.run(false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run() error = %v, want the stop", err)
	}
	if notifier.count("Turn stopped; nothing was saved.") != 1 {
		t.Fatalf("messages = %q, want the stopped turn reported", notifier.all())
	}
	if requests := server.GetRequests(); len(requests) != 1 {
		t.Fatalf("%d requests, want none after the stop", len(requests))
	}
}

// Another writer appends to the session while a turn runs. The turn's
// writes move to a fork through the head the loop kept, the loop reports it
// and continues there, and the other writer's message stays out of every
// later request.
func TestREPLContinuesInAConflictFork(t *testing.T) {
	var mu sync.Mutex
	count := 0
	var sessionsDir string
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		mu.Lock()
		defer mu.Unlock()
		count++
		if count == 2 {
			entries, err := os.ReadDir(sessionsDir)
			if err != nil || len(entries) == 0 {
				return nil, http.StatusInternalServerError, fmt.Errorf("no session to append to: %v", err)
			}
			other, err := session.LoadSession(filepath.Join(sessionsDir, entries[0].Name()))
			if err != nil {
				return nil, http.StatusInternalServerError, err
			}
			appendTestSessionMessage(t, context.Background(), other, core.RoleUser, "someone else's line")
		}
		return openAITextResponse(fmt.Sprintf("answer %d", count)), http.StatusOK, nil
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server)
	sessionsDir = te.sessionsDir
	loop, w, notifier := newTestLoop(t, te)
	typeTurns(t, w, "one\ntwo\nthree\n", true)

	if err := loop.run(false); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if notifier.count("changed while this turn ran") != 1 {
		t.Fatalf("messages = %q, want the fork reported once", notifier.all())
	}
	requests := server.GetRequests()
	if len(requests) != 3 || strings.Contains(requests[2].Body, "someone else") || !strings.Contains(requests[2].Body, "answer 2") {
		t.Fatalf("third request = %v, want it in the fork, without the other writer's line", requests)
	}
	want := "user:one|assistant:answer 1|user:two|assistant:answer 2|user:three|assistant:answer 3"
	if got := strings.Join(sessionLines(t, loop.from), "|"); !strings.HasSuffix(got, want) || strings.Contains(got, "someone else") {
		t.Fatalf("fork = %q, want the loop's turns alone", got)
	}
}

// -branch naming an assistant message regenerates it before the first
// prompt, and the prompts continue from the regenerated answer. A
// regeneration that saves nothing ends the loop with its error.
func TestREPLRegeneratesBeforeTheFirstPrompt(t *testing.T) {
	makeSession := func(t *testing.T) string {
		sess, err := session.CreateSession("", core.NewTestLogger(false))
		if err != nil {
			t.Fatalf("CreateSession() error = %v", err)
		}
		appendTestSessionMessage(t, context.Background(), sess, core.RoleUser, "the question")
		appendTestSessionMessage(t, context.Background(), sess, core.RoleAssistant, "the old answer")
		return session.GetSessionID(sess.Path) + "/0001"
	}
	t.Run("then the prompts continue", func(t *testing.T) {
		server := mockserver.NewMockServer(numberedAnswers())
		defer server.Close()
		te := newTurnTestEnv(t, server)
		te.opts.Branch = makeSession(t)
		loop, w, _ := newTestLoop(t, te)
		typeTurns(t, w, "follow up\n", true)
		if err := loop.run(true); err != nil {
			t.Fatalf("run() error = %v", err)
		}
		requests := server.GetRequests()
		if len(requests) != 2 || strings.Contains(requests[0].Body, "the old answer") ||
			!strings.Contains(requests[1].Body, "answer 1") || !strings.Contains(requests[1].Body, "follow up") {
			t.Fatalf("requests = %v, want the regeneration and then the prompt after the new answer", requests)
		}
	})
	t.Run("a regeneration that saves nothing", func(t *testing.T) {
		server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
			return nil, http.StatusInternalServerError, errors.New("provider down")
		}))
		defer server.Close()
		te := newTurnTestEnv(t, server)
		te.opts.Branch = makeSession(t)
		loop, w, _ := newTestLoop(t, te)
		typeTurns(t, w, "never sent\n", true)
		if err := loop.run(true); err == nil {
			t.Fatal("run() error = nil, want the regeneration's failure")
		}
		if requests := server.GetRequests(); len(requests) != 1 {
			t.Fatalf("%d requests, want the regeneration's alone", len(requests))
		}
	})
}

// runWithArgs runs lmc's run in process with args and stdin, its stdout
// sent to a file, restoring the process state it changes.
func runWithArgs(t *testing.T, stdin *os.File, args ...string) error {
	t.Helper()
	stdout, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer stdout.Close()
	defer func(oldStdin, oldStdout *os.File, oldArgs []string, sessionsDir string) {
		os.Stdin, os.Stdout, os.Args = oldStdin, oldStdout, oldArgs
		session.SetSessionsDir(sessionsDir)
		logger.ResetForTesting()
	}(os.Stdin, os.Stdout, os.Args, session.GetSessionsDir())
	os.Stdin, os.Stdout = stdin, stdout
	os.Args = append([]string{"lmc"}, args...)
	return run(core.NewTestNotifier())
}

// -branch on an assistant message regenerates it without the images, and a
// loop keeps -image for its first prompt, so -repl accepts the pair a single
// run refuses: the regeneration goes out first, and the typed prompt
// carries the image.
func TestREPLRegenerationLeavesTheImagesForThePrompt(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	sessionsDir := t.TempDir()
	defer func(dir string) { session.SetSessionsDir(dir) }(session.GetSessionsDir())
	session.SetSessionsDir(sessionsDir)
	sess, err := session.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	appendTestSessionMessage(t, context.Background(), sess, core.RoleUser, "the question")
	appendTestSessionMessage(t, context.Background(), sess, core.RoleAssistant, "the old answer")
	image := filepath.Join(t.TempDir(), "picture.png")
	if err := os.WriteFile(image, replPNG, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	defer r.Close()
	if _, err := w.WriteString("what is in the picture?\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = w.Close()

	args := []string{
		"-provider", "openai", "-provider-url", server.URL() + "/v1", "-model", "gpt-test",
		"-log-dir", t.TempDir(), "-sessions-dir", sessionsDir,
		"-branch", session.GetSessionID(sess.Path) + "/0001", "-image", image,
	}
	if err := runWithArgs(t, r, args...); err == nil || !strings.Contains(err.Error(), "-image needs a user turn") {
		t.Fatalf("single run error = %v, want the image refused", err)
	}
	if err := runWithArgs(t, r, append([]string{"-repl", "-stream=false"}, args...)...); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	requests := server.GetRequests()
	if len(requests) != 2 {
		t.Fatalf("%d requests, want the regeneration's and the prompt's", len(requests))
	}
	if strings.Contains(requests[0].Body, "data:image/png") || strings.Contains(requests[0].Body, "the old answer") {
		t.Fatalf("the regeneration's request = %s, want the question alone", requests[0].Body)
	}
	for _, want := range []string{"answer 1", "what is in the picture?", "data:image/png"} {
		if !strings.Contains(requests[1].Body, want) {
			t.Fatalf("the prompt's request lacks %q: %s", want, requests[1].Body)
		}
	}
}
