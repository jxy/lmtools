package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
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
		}, func() {}, false)
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
				_, how, _ := readTurn(ctx, owner, func(prompt string) { prompts <- prompt }, func(string, ...interface{}) {}, func() {}, false)
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
	return lmcTypedTextLines(historyOf(t, sess))
}

// Consecutive turns continue one session: the second request carries the
// first turn, and the session holds both.
func TestREPLConsecutiveTurnsShareOneSession(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	te := newTurnTestEnv(t, server)
	loop, w, _ := newTestLoop(t, te)
	typeTurns(t, w, "first question\nsecond question\n", true)

	if err := loop.run(); err != nil {
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
	sess := newTestSession(t, te.store(), textEntry(core.RoleUser, "earlier question"), textEntry(core.RoleAssistant, "earlier answer"))
	te.env.cfg.Start = sess.Ref()
	loop, w, notifier := newTestLoop(t, te)
	typeTurns(t, w, "first try\nsecond try\n", true)

	if err := loop.run(); err == nil || !strings.Contains(err.Error(), "1 REPL turn(s) failed") {
		t.Fatalf("run() error = %v, want aggregate failure", err)
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
		!strings.Contains(got, "earlier answer") || loop.from.Number != sess.Number {
		t.Fatalf("session %s = %q, want the resumed session continued", loop.from.Ref(), got)
	}
}

// replPNG is a complete image, so attachment tests cover the file loader too.
var replPNG = func() []byte {
	var out bytes.Buffer
	_ = png.Encode(&out, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	return out.Bytes()
}()

// A resumed session holds a pending call. Before the first prompt the loop
// runs it and saves its result without a request. The first input, text,
// is a new turn after the result: its request carries the result and the
// -image attachment, which only a committed user message consumes.
func TestREPLStartupRecoveryKeepsTheImagesForThePrompt(t *testing.T) {
	server := mockserver.NewMockServer(mockserver.WithDefaultResponse("saw the picture"))
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
	sess := newTestSession(t, te.store(), textEntry(core.RoleUser, "count once"))
	saveToolCalls(t, sess, "counting", claimedCalls(t, te.store(), calls))
	te.env.cfg.Start = sess.Ref()
	loop, w, _ := newTestLoop(t, te)
	typeTurns(t, w, "what is in the picture?\n", true)

	if err := loop.run(); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	requests := server.GetRequests()
	if len(requests) != 1 {
		t.Fatalf("%d requests, want the prompt's alone", len(requests))
	}
	for _, want := range []string{"ran", "what is in the picture?", "data:image/png"} {
		if !strings.Contains(requests[0].Body, want) {
			t.Fatalf("the prompt's request lacks %q: %s", want, requests[0].Body)
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

	if err := loop.run(); err == nil || !strings.Contains(err.Error(), "1 REPL turn(s) failed") {
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
		replaceWithFile(t, filepath.Join(te.sessionsDir, sess.Number))
	}
	t.Cleanup(func() { afterTurnCommitForTest = nil })
	loop, w, _ := newTestLoop(t, te)
	typeTurns(t, w, "first\nsecond\n", true)

	err := loop.run()
	if err == nil || !strings.Contains(err.Error(), "answer could not be saved") {
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
	go func() { result <- loop.run() }()
	notifier.waitFor(t, "Turn cancelled; nothing was saved.")
	loop.signals.interrupt()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "1 REPL turn(s) failed") {
			t.Fatalf("run() error = %v, want the cancelled pipe turn to fail", err)
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
			go func() { result <- loop.run() }()
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

	err := loop.run()
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

// Another writer appends after the loop's last message while a turn runs.
// The turn's messages follow the loop's own, as an alternative to the other
// writer's, and the other writer's line stays out of every later request.
func TestREPLContinuesAfterItsOwnMessages(t *testing.T) {
	var mu sync.Mutex
	count := 0
	var store *session.Store
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		mu.Lock()
		defer mu.Unlock()
		count++
		if count == 2 {
			newest, err := store.Newest("0001")
			if err != nil {
				return nil, http.StatusInternalServerError, err
			}
			if _, err := store.Append(context.Background(), session.Ref{Session: "0001", Message: newest}, textEntry(core.RoleUser, "someone else's line")); err != nil {
				return nil, http.StatusInternalServerError, err
			}
		}
		return openAITextResponse(fmt.Sprintf("answer %d", count)), http.StatusOK, nil
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server)
	store = te.store()
	loop, w, _ := newTestLoop(t, te)
	typeTurns(t, w, "one\ntwo\nthree\n", true)

	if err := loop.run(); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	requests := server.GetRequests()
	if len(requests) != 3 || strings.Contains(requests[2].Body, "someone else") || !strings.Contains(requests[2].Body, "answer 2") {
		t.Fatalf("third request = %v, want the loop's history without the other writer's line", requests)
	}
	want := "user:one|assistant:answer 1|user:two|assistant:answer 2|user:three|assistant:answer 3"
	if got := strings.Join(sessionLines(t, loop.from), "|"); !strings.HasSuffix(got, want) || strings.Contains(got, "someone else") {
		t.Fatalf("lineage = %q, want the loop's turns alone", got)
	}
}

// Resumed at a question, the loop says the question awaits an answer. An
// empty first line asks for it, and the prompts continue from the new
// answer; text as the first line is a new turn after the question instead.
func TestREPLFirstInputDecidesAtAQuestion(t *testing.T) {
	question := func(t *testing.T, te turnTestEnv) {
		t.Helper()
		sess := newTestSession(t, te.store(), textEntry(core.RoleUser, "the question"), textEntry(core.RoleAssistant, "the old answer"))
		te.env.cfg.Start = sess.Ref().At("0001")
	}
	t.Run("an empty line answers", func(t *testing.T) {
		server := mockserver.NewMockServer(numberedAnswers())
		defer server.Close()
		te := newTurnTestEnv(t, server)
		question(t, te)
		loop, w, notes := newTestLoop(t, te)
		typeTurns(t, w, "\nfollow up\n", true)
		if err := loop.run(); err != nil {
			t.Fatalf("run() error = %v", err)
		}
		if !strings.Contains(strings.Join(notes.all(), "\n"), "0001/0001 awaits an answer") {
			t.Fatalf("notes = %q", notes.all())
		}
		requests := server.GetRequests()
		if len(requests) != 2 || strings.Contains(requests[0].Body, "the old answer") ||
			!strings.Contains(requests[1].Body, "answer 1") || !strings.Contains(requests[1].Body, "follow up") {
			t.Fatalf("requests = %v, want the answer and then the prompt after it", requests)
		}
		want := "user:the question|assistant:answer 1|user:follow up|assistant:answer 2"
		if got := strings.Join(sessionLines(t, loop.from), "|"); got != want {
			t.Fatalf("lineage = %q, want %q", got, want)
		}
	})
	t.Run("a queued instruction waits for a new turn", func(t *testing.T) {
		server := mockserver.NewMockServer(numberedAnswers())
		defer server.Close()
		te := newTurnTestEnv(t, server, "-developer", "Use short answers.")
		question(t, te)
		loop, w, _ := newTestLoop(t, te)
		typeTurns(t, w, "\nfollow up\n", true)
		if err := loop.run(); err != nil {
			t.Fatalf("run() error = %v", err)
		}
		requests := server.GetRequests()
		if len(requests) != 2 || strings.Contains(requests[0].Body, "Use short answers.") || !strings.Contains(requests[1].Body, "Use short answers.") {
			t.Fatalf("requests = %v, want the instruction with the new turn alone", requests)
		}
	})
	t.Run("text is a new turn", func(t *testing.T) {
		server := mockserver.NewMockServer(numberedAnswers())
		defer server.Close()
		te := newTurnTestEnv(t, server)
		question(t, te)
		loop, w, _ := newTestLoop(t, te)
		typeTurns(t, w, "more detail\n", true)
		if err := loop.run(); err != nil {
			t.Fatalf("run() error = %v", err)
		}
		want := "user:the question|user:more detail|assistant:answer 1"
		if got := strings.Join(sessionLines(t, loop.from), "|"); got != want {
			t.Fatalf("lineage = %q, want %q", got, want)
		}
	})
}

// A turn that fails at a start point awaiting an answer, the empty line
// that asks for the answer or text that starts a new turn after it, leaves
// the answer awaited: the next empty line asks for it again.
func TestREPLEmptyLineRetriesAFailedAnswer(t *testing.T) {
	for _, first := range []string{"", "more detail"} {
		t.Run(fmt.Sprintf("after %q", first), func(t *testing.T) {
			var mu sync.Mutex
			count := 0
			server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
				mu.Lock()
				defer mu.Unlock()
				count++
				if count == 1 {
					return nil, http.StatusInternalServerError, errors.New("provider down")
				}
				return openAITextResponse(fmt.Sprintf("answer %d", count)), http.StatusOK, nil
			}))
			defer server.Close()
			te := newTurnTestEnv(t, server)
			sess := newTestSession(t, te.store(), textEntry(core.RoleUser, "the question"), textEntry(core.RoleAssistant, "the old answer"))
			te.env.cfg.Start = sess.Ref().At("0001")
			loop, w, notes := newTestLoop(t, te)
			typeTurns(t, w, first+"\n\n", true)
			if err := loop.run(); err == nil || !strings.Contains(err.Error(), "1 REPL turn(s) failed") {
				t.Fatalf("run() error = %v, want the failed turn counted", err)
			}
			if !strings.Contains(strings.Join(notes.all(), "\n"), "0001/0001 still awaits an answer") {
				t.Fatalf("notes = %q", notes.all())
			}
			if requests := server.GetRequests(); len(requests) != 2 {
				t.Fatalf("%d requests, want the failed turn and the answer", len(requests))
			}
			if got := strings.Join(sessionLines(t, loop.from), "|"); got != "user:the question|assistant:answer 2" {
				t.Fatalf("lineage = %q", got)
			}
		})
	}
}

// A failed turn after an answer leaves nothing awaited, so an empty line
// after it sends nothing.
func TestREPLEmptyLineAfterAFailedTurnAtAnAnswer(t *testing.T) {
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		return nil, http.StatusInternalServerError, errors.New("provider down")
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server)
	sess := newTestSession(t, te.store(), textEntry(core.RoleUser, "the question"), textEntry(core.RoleAssistant, "the answer"))
	te.env.cfg.Start = sess.Ref()
	loop, w, notes := newTestLoop(t, te)
	typeTurns(t, w, "more\n\n", true)
	if err := loop.run(); err == nil || !strings.Contains(err.Error(), "1 REPL turn(s) failed") {
		t.Fatalf("run() error = %v, want the failed turn counted", err)
	}
	if requests := server.GetRequests(); len(requests) != 1 || strings.Contains(strings.Join(notes.all(), "\n"), "awaits an answer") {
		t.Fatalf("%d requests, notes %q", len(requests), notes.all())
	}
}

// startAtAnswered starts te's runs at a session whose question is answered,
// and returns the content file of the question.
func startAtAnswered(t *testing.T, te turnTestEnv) string {
	t.Helper()
	sess := newTestSession(t, te.store(), textEntry(core.RoleUser, "the question"), textEntry(core.RoleAssistant, "the answer"))
	te.env.cfg.Start = sess.Ref()
	return filepath.Join(te.sessionsDir, sess.Number, "0001.content.json")
}

// A loop reads the session it resumes once: its first turn and a pin
// continue from the history the startup read. The question's content file
// is removed once the startup has read it, which a turn reading the history
// again could not send.
func TestREPLReadsTheSessionItResumesOnce(t *testing.T) {
	t.Run("a turn", func(t *testing.T) {
		server := mockserver.NewMockServer(numberedAnswers())
		defer server.Close()
		te := newTurnTestEnv(t, server)
		question := startAtAnswered(t, te)
		loop, _, _ := newTestLoop(t, te)
		if err := loop.prepareStart(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(question); err != nil {
			t.Fatal(err)
		}
		if _, err := loop.runTurn(t.Context(), session.Turn{Prompt: "next"}); err != nil {
			t.Fatalf("the first turn: %v", err)
		}
		if requests := server.GetRequests(); len(requests) != 1 || !strings.Contains(requests[0].Body, "the question") {
			t.Fatalf("requests = %v", requests)
		}
	})
	t.Run("a pin", func(t *testing.T) {
		te := newTurnTestEnvAt(t, "http://127.0.0.1:1", "-notebook", t.TempDir(), "-context-window", "8192")
		question := startAtAnswered(t, te)
		loop, _, _ := newTestLoop(t, te)
		if err := loop.prepareStart(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(question); err != nil {
			t.Fatal(err)
		}
		if action := loop.interpretInput("/pin keep this"); action.send || loop.failedTurns != 0 || !loop.saved {
			t.Fatalf("the pin: %+v, %d failed turns, saved %t", action, loop.failedTurns, loop.saved)
		}
	})
}

// A loop that saved nothing names no saved position as it ends.
func TestREPLThatSavedNothingNamesNoPosition(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	te := newTurnTestEnv(t, server)
	startAtAnswered(t, te)
	loop, w, notes := newTestLoop(t, te)
	typeTurns(t, w, "", true)
	if err := loop.run(); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if strings.Contains(strings.Join(notes.all(), "\n"), "Saved in session") {
		t.Fatalf("notes = %q", notes.all())
	}
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
	defer func(oldStdin, oldStdout *os.File, oldArgs []string) {
		os.Stdin, os.Stdout, os.Args = oldStdin, oldStdout, oldArgs
		logger.ResetForTesting()
	}(os.Stdin, os.Stdout, os.Args)
	os.Stdin, os.Stdout = stdin, stdout
	os.Args = append([]string{"lmc"}, args...)
	return run(core.NewTestNotifier())
}

// Resumed at a question, an empty first line of a piped loop asks for its
// answer without the -image attachment, which the typed prompt that follows
// carries. A single run with the same input sends the image with no text.
func TestREPLAnswerLeavesTheImagesForThePrompt(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	sessionsDir := t.TempDir()
	sess := newTestSession(t, session.Open(sessionsDir), textEntry(core.RoleUser, "the question"), textEntry(core.RoleAssistant, "the old answer"))
	image := filepath.Join(t.TempDir(), "picture.png")
	if err := os.WriteFile(image, replPNG, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	defer r.Close()
	if _, err := w.WriteString("\nwhat is in the picture?\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = w.Close()

	args := []string{
		"-provider", "openai", "-provider-url", server.URL() + "/v1", "-chat-completions", "-model", "gpt-test",
		"-log-dir", t.TempDir(), "-sessions-dir", sessionsDir,
		"-resume", sess.Ref().At("0001").String(), "-image", image,
	}
	if err := runWithArgs(t, r, append([]string{"-repl", "-no-stream"}, args...)...); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	requests := server.GetRequests()
	if len(requests) != 2 {
		t.Fatalf("%d requests, want the answer's and the prompt's", len(requests))
	}
	if strings.Contains(requests[0].Body, "data:image/png") || strings.Contains(requests[0].Body, "the old answer") {
		t.Fatalf("the answer's request = %s, want the question alone", requests[0].Body)
	}
	for _, want := range []string{"answer 1", "what is in the picture?", "data:image/png"} {
		if !strings.Contains(requests[1].Body, want) {
			t.Fatalf("the prompt's request lacks %q: %s", want, requests[1].Body)
		}
	}
}

func TestREPLInteractiveFailureStillAllowsCleanExit(t *testing.T) {
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		return nil, http.StatusBadRequest, errors.New("provider refused")
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server)
	loop, w, _ := newTestLoop(t, te)
	loop.terminal = true
	typeTurns(t, w, "first\n/exit\n", true)
	if err := loop.run(); err != nil {
		t.Fatalf("interactive exit: %v", err)
	}
	if loop.failedTurns != 1 {
		t.Fatalf("failedTurns=%d", loop.failedTurns)
	}
}
