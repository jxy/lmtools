package main

import (
	"bytes"
	"io"
	"lmtools/internal/core"
	"lmtools/internal/retry"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is read by the test while the progress timer writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func shortenStatusDelays(t *testing.T) {
	t.Helper()
	saved := []time.Duration{waitingStatusDelay, quietStatusDelay, toolCallStatusDelay, statusTick}
	waitingStatusDelay, quietStatusDelay, toolCallStatusDelay, statusTick = 5*time.Millisecond, 5*time.Millisecond, 5*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() {
		waitingStatusDelay, quietStatusDelay, toolCallStatusDelay, statusTick = saved[0], saved[1], saved[2], saved[3]
	})
}

// fakeClock advances only when the test says so.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func waitForOutput(t *testing.T, out *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), want) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("status %q never appeared in %q", want, out.String())
}

// statusShown reports whether a status line is on the screen.
func statusShown(screen *replScreen) bool {
	screen.mu.Lock()
	defer screen.mu.Unlock()
	return len(screen.status.widths) > 0
}

func newTestProgress(out io.Writer) (*turnProgress, *replScreen, *fakeClock) {
	screen := &replScreen{statusOut: out, inlineStatus: true, columns: func() int { return 80 }}
	clock := &fakeClock{now: time.Unix(1000, 0)}
	return &turnProgress{screen: screen, now: clock.Now}, screen, clock
}

// holdStatusTimers keeps the status timers from firing on their own, so a
// test decides when a tick happens with tickNow.
func holdStatusTimers(t *testing.T) {
	t.Helper()
	saved := []time.Duration{waitingStatusDelay, quietStatusDelay, toolCallStatusDelay, statusTick}
	waitingStatusDelay, quietStatusDelay, toolCallStatusDelay, statusTick = time.Hour, time.Hour, time.Hour, time.Hour
	t.Cleanup(func() {
		waitingStatusDelay, quietStatusDelay, toolCallStatusDelay, statusTick = saved[0], saved[1], saved[2], saved[3]
	})
}

// tickNow runs the pending status tick at once, as if its delay had passed.
func tickNow(p *turnProgress) {
	p.mu.Lock()
	epoch := p.epoch
	p.mu.Unlock()
	p.tick(epoch)
}

// A thinking model can be silent for seconds after its first paragraph; the
// status says it is thinking and for how long, on an empty line only.
func TestProgressExplainsThinkingSilenceAtLineStart(t *testing.T) {
	holdStatusTimers(t)
	out := &syncBuffer{}
	p, screen, clock := newTestProgress(out)
	diag := screen.writer(out, false)
	p.report(retry.Progress{Attempt: 1, Total: 1})
	_, _ = io.WriteString(diag, "--- thinking ---\n")
	p.streamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningStart})
	clock.advance(3 * time.Second)
	tickNow(p)
	if !strings.HasSuffix(out.String(), "--- thinking ---\nThinking... 3s") {
		t.Fatalf("no thinking status: %q", out.String())
	}

	// Text clears the status as it is written, and a line left open keeps
	// the status away until the line ends.
	p.streamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningDelta, Text: "Halfway"})
	_, _ = io.WriteString(diag, "| Halfway")
	before := out.String()
	tickNow(p)
	if after := out.String(); after != before {
		t.Fatalf("status drawn mid-line: %q", strings.TrimPrefix(after, before))
	}
	if !strings.Contains(before, "Thinking... 3s\r\x1b[J| Halfway") {
		t.Fatalf("status was not cleared before output: %q", before)
	}
	p.stop()
}

func TestProgressNamesToolCallsAndRunningCommands(t *testing.T) {
	shortenStatusDelays(t)
	out := &syncBuffer{}
	p, screen, clock := newTestProgress(out)
	_, _ = io.WriteString(screen.writer(out, false), "Assistant:\nI'll list the files.\n\n")
	p.streamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamToolCallStart, Text: "universal_command"})
	waitForOutput(t, out, "Writing tool call universal_command...")
	p.responseComplete()

	p.running([]string{"find", "grep"})
	clock.advance(2 * time.Second)
	waitForOutput(t, out, "Running find, grep... 2s")
	p.stillRunning([]string{"grep"})
	clock.advance(time.Second)
	waitForOutput(t, out, "Running grep... 3s")
	p.stillRunning(nil)
	if statusShown(screen) {
		t.Fatal("status left after the last call")
	}
	p.stop()
}

func TestProgressReportsElapsedSinceTheRequest(t *testing.T) {
	_, _, clock := newTestProgress(io.Discard)
	p := &turnProgress{screen: &replScreen{}, now: clock.Now}
	if p.elapsed() != 0 {
		t.Fatal("elapsed before any request")
	}
	p.report(retry.Progress{Attempt: 1, Total: 1})
	clock.advance(5600 * time.Millisecond)
	if got := p.elapsed(); got != 5600*time.Millisecond {
		t.Fatalf("elapsed = %v", got)
	}
	p.stop()
}

func TestFormatStatusElapsed(t *testing.T) {
	for d, want := range map[time.Duration]string{
		900 * time.Millisecond: "",
		time.Second:            "1s",
		59 * time.Second:       "59s",
		65 * time.Second:       "1m05s",
	} {
		if got := formatStatusElapsed(d); got != want {
			t.Fatalf("formatStatusElapsed(%v) = %q, want %q", d, got, want)
		}
	}
	if got := clipStatus("Running 界界...", 10); got != "Running 界" {
		t.Fatalf("clipStatus = %q", got)
	}
}

// With stdout redirected, the answer never reaches the terminal to clear a
// status shown there; its first text clears it.
func TestProgressClearsTheStatusWhenTheAnswerStarts(t *testing.T) {
	shortenStatusDelays(t)
	out := &syncBuffer{}
	p, screen, clock := newTestProgress(out)
	p.report(retry.Progress{Attempt: 1, Total: 1})
	clock.advance(2 * time.Second)
	waitForOutput(t, out, "Waiting for response... 2s")
	p.streamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamTextDelta, Text: "Hello"})
	if statusShown(screen) {
		t.Fatal("status left on the terminal while the answer streams elsewhere")
	}
	p.stop()
}

// A basic terminal gets one plain waiting line, and only before anything of
// the response has arrived: once it streams, a line could land mid-answer.
func TestProgressPrintsPlainWaitingOnlyBeforeTheResponse(t *testing.T) {
	holdStatusTimers(t)
	out := &syncBuffer{}
	p := &turnProgress{screen: &replScreen{statusOut: out}}
	p.report(retry.Progress{Attempt: 1, Total: 1})
	p.streamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningStart})
	p.streamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningEnd})
	tickNow(p)
	if got := out.String(); got != "" {
		t.Fatalf("a basic terminal was told %q after the response started", got)
	}
	p.stop()

	p.report(retry.Progress{Attempt: 1, Total: 1})
	tickNow(p)
	if got := out.String(); got != "Waiting for response...\n" {
		t.Fatalf("waiting line = %q", got)
	}
	p.stop()
}

// The running status drops a call as soon as it ends, though its result waits
// for a slower call requested before it.
func TestREPLToolUIDropsAFinishedCallFromTheStatus(t *testing.T) {
	shortenStatusDelays(t)
	out := &syncBuffer{}
	progress, _, clock := newTestProgress(out)
	ui := &replToolUI{loop: &replLoop{env: &turnEnv{progress: progress}}}
	command := func(argv string) core.ToolCall {
		return core.ToolCall{Name: core.UniversalCommandToolName, Args: []byte(`{"command":` + argv + `}`)}
	}
	ui.CallStarted(0, command(`["sleep","9"]`))
	ui.CallStarted(1, command(`["echo","x"]`))
	clock.advance(time.Second)
	waitForOutput(t, out, "Running sleep, echo... 1s")
	ui.CallFinished(1)
	clock.advance(time.Second)
	waitForOutput(t, out, "Running sleep... 2s")
	progress.stop()
}

// A program name is the model's text; an escape sequence in it reaches the
// terminal spelled out, never as a control.
func TestProgressStatusSpellsOutControlsInNames(t *testing.T) {
	shortenStatusDelays(t)
	out := &syncBuffer{}
	progress, _, clock := newTestProgress(out)
	ui := &replToolUI{loop: &replLoop{env: &turnEnv{progress: progress}}}
	ui.CallStarted(0, core.ToolCall{Name: core.UniversalCommandToolName, Args: []byte(`{"command":["\u001b]0;owned\u0007sh"]}`)})
	clock.advance(time.Second)
	waitForOutput(t, out, `Running "\u001b]0;owned\u0007sh"... 1s`)
	progress.stop()
	if strings.Contains(out.String(), "\x1b]0") || strings.Contains(out.String(), "\a") {
		t.Fatalf("a control reached the terminal: %q", out.String())
	}
}

// recordedProgress keeps the stream events a presenter passes on.
type recordedProgress struct {
	events []core.ResponseStreamEventType
}

func (r *recordedProgress) streamEvent(event core.ResponseStreamEvent) {
	r.events = append(r.events, event.Type)
}
func (r *recordedProgress) responseComplete() {}

// Shown thinking is output, so each delta restarts the quiet spell before a
// status; hidden thinking draws nothing, so its deltas are not passed on.
func TestResponsePresenterPassesOnlyShownThinkingDeltas(t *testing.T) {
	for _, shown := range []bool{false, true} {
		p := newResponsePresenter(io.Discard, io.Discard, shown)
		progress := &recordedProgress{}
		p.progress = progress
		for _, event := range []core.ResponseStreamEvent{
			{Type: core.ResponseStreamReasoningStart},
			{Type: core.ResponseStreamReasoningDelta, Text: "step"},
			{Type: core.ResponseStreamReasoningEnd},
		} {
			p.HandleStreamEvent(event)
		}
		deltas := 0
		for _, event := range progress.events {
			if event == core.ResponseStreamReasoningDelta {
				deltas++
			}
		}
		if want := map[bool]int{false: 0, true: 1}[shown]; deltas != want || len(progress.events) != 2+want {
			t.Fatalf("thinking shown %v: progress heard %v", shown, progress.events)
		}
	}
}

// With thinking hidden, reasoning deltas that arrive faster than the quiet
// spell leave the status showing and counting.
func TestHiddenThinkingKeepsTheStatusCounting(t *testing.T) {
	shortenStatusDelays(t)
	quietStatusDelay = 50 * time.Millisecond
	out := &syncBuffer{}
	progress, _, clock := newTestProgress(out)
	p := newResponsePresenter(io.Discard, io.Discard, false)
	p.progress = progress
	p.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningStart})
	streamUntil := func(want string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for !strings.Contains(out.String(), want) && time.Now().Before(deadline) {
			p.HandleStreamEvent(core.ResponseStreamEvent{Type: core.ResponseStreamReasoningDelta, Text: "x"})
			time.Sleep(5 * time.Millisecond)
		}
		if !strings.Contains(out.String(), want) {
			t.Fatalf("%q never appeared while hidden thinking streamed: %q", want, out.String())
		}
	}
	streamUntil("Thinking...")
	clock.advance(3 * time.Second)
	streamUntil("Thinking... 3s")
	progress.stop()
}
