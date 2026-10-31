package main

import (
	"fmt"
	"io"
	"lmtools/internal/core"
	"lmtools/internal/retry"
	"strings"
	"sync"
	"time"
)

// The delays are variables so tests can shorten them.
var (
	// waitingStatusDelay is how long a request may go unanswered before the
	// status says so.
	waitingStatusDelay = 500 * time.Millisecond
	// quietStatusDelay is how long a started response may stay silent before
	// the status says what it is doing.
	quietStatusDelay = time.Second
	// toolCallStatusDelay is shorter: a tool call's arguments never show, so
	// any wait for them is a silence.
	toolCallStatusDelay = 300 * time.Millisecond
	// statusTick is how often a shown status updates its elapsed time.
	statusTick = time.Second
)

// progressPhase is what the turn is doing, as far as the operator can tell.
type progressPhase int

const (
	phaseIdle progressPhase = iota
	phaseWaiting
	phaseThinking
	phaseAnswer
	phaseToolCall
	phaseRunning
)

// turnProgress keeps one status line describing the turn: waiting for the
// provider, thinking, writing a tool call, or running tools, with the time
// spent so far. It has no input reader and draws only where it cannot mix with
// output: inline at the start of an empty line, cleared by the next write. A
// basic terminal gets the waiting and retry notices as plain lines and
// nothing that updates. Stopping serializes with the timer, so no stale status
// can appear after an answer.
type turnProgress struct {
	mu     sync.Mutex
	timer  *time.Timer
	epoch  uint64
	screen *replScreen
	phase  progressPhase
	since  time.Time
	detail string
	// attempt is when the current request was sent; the usage line reports
	// the time from there to the response's end.
	attempt time.Time
	// waitingShown records the plain waiting line of a basic terminal, which
	// appears once per request and only before the response starts.
	waitingShown bool
	now          func() time.Time
}

func (p *turnProgress) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// report hears the retry client: a request attempt starting, or a retry
// waiting out its delay.
func (p *turnProgress) report(event retry.Progress) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cancelLocked()
	if event.Delay > 0 {
		p.phase = phaseIdle
		status := fmt.Sprintf("Retrying in %s (attempt %d/%d)", event.Delay.Round(time.Millisecond), event.Attempt, event.Total)
		if event.Reason != "" {
			// A stream's error message comes from the provider; the
			// screen spells out any hidden character in it, and an inline
			// status shortens it to the line.
			status += " after "
			status += p.screen.fitStatusPart(core.VisibleText(event.Reason), status, "...")
		}
		p.screen.setStatus(status + "...")
		return
	}
	p.screen.setStatus("")
	p.attempt = p.clock()
	p.waitingShown = false
	p.enterLocked(phaseWaiting, "", p.attempt, waitingStatusDelay)
}

// streamEvent follows a streamed response. Output clears the status as it is
// written; a phase that writes nothing of its own gets its status after a
// quiet spell.
func (p *turnProgress) streamEvent(event core.ResponseStreamEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// A plain line printed once the response streams could land mid-line.
	p.waitingShown = true
	switch event.Type {
	case core.ResponseStreamReasoningStart:
		p.screen.setStatus("")
		p.enterLocked(phaseThinking, "", p.clock(), quietStatusDelay)
	case core.ResponseStreamReasoningDelta:
		// Thinking goes on: the time counts from when it began.
		since := p.clock()
		if p.phase == phaseThinking {
			since = p.since
		}
		p.enterLocked(phaseThinking, "", since, quietStatusDelay)
	case core.ResponseStreamReasoningEnd:
		// Waiting again, for the answer: the time counts from the request.
		p.screen.setStatus("")
		p.enterLocked(phaseWaiting, "", p.attempt, quietStatusDelay)
	case core.ResponseStreamTextDelta:
		p.cancelLocked()
		if p.phase != phaseAnswer {
			// Answers written to a redirected stdout never pass the
			// terminal, so nothing else would clear the status.
			p.screen.setStatus("")
		}
		p.phase = phaseAnswer
	case core.ResponseStreamToolCallStart:
		p.screen.setStatus("")
		p.enterLocked(phaseToolCall, core.DisplayName(event.Text), p.clock(), toolCallStatusDelay)
	}
}

// responseComplete ends the response's status before anything reacts to it.
func (p *turnProgress) responseComplete() { p.stop() }

// running shows the tool calls a round is executing.
func (p *turnProgress) running(names []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.screen.setStatus("")
	p.enterLocked(phaseRunning, strings.Join(names, ", "), p.clock(), waitingStatusDelay)
}

// stillRunning narrows the running status to the calls not yet finished,
// keeping the time already shown.
func (p *turnProgress) stillRunning(names []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.phase != phaseRunning {
		return
	}
	p.detail = strings.Join(names, ", ")
	if len(names) == 0 {
		p.cancelLocked()
		p.phase = phaseIdle
		p.screen.setStatus("")
	}
}

func (p *turnProgress) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cancelLocked()
	p.phase = phaseIdle
	p.screen.setStatus("")
}

// elapsed is the time since the current request was sent, zero when none
// was.
func (p *turnProgress) elapsed() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.attempt.IsZero() {
		return 0
	}
	return p.clock().Sub(p.attempt)
}

func (p *turnProgress) cancelLocked() {
	p.epoch++
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
}

// enterLocked starts a phase whose time counts from since; its status shows
// after a quiet spell of delay, unless something happens first.
func (p *turnProgress) enterLocked(phase progressPhase, detail string, since time.Time, delay time.Duration) {
	p.cancelLocked()
	p.phase, p.detail, p.since = phase, detail, since
	epoch := p.epoch
	p.timer = time.AfterFunc(delay, func() { p.tick(epoch) })
}

func (p *turnProgress) tick(epoch uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.epoch != epoch || p.phase == phaseIdle || p.phase == phaseAnswer {
		return
	}
	if !p.screen.inlineStatus {
		if p.phase == phaseWaiting && !p.waitingShown {
			p.waitingShown = true
			p.screen.setStatus("Waiting for response...")
		}
		return
	}
	p.screen.showStatus(p.statusLocked())
	p.timer = time.AfterFunc(statusTick, func() { p.tick(epoch) })
}

func (p *turnProgress) statusLocked() string {
	var text string
	switch p.phase {
	case phaseWaiting:
		text = "Waiting for response..."
	case phaseThinking:
		text = "Thinking..."
	case phaseToolCall:
		text = "Writing a tool call..."
		if p.detail != "" {
			text = "Writing tool call " + p.detail + "..."
		}
	case phaseRunning:
		text = "Running " + p.detail + "..."
	}
	if seconds := formatStatusElapsed(p.clock().Sub(p.since)); seconds != "" {
		text += " " + seconds
	}
	return text
}

// formatStatusElapsed counts whole seconds, and nothing under one.
func formatStatusElapsed(d time.Duration) string {
	seconds := int(d / time.Second)
	switch {
	case seconds < 1:
		return ""
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
}

func (s *replScreen) setStatus(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statusOut == nil {
		return
	}
	if !s.inlineStatus {
		if text != "" {
			_, _ = fmt.Fprintln(s.statusOut, core.VisibleText(text))
			s.answerLast, s.newlines = false, 1
		}
		return
	}
	s.clearStatusLocked()
	s.drawStatusLocked(text)
}

// showStatus draws text where it cannot touch output: on an empty line, or in
// place of a status already shown. Mid-line it waits for a later tick.
func (s *replScreen) showStatus(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statusOut == nil || !s.inlineStatus {
		return
	}
	if len(s.status.widths) == 0 && s.wrote && s.newlines == 0 {
		return
	}
	s.clearStatusLocked()
	s.drawStatusLocked(text)
}

// drawStatusLocked draws text with every hidden character spelled out: the
// names in a status come from the model.
func (s *replScreen) drawStatusLocked(text string) {
	columns := 80
	if s.columns != nil {
		columns = s.columns()
	}
	text = clipStatus(core.VisibleText(text), max(0, columns-1))
	if text == "" {
		return
	}
	width := statusCells(text)
	_, _ = io.WriteString(s.statusOut, text)
	s.status = terminalArea{widths: []int{width}, cursorCol: width}
}

// fitStatusPart shortens part, shown in an inline status between before and
// after, to the cells the line leaves it; a status printed as a line of its
// own keeps all of it.
func (s *replScreen) fitStatusPart(part, before, after string) string {
	s.mu.Lock()
	inline, columns := s.inlineStatus, 80
	if s.columns != nil {
		columns = s.columns()
	}
	s.mu.Unlock()
	if !inline {
		return part
	}
	return fitReason(part, columns-1-statusCells(before)-statusCells(after))
}

// fitReason shortens a provider's message to cells terminal cells when it
// does not fit: to its first sentence, which says what failed, and then by
// whole words from the middle, since such a sentence tends to say where at
// its start and what went wrong at its end.
func fitReason(text string, cells int) string {
	if statusCells(text) <= cells {
		return text
	}
	if end := strings.Index(text, ". "); end > 0 {
		text = text[:end+1]
		if statusCells(text) <= cells {
			return text
		}
	}
	// Each word kept takes its cells and one space beside it; words come
	// from the end and the start in turn, the end first.
	words := strings.Fields(text)
	var head, tail []string
	used := len("...")
	for i, j, fromEnd := 0, len(words)-1, true; i <= j; fromEnd = !fromEnd {
		k := i
		if fromEnd {
			k = j
		}
		if used += statusCells(words[k]) + 1; used > cells {
			break
		}
		if fromEnd {
			tail = append([]string{words[k]}, tail...)
			j--
		} else {
			head = append(head, words[k])
			i++
		}
	}
	return strings.Join(append(append(head, "..."), tail...), " ")
}

// statusCells counts the terminal cells text takes on a status line.
func statusCells(text string) int {
	cells := 0
	for _, r := range text {
		cells += editRuneWidth(r)
	}
	return cells
}

func (s *replScreen) clearStatusLocked() {
	if len(s.status.widths) == 0 {
		return
	}
	columns := 80
	if s.columns != nil {
		columns = s.columns()
	}
	s.status.clear(s.statusOut, columns)
}

// clipStatus cuts text to at most cells terminal cells, on a rune boundary.
func clipStatus(text string, cells int) string {
	used := 0
	for i, r := range text {
		if used += editRuneWidth(r); used > cells {
			return text[:i]
		}
	}
	return text
}
