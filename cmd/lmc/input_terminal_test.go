//go:build darwin || freebsd || linux

package main

import (
	"context"
	"errors"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/ptytest"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// terminalOwner opens a pseudo terminal and an input owner on its slave, the
// way lmc owns a terminal on its standard input. The screen keeps the master
// drained.
func terminalOwner(t *testing.T) (*inputOwner, *ptytest.PTY, *ptytest.Screen) {
	t.Helper()
	pty, err := ptytest.Open()
	if err != nil {
		t.Fatalf("ptytest.Open() error = %v", err)
	}
	screen := pty.Screen()
	owner := newInputOwner(pty.Slave)
	if owner.term == nil {
		_ = pty.Close()
		t.Fatalf("the owner did not open %s: %v", pty.Name, owner.termErr)
	}
	t.Cleanup(func() {
		_ = owner.close()
		_ = pty.Close()
	})
	return owner, pty, screen
}

// typeEchoed types text and waits for the terminal to echo it, by which time
// the terminal has taken it in.
func typeEchoed(t *testing.T, pty *ptytest.PTY, screen *ptytest.Screen, text string) {
	t.Helper()
	if err := pty.Type(text); err != nil {
		t.Fatalf("Type(%q) error = %v", text, err)
	}
	if err := screen.Expect(strings.ReplaceAll(text, "\n", "\r\n"), 10*time.Second); err != nil {
		t.Fatal(err)
	}
}

func typeKeys(t *testing.T, pty *ptytest.PTY, keys string) {
	t.Helper()
	if err := pty.Type(keys); err != nil {
		t.Fatalf("Type(%q) error = %v", keys, err)
	}
}

// questionNotifier records what an approver shows, and tells the test each
// time a question appears.
type questionNotifier struct {
	mu       sync.Mutex
	messages []string
	shown    chan struct{}
}

func newQuestionNotifier() *questionNotifier {
	return &questionNotifier{shown: make(chan struct{}, 8)}
}

func (n *questionNotifier) record(format string, args ...interface{}) string {
	message := fmt.Sprintf(format, args...)
	n.mu.Lock()
	n.messages = append(n.messages, message)
	n.mu.Unlock()
	return message
}

func (n *questionNotifier) Infof(format string, args ...interface{})  { n.record(format, args...) }
func (n *questionNotifier) Warnf(format string, args ...interface{})  { n.record(format, args...) }
func (n *questionNotifier) Errorf(format string, args ...interface{}) { n.record(format, args...) }

func (n *questionNotifier) Promptf(format string, args ...interface{}) {
	if strings.HasSuffix(n.record(format, args...), "[y/N]: ") {
		n.shown <- struct{}{}
	}
}

func (n *questionNotifier) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.messages...)
}

func (n *questionNotifier) said(want string) bool {
	for _, message := range n.all() {
		if strings.Contains(message, want) {
			return true
		}
	}
	return false
}

type approval struct {
	approved bool
	err      error
}

// askApproval asks the approval question on the owner's terminal and
// returns once the question has appeared, with a channel for the outcome.
func askApproval(t *testing.T, ctx context.Context, owner *inputOwner, notifier *questionNotifier) <-chan approval {
	t.Helper()
	approver := &cliApprover{notifier: notifier, answers: owner}
	outcome := make(chan approval, 1)
	go func() {
		approved, err := approver.Approve(ctx, core.UniversalCommandArgs{})
		outcome <- approval{approved: approved, err: err}
	}()
	select {
	case <-notifier.shown:
	case result := <-outcome:
		t.Fatalf("Approve() returned %+v before the question appeared", result)
	case <-time.After(10 * time.Second):
		t.Fatal("the question did not appear")
	}
	return outcome
}

func awaitApproval(t *testing.T, outcome <-chan approval) approval {
	t.Helper()
	select {
	case result := <-outcome:
		return result
	case <-time.After(10 * time.Second):
		t.Fatal("Approve() did not return")
		return approval{}
	}
}

// Input typed before a question appears cannot answer it. The owner drops
// what the terminal holds, then shows the question: an unfinished line goes
// without a word, and a complete line with a note above the question.
func TestTerminalQuestionDropsInputTypedBeforeIt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		before   string
		answer   string
		approved bool
		noted    bool
	}{
		{name: "y without Enter, then Enter", before: "y", answer: "\n"},
		{name: "a line typed during generation, then no", before: "yes\n", answer: "n\n", noted: true},
		{name: "a line typed during generation, then yes", before: "no\n", answer: "yes\n", approved: true, noted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, pty, screen := terminalOwner(t)
			notifier := newQuestionNotifier()
			typeEchoed(t, pty, screen, tc.before)

			outcome := askApproval(t, context.Background(), owner, notifier)
			typeKeys(t, pty, tc.answer)
			result := awaitApproval(t, outcome)
			if result.err != nil || result.approved != tc.approved {
				t.Fatalf("Approve() = %v, %v; want %v", result.approved, result.err, tc.approved)
			}
			if noted := notifier.said(discardedInputNotice); noted != tc.noted {
				t.Fatalf("discarded input noted = %v, want %v; shown %q", noted, tc.noted, notifier.all())
			}
		})
	}
}

// Ctrl-D ends an answer without a newline, and the owner keeps the text
// until the line ends: y, Ctrl-D, and Enter is the line y, and y and Ctrl-D
// twice is y and then the end of input. Both approve.
func TestTerminalAnswerEndedByCtrlD(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys string
	}{
		{name: "then Enter", keys: "y" + ptytest.EndOfInput + "\n"},
		{name: "then Ctrl-D again", keys: "y" + ptytest.EndOfInput + ptytest.EndOfInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, pty, _ := terminalOwner(t)
			outcome := askApproval(t, context.Background(), owner, newQuestionNotifier())
			typeKeys(t, pty, tc.keys)
			if result := awaitApproval(t, outcome); result.err != nil || !result.approved {
				t.Fatalf("Approve() = %v, %v; want the approval", result.approved, result.err)
			}
		})
	}
}

// A read whose buffer ends inside the answer returns part of it. The owner
// reads on to the newline, so yesplease is not taken for yes.
func TestTerminalAnswerSplitAcrossReads(t *testing.T) {
	defer func(size int) { terminalReadSize = size }(terminalReadSize)
	terminalReadSize = 3
	for _, tc := range []struct {
		answer   string
		approved bool
	}{
		{answer: "yesplease\n"},
		{answer: "yes\n", approved: true},
	} {
		t.Run(strings.TrimSpace(tc.answer), func(t *testing.T) {
			owner, pty, _ := terminalOwner(t)
			outcome := askApproval(t, context.Background(), owner, newQuestionNotifier())
			typeKeys(t, pty, tc.answer)
			if result := awaitApproval(t, outcome); result.err != nil || result.approved != tc.approved {
				t.Fatalf("Approve() = %v, %v; want %v", result.approved, result.err, tc.approved)
			}
		})
	}
}

// An answer past the bound is refused, even one that is yes once its spaces
// are trimmed, and the rest of its line is read away, so a prompt after it
// reads only the next line.
func TestTerminalAnswerPastTheBound(t *testing.T) {
	defer func(size int) { terminalReadSize = size }(terminalReadSize)
	terminalReadSize = 16
	owner, pty, _ := terminalOwner(t)
	outcome := askApproval(t, context.Background(), owner, newQuestionNotifier())
	typeKeys(t, pty, "yes"+strings.Repeat(" ", 4*maxAnswerBytes)+"\n")
	if result := awaitApproval(t, outcome); result.err != nil || result.approved {
		t.Fatalf("Approve() = %v, %v; want a denial", result.approved, result.err)
	}

	typeKeys(t, pty, "next\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	line, err := owner.readLine(ctx)
	if err != nil || line.text != "next" || line.eof || line.tooLong {
		t.Fatalf("readLine() = %+v, %v; want the next line", line, err)
	}
}

// A question cancelled while it waits ends without reading. A prompt after
// it reads the next line typed, and another question reads only what is
// typed after it appears.
func TestTerminalCancelledQuestion(t *testing.T) {
	t.Run("then a prompt", func(t *testing.T) {
		owner, pty, _ := terminalOwner(t)
		notifier := newQuestionNotifier()
		ctx, cancel := context.WithCancel(context.Background())
		outcome := askApproval(t, ctx, owner, notifier)
		cancel()
		if result := awaitApproval(t, outcome); !errors.Is(result.err, context.Canceled) || result.approved {
			t.Fatalf("Approve() = %v, %v; want its cancellation", result.approved, result.err)
		}
		if !notifier.said("Approval prompt cancelled.") {
			t.Fatalf("shown %q, want the cancellation notice", notifier.all())
		}

		typeKeys(t, pty, "hello\n")
		readCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if line, err := owner.readLine(readCtx); err != nil || line.text != "hello" {
			t.Fatalf("readLine() = %+v, %v; want the line typed after the cancellation", line, err)
		}
	})
	t.Run("then another question", func(t *testing.T) {
		owner, pty, screen := terminalOwner(t)
		ctx, cancel := context.WithCancel(context.Background())
		outcome := askApproval(t, ctx, owner, newQuestionNotifier())
		typeEchoed(t, pty, screen, "y")
		cancel()
		if result := awaitApproval(t, outcome); !errors.Is(result.err, context.Canceled) {
			t.Fatalf("Approve() = %v, %v; want its cancellation", result.approved, result.err)
		}

		// With the y kept, es would make yes.
		outcome = askApproval(t, context.Background(), owner, newQuestionNotifier())
		typeKeys(t, pty, "es\n")
		if result := awaitApproval(t, outcome); result.err != nil || result.approved {
			t.Fatalf("Approve() = %v, %v; want a denial of es", result.approved, result.err)
		}
	})
}

type readResult struct {
	line inputLine
	err  error
}

func readLineAsync(ctx context.Context, owner *inputOwner) <-chan readResult {
	results := make(chan readResult, 1)
	go func() {
		line, err := owner.readLine(ctx)
		results <- readResult{line: line, err: err}
	}()
	return results
}

// Readiness can be withdrawn between select and the read. A hook there
// flushes the terminal: the read finds nothing and the owner waits again,
// a cancellation still ends the request, and the next line is read.
func TestTerminalReadinessWithdrawnBeforeTheRead(t *testing.T) {
	pty, err := ptytest.Open()
	if err != nil {
		t.Fatalf("ptytest.Open() error = %v", err)
	}
	defer pty.Close()
	screen := pty.Screen()
	flushed := make(chan struct{})
	var once sync.Once
	afterTerminalReadyForTest = func() {
		once.Do(func() {
			if err := flushInput(int(pty.Slave.Fd())); err != nil {
				t.Errorf("flush: %v", err)
			}
			close(flushed)
		})
	}
	owner := newInputOwner(pty.Slave)
	afterTerminalReadyForTest = nil
	if owner.term == nil {
		t.Fatalf("the owner did not open %s: %v", pty.Name, owner.termErr)
	}
	defer owner.close()

	ctx, cancel := context.WithCancel(context.Background())
	results := readLineAsync(ctx, owner)
	typeEchoed(t, pty, screen, "first\n")
	select {
	case <-flushed:
	case <-time.After(10 * time.Second):
		t.Fatal("the terminal never reported the line ready")
	}
	select {
	case result := <-results:
		t.Fatalf("readLine() = %+v, %v after the flush, want it waiting again", result.line, result.err)
	case <-time.After(200 * time.Millisecond):
	}
	cancel()
	select {
	case result := <-results:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("readLine() = %+v, %v; want its cancellation", result.line, result.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the cancellation did not end the request")
	}

	results = readLineAsync(context.Background(), owner)
	typeKeys(t, pty, "second\n")
	select {
	case result := <-results:
		if result.err != nil || result.line.text != "second" {
			t.Fatalf("readLine() = %+v, %v; want the next line", result.line, result.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the next line was not read")
	}
}

// A prompt reads lines in order, with nothing dropped. Ctrl-D at the start of
// a line is the end of input; after text it delivers the text without a
// newline, and Enter or a second Ctrl-D ends the line.
func TestTerminalPromptLines(t *testing.T) {
	for _, tc := range []struct {
		name  string
		keys  string
		lines []inputLine
	}{
		{name: "Ctrl-D at the start of a line", keys: ptytest.EndOfInput, lines: []inputLine{{eof: true}}},
		{name: "Ctrl-D after text, then Enter", keys: "abc" + ptytest.EndOfInput + "\n", lines: []inputLine{{text: "abc"}}},
		{name: "Ctrl-D after text, twice", keys: "abc" + ptytest.EndOfInput + ptytest.EndOfInput, lines: []inputLine{{text: "abc", eof: true}}},
		{name: "lines typed ahead", keys: "one\ntwo\n", lines: []inputLine{{text: "one"}, {text: "two"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, pty, _ := terminalOwner(t)
			typeKeys(t, pty, tc.keys)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			for i, want := range tc.lines {
				got, err := owner.readLine(ctx)
				if err != nil || got != want {
					t.Fatalf("readLine() %d = %+v, %v; want %+v", i+1, got, err, want)
				}
			}
		})
	}
}

// A single run reads its prompt up to Ctrl-D at the start of a line, and a
// cancellation, which Ctrl-C makes, ends the read.
func TestTerminalReadAll(t *testing.T) {
	t.Run("to the end of input", func(t *testing.T) {
		owner, pty, _ := terminalOwner(t)
		typeKeys(t, pty, "line one\nline two\n"+ptytest.EndOfInput)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		got, err := owner.readAll(ctx, 1024)
		if err != nil || string(got) != "line one\nline two\n" {
			t.Fatalf("readAll() = %q, %v; want both lines", got, err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		owner, pty, screen := terminalOwner(t)
		typeEchoed(t, pty, screen, "unfinished\n")
		ctx, cancel := context.WithCancel(context.Background())
		results := make(chan error, 1)
		go func() {
			_, err := owner.readAll(ctx, 1024)
			results <- err
		}()
		cancel()
		select {
		case err := <-results:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("readAll() error = %v, want its cancellation", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the cancellation did not end the read")
		}
	})
}

// The owner reads through an open of its own, which alone is nonblocking;
// the description behind standard input stays blocking.
func TestTerminalOwnerLeavesStandardInputBlocking(t *testing.T) {
	owner, pty, _ := terminalOwner(t)
	typeKeys(t, pty, "line\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := owner.readLine(ctx); err != nil {
		t.Fatalf("readLine() error = %v", err)
	}
	blocking := func(fd uintptr) bool {
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
		if errno != 0 {
			t.Fatalf("F_GETFL: %v", errno)
		}
		return flags&syscall.O_NONBLOCK == 0
	}
	if !blocking(pty.Slave.Fd()) {
		t.Fatal("standard input's description became nonblocking")
	}
	if blocking(uintptr(owner.term.(*terminalReader).fd)) {
		t.Fatal("the owner's own open is blocking")
	}
}

// A name that opens another device than standard input is refused, and the
// error names the terminal and the reason.
func TestTerminalOwnerRefusesAnotherDevice(t *testing.T) {
	pty, err := ptytest.Open()
	if err != nil {
		t.Fatalf("ptytest.Open() error = %v", err)
	}
	defer pty.Close()
	other, err := ptytest.Open()
	if err != nil {
		t.Fatalf("ptytest.Open() error = %v", err)
	}
	defer other.Close()
	defer func(reopen func(string) (int, error)) { reopenTerminal = reopen }(reopenTerminal)
	reopenTerminal = func(string) (int, error) {
		return syscall.Open(other.Name, syscall.O_RDONLY|syscall.O_NOCTTY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	}

	owner := newInputOwner(pty.Slave)
	if owner.term != nil {
		_ = owner.close()
		t.Fatalf("the owner opened %s through a name that opens %s", pty.Name, other.Name)
	}
	if !owner.terminal || owner.termErr == nil ||
		!strings.Contains(owner.termErr.Error(), pty.Name) || !strings.Contains(owner.termErr.Error(), "another device") {
		t.Fatalf("termErr = %v, want the terminal's name and the reason", owner.termErr)
	}
}

// The end of a request's context starts a write to the wake pipe. The
// request returns only once that write is done, even when the wait ends for
// another reason first, here a line arriving: the owner may close the pipe
// next, and another open could then take the descriptor the write goes to.
// The cancelled request leaves the line, and the byte left in the pipe does
// not disturb the next request.
func TestTerminalRequestWaitsForItsWake(t *testing.T) {
	pty, err := ptytest.Open()
	if err != nil {
		t.Fatalf("ptytest.Open() error = %v", err)
	}
	defer pty.Close()
	pty.Screen()
	entered := make(chan struct{})
	release := make(chan struct{})
	beforeTerminalWakeForTest = func() {
		close(entered)
		<-release
	}
	owner := newInputOwner(pty.Slave)
	beforeTerminalWakeForTest = nil
	if owner.term == nil {
		t.Fatalf("the owner did not open %s: %v", pty.Name, owner.termErr)
	}
	defer owner.close()

	ctx, cancel := context.WithCancel(context.Background())
	results := readLineAsync(ctx, owner)
	// Let the request reach its wait.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the end of the context never started the wake")
	}
	// The line ends the wait while the wake is held before its write.
	typeKeys(t, pty, "during\n")
	select {
	case result := <-results:
		t.Fatalf("readLine() returned %+v, %v while its wake was still writing", result.line, result.err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case result := <-results:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("readLine() = %+v, %v; want its cancellation", result.line, result.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("readLine() did not return after its wake finished")
	}

	next, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if line, err := owner.readLine(next); err != nil || line.text != "during" {
		t.Fatalf("readLine() = %+v, %v; want the line the cancelled request left", line, err)
	}
}

// Bytes an earlier read held back, which only a terminal outside canonical
// mode leaves, go to the next request whose context is live: a request whose
// context has ended takes none.
func TestTerminalHeldBytesSurviveAnEndedRequest(t *testing.T) {
	reader := &terminalReader{held: []byte("next\n")}
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := reader.readLine(ended, maxAnswerBytes); !errors.Is(err, context.Canceled) {
		t.Fatalf("readLine() with an ended context = %+v, %v; want its cancellation", got, err)
	}
	if got, err := reader.readLine(context.Background(), maxAnswerBytes); err != nil || got != (inputLine{text: "next"}) {
		t.Fatalf("readLine() = %+v, %v; want the held line", got, err)
	}
}
