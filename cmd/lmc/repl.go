package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/config"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/session"
	"lmtools/internal/ui"
	"lmtools/internal/ui/tools"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
)

// The prompts a loop shows when standard input is a terminal: one for a
// fresh turn, one for a line that continues it.
const (
	freshPrompt        = "> "
	continuationPrompt = ". "
)

// maxTurnBytes caps a turn and an accumulating continuation. Tests shrink
// it.
var maxTurnBytes = constants.MaxCLIInputSize

// errInterrupted is the cause a phase of the loop ends with when SIGINT
// ends it.
var errInterrupted = errors.New("interrupted")

// replSignals turns the signals a loop handles into ends of contexts, and
// stays installed across turns. SIGINT ends the phase the loop is in, a
// turn or a prompt, and the loop goes on. SIGTERM and SIGHUP end the
// process's context, which ends the phase too, and the loop stops once the
// turn has written what it must.
type replSignals struct {
	process    context.Context
	endProcess context.CancelCauseFunc
	signals    chan os.Signal
	done       chan struct{}

	mu    sync.Mutex
	phase context.CancelCauseFunc
	// phaseID numbers the phases, so a phase that ends clears only itself.
	phaseID int
	// pending is an interrupt that arrived between phases; it ends the next
	// phase as that begins.
	pending bool
}

// newREPLSignals makes the phases a loop runs in, with no handler
// installed; tests deliver the signals themselves.
func newREPLSignals() *replSignals {
	s := &replSignals{signals: make(chan os.Signal, 4), done: make(chan struct{})}
	s.process, s.endProcess = context.WithCancelCause(context.Background())
	return s
}

// watchREPLSignals installs the handler for SIGINT, SIGTERM, and SIGHUP.
func watchREPLSignals() *replSignals {
	s := newREPLSignals()
	signal.Notify(s.signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for {
			select {
			case sig := <-s.signals:
				if sig == os.Interrupt {
					s.interrupt()
					continue
				}
				s.terminate(sig)
			case <-s.done:
				return
			}
		}
	}()
	return s
}

// terminate ends the process's context, which a loop stops at.
func (s *replSignals) terminate(sig os.Signal) {
	s.endProcess(fmt.Errorf("stopped by %v: %w", sig, context.Canceled))
}

func (s *replSignals) interrupt() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != nil {
		s.phase(errInterrupted)
		return
	}
	s.pending = true
}

// begin starts a phase and returns its context, which SIGINT ends, and a
// function that ends the phase.
func (s *replSignals) begin() (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(s.process)
	s.mu.Lock()
	s.phaseID++
	id := s.phaseID
	s.phase = cancel
	if s.pending {
		s.pending = false
		cancel(errInterrupted)
	}
	s.mu.Unlock()
	return ctx, func() {
		s.mu.Lock()
		if s.phaseID == id {
			s.phase = nil
		}
		s.mu.Unlock()
		cancel(nil)
	}
}

// ended returns why the process is stopping, or nil.
func (s *replSignals) ended() error {
	if s.process.Err() == nil {
		return nil
	}
	return context.Cause(s.process)
}

func (s *replSignals) stop() {
	signal.Stop(s.signals)
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// replDiagnostics chooses where a loop shows its prompt, notes, thinking
// summaries, tool transcript, and questions: stderr, or the controlling
// terminal when standard input is a terminal and stderr is not, the way the
// approval question's stream is chosen, so a redirected stderr still shows
// the prompt.
func replDiagnostics(stdinTerminal bool) (io.Writer, func()) {
	if !stdinTerminal || isTerminalFile(os.Stderr) {
		return os.Stderr, func() {}
	}
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return os.Stderr, func() {}
	}
	return tty, func() { _ = tty.Close() }
}

// separatedAnswers writes the answers of a loop's turns to a stdout that is
// not a terminal. An answer that wrote anything ends with a newline, and an
// empty line separates it from the next answer. This is formatting for a
// reader: an answer can hold empty lines itself, so it is no record format.
type separatedAnswers struct {
	out io.Writer
	// newlines counts the newlines the output ends with, up to two.
	newlines int
	// wrote reports that the current turn wrote, and earlier that an
	// earlier turn did.
	wrote, earlier bool
}

func (a *separatedAnswers) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if !a.wrote && a.earlier && a.newlines < 2 {
		if _, err := io.WriteString(a.out, strings.Repeat("\n", 2-a.newlines)); err != nil {
			return 0, err
		}
		a.newlines = 2
	}
	a.wrote = true
	n, err := a.out.Write(p)
	for _, b := range p[:n] {
		if b != '\n' {
			a.newlines = 0
		} else if a.newlines < 2 {
			a.newlines++
		}
	}
	return n, err
}

// endTurn ends the current turn's answer with a newline, when it wrote one
// without.
func (a *separatedAnswers) endTurn() {
	if !a.wrote {
		return
	}
	if a.newlines == 0 {
		_, _ = io.WriteString(a.out, "\n")
		a.newlines = 1
	}
	a.wrote = false
	a.earlier = true
}

// replScreen follows what was shown last on a terminal that the answers on
// stdout and the diagnostic stream share, so a prompt can start below an
// answer, with an empty line between them, as the presenter keeps a note
// off an answer. The answers themselves pass unchanged.
type replScreen struct {
	mu sync.Mutex
	// answerLast reports that an answer was shown last, and newlines counts
	// the newlines what was shown last ends with, up to two.
	answerLast bool
	newlines   int
}

func (s *replScreen) writer(w io.Writer, answer bool) io.Writer {
	return screenWriter{w: w, screen: s, answer: answer}
}

// separateAnswer writes to diag the newlines that put an empty line below
// an answer shown last.
func (s *replScreen) separateAnswer(diag io.Writer) {
	s.mu.Lock()
	missing := 0
	if s.answerLast && s.newlines < 2 {
		missing = 2 - s.newlines
	}
	s.mu.Unlock()
	if missing > 0 {
		_, _ = io.WriteString(diag, strings.Repeat("\n", missing))
	}
}

type screenWriter struct {
	w      io.Writer
	screen *replScreen
	answer bool
}

func (w screenWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if n > 0 {
		s := w.screen
		s.mu.Lock()
		if s.answerLast != w.answer {
			s.answerLast, s.newlines = w.answer, 0
		}
		for _, b := range p[:n] {
			if b != '\n' {
				s.newlines = 0
			} else if s.newlines < 2 {
				s.newlines++
			}
		}
		s.mu.Unlock()
	}
	return n, err
}

// turnRead is how reading a turn ended.
type turnRead int

const (
	// readTurnText is a turn to send.
	readTurnText turnRead = iota
	// readEnd is the end of input at a fresh prompt.
	readEnd
	// readEndInsideTurn is the end of input inside an unfinished turn: a
	// continuation, or text that Ctrl-D delivered without a newline.
	readEndInsideTurn
	// readInterrupted is an interrupt at a fresh prompt.
	readInterrupted
	// readInterruptedInsideTurn is an interrupt that discarded an
	// unfinished turn.
	readInterruptedInsideTurn
)

// readTurn reads one turn under the input contract. A line ending in a
// single backslash continues the turn, and a doubled backslash there ends it
// with one literal backslash. An empty line at a fresh prompt sends nothing
// and prompts again; inside a continuation it is an empty line of the turn.
// The turn is the lines as typed, joined by newlines; one that is all
// whitespace is not sent. A turn is capped at maxTurnBytes, the size a
// single run accepts: a longer one is discarded once it ends, and the loop
// prompts again; a line longer than that keeps its tail, so the end of the
// turn is still found. Pasted text is read line by line like typed text.
// show displays a prompt, note reports a discarded turn, and consumed hears
// of every line read, sent or not.
func readTurn(ctx context.Context, input *inputOwner, show func(prompt string), note func(format string, args ...interface{}), consumed func()) (string, turnRead, error) {
	var lines []string
	// size is the length of the turn so far: its lines and the newlines
	// between them.
	size := 0
	discarding := false
	for {
		prompt := freshPrompt
		if lines != nil || discarding {
			prompt = continuationPrompt
		}
		show(prompt)
		// Room for a backslash that continues the turn and a carriage
		// return, which are not part of the turn.
		line, err := input.readLineUnder(ctx, maxTurnBytes+2)
		if err != nil {
			if errors.Is(context.Cause(ctx), errInterrupted) {
				if lines != nil || discarding {
					return "", readInterruptedInsideTurn, nil
				}
				return "", readInterrupted, nil
			}
			return "", readEnd, err
		}
		if line.eof {
			if lines != nil || discarding || line.text != "" || line.tooLong {
				return "", readEndInsideTurn, nil
			}
			return "", readEnd, nil
		}
		consumed()

		// A line too long keeps its tail, where the marker is.
		raw := line.text
		if line.tooLong {
			raw = line.tail
		}
		text, continues := continuationMarker(strings.TrimSuffix(raw, "\r"))
		grown := len(text)
		if lines != nil {
			grown += size + 1
		}
		if line.tooLong || grown > maxTurnBytes {
			discarding = true
			lines, size = nil, 0
		}
		if discarding {
			if !continues {
				note("Discarded a turn longer than %d bytes.", maxTurnBytes)
				discarding = false
			}
			continue
		}
		if lines == nil && !continues && strings.TrimSpace(text) == "" {
			continue
		}
		lines = append(lines, text)
		size = grown
		if continues {
			continue
		}
		turn := strings.Join(lines, "\n")
		lines, size = nil, 0
		if strings.TrimSpace(turn) == "" {
			continue
		}
		return turn, readTurnText, nil
	}
}

// continuationMarker reads the end of a line: a single backslash there
// continues the turn and is dropped, and a doubled one ends the turn with
// one backslash.
func continuationMarker(text string) (string, bool) {
	switch {
	case strings.HasSuffix(text, `\\`):
		return text[:len(text)-1], false
	case strings.HasSuffix(text, `\`):
		return text[:len(text)-1], true
	}
	return text, false
}

// runREPL connects the MCP servers, sets up the loop's streams, ends the
// startup phase, and holds the conversation.
func runREPL(ctx context.Context, endStartup func(), signals *replSignals, cfg *config.Config, opts core.RequestOptions, input *inputOwner, logDir string, isRegeneration bool) error {
	stream, closeDiag := replDiagnostics(input.terminal)
	defer closeDiag()
	screen := &replScreen{}
	diag := screen.writer(stream, false)
	notifier := ui.NewNotifierWithWriter(diag)

	// The servers' tools are applied to the options once; each turn takes
	// a copy of the result.
	host, err := connectMCPServers(ctx, cfg, &opts, notifier)
	if err != nil {
		return err
	}
	if host != nil {
		defer host.Close()
	}
	if err := signals.ended(); err != nil {
		return err
	}
	endStartup()

	// Everything the operator sees but the answers goes to diag: the tool
	// transcript and the questions too, which a single run sends to the
	// approval stream the same choice picks.
	env := &turnEnv{
		cfg:             cfg,
		notifier:        notifier,
		toolUI:          tools.NewCLIToolUI(notifier),
		logDir:          logDir,
		pendingToolMode: session.PendingToolExecute,
		stdout:          os.Stdout,
		stderr:          diag,
		answersOnScreen: true,
	}
	if input.terminal {
		env.approver = newApprover(notifier, input)
	}
	loop := &replLoop{
		env:      env,
		base:     opts,
		input:    input,
		signals:  signals,
		diag:     diag,
		terminal: input.terminal,
		images:   opts.Images,
	}
	if isTerminalFile(os.Stdout) {
		env.stdout = screen.writer(os.Stdout, true)
		loop.screen = screen
	} else {
		loop.answers = &separatedAnswers{out: os.Stdout}
		env.stdout = loop.answers
		env.answersOnScreen = false
	}
	return loop.run(isRegeneration)
}

// replLoop is a conversation held by one process: turns read from standard
// input until it ends, each continuing from where the previous one left
// the session.
type replLoop struct {
	env     *turnEnv
	base    core.RequestOptions
	input   *inputOwner
	signals *replSignals
	// diag is where the prompt goes, shown only when standard input is a
	// terminal.
	diag     io.Writer
	terminal bool
	// answers separates the answers on a stdout that is not a terminal; nil
	// otherwise.
	answers *separatedAnswers
	// screen follows the terminal a stdout that is a terminal shares with
	// diag; nil otherwise.
	screen *replScreen
	// from is the session the first commit established, holding the head
	// the last turn left; nil until then, while the options still name the
	// starting point.
	from *session.Session
	// images are the -image attachments, kept until a turn's user message
	// carries them into the session.
	images []core.ImageBlock
	// armed reports that the last thing that happened was an interrupt, so
	// another at an idle prompt ends the loop.
	armed bool
}

// run holds the conversation: a regeneration first, when -branch names an
// assistant message, or the pending tool calls of a resumed session; then a
// turn for every line read, until the end of input or a second interrupt at
// an idle prompt. It returns an error when the loop cannot go on.
func (l *replLoop) run(isRegeneration bool) error {
	if isRegeneration {
		out, err := l.runTurn(turnInput{isRegeneration: true}, nil)
		if err != nil && out.Session == nil {
			// Nothing a prompt could send fits a regeneration, so a
			// regeneration that saved nothing ends the loop as it ends a
			// single run.
			return err
		}
		if stop := l.finishTurn(out, err); stop != nil {
			return stop
		}
	} else if err := l.recoverPendingTools(); err != nil {
		return err
	}

	for {
		if err := l.signals.ended(); err != nil {
			return err
		}
		text, how, err := l.readTurn()
		if err != nil {
			if ended := l.signals.ended(); ended != nil {
				return ended
			}
			return err
		}
		switch how {
		case readEnd:
			l.closePromptLine()
			return nil
		case readEndInsideTurn:
			l.closePromptLine()
			l.env.notifier.Infof("Input ended inside an unfinished turn, which was not sent.")
			return nil
		case readInterruptedInsideTurn:
			l.closePromptLine()
			l.env.notifier.Infof("Discarded the unfinished turn.")
			l.armed = false
			continue
		case readInterrupted:
			if err := l.signals.ended(); err != nil {
				return err
			}
			l.closePromptLine()
			if l.armed {
				return nil
			}
			l.armed = true
			l.env.notifier.Infof("Press Ctrl-C again at the prompt, or Ctrl-D, to exit.")
			continue
		}
		l.armed = false
		out, err := l.runTurn(turnInput{text: text}, l.images)
		if stop := l.finishTurn(out, err); stop != nil {
			return stop
		}
	}
}

func (l *replLoop) readTurn() (string, turnRead, error) {
	ctx, end := l.signals.begin()
	defer end()
	// Every line read disarms an interrupt, whether it is sent or not.
	return readTurn(ctx, l.input, l.showPrompt, l.env.notifier.Infof, func() { l.armed = false })
}

func (l *replLoop) showPrompt(prompt string) {
	if !l.terminal {
		return
	}
	if l.screen != nil {
		l.screen.separateAnswer(l.diag)
	}
	_, _ = io.WriteString(l.diag, prompt)
}

// closePromptLine ends the line a prompt left open, where an interrupt or
// the end of input found it: the terminal echoed ^C there, or nothing.
func (l *replLoop) closePromptLine() {
	if l.terminal {
		_, _ = io.WriteString(l.diag, "\n")
	}
}

// recoverPendingTools resolves the tool calls a resumed session left
// pending before the first prompt, the way a single run with empty input
// does, and sends their results. A failure is reported and leaves the
// session for the first prompt to resolve again.
func (l *replLoop) recoverPendingTools() error {
	if !session.IsSessionResume(l.base.Resume) {
		return nil
	}
	ctx, end := l.signals.begin()
	sess, err := session.OpenSession(ctx, l.base.Resume)
	var calls []core.ToolCall
	if err == nil {
		calls, err = session.CheckForPendingToolCalls(ctx, sess.Path)
	}
	end()
	if err != nil || len(calls) == 0 {
		// The first turn resolves the session itself and reports what fails.
		return l.signals.ended()
	}
	out, err := l.runTurn(turnInput{}, nil)
	return l.finishTurn(out, err)
}

// runTurn runs one turn in a phase of its own, continuing from the session
// the loop holds once there is one, and records what the turn left: the
// session to continue from, and whether the images went with it.
func (l *replLoop) runTurn(in turnInput, images []core.ImageBlock) (turnOutcome, error) {
	opts := l.base
	opts.Images = images
	if l.from != nil {
		from := *l.from
		from.ConflictForks = nil
		in.from = &from
		opts.Resume, opts.Branch = "", ""
	}
	ctx, end := l.signals.begin()
	out, err := runTurn(ctx, l.env, opts, in)
	if err != nil && errors.Is(context.Cause(ctx), errInterrupted) {
		err = fmt.Errorf("%w: %w", errInterrupted, err)
	}
	end()
	if l.answers != nil {
		l.answers.endTurn()
	}
	if out.Session != nil {
		l.from = out.Session
	}
	if out.PlanCommitted && len(images) > 0 {
		l.images = nil
	}
	return out, err
}

// finishTurn reports how a turn ended and returns an error when the loop
// must stop: the process is stopping, or the turn showed an answer the
// session does not hold, which the next turn would continue without.
func (l *replLoop) finishTurn(out turnOutcome, err error) error {
	switch {
	case err == nil:
	case errors.Is(err, errInterrupted):
		// The terminal echoed ^C where the interrupt found the cursor.
		l.closePromptLine()
		l.armed = true
		l.env.notifier.Infof("Turn cancelled; %s.", describeSaved(out))
	case l.signals.ended() != nil:
		l.closePromptLine()
		l.env.notifier.Infof("Turn stopped; %s.", describeSaved(out))
	default:
		l.env.notifier.Errorf("%v", err)
		if out.Session != nil {
			l.env.notifier.Infof("The turn failed after it saved to the session; %s.", describeSaved(out))
		}
	}
	if out.UnsavedAnswer != nil {
		return fmt.Errorf("stopping, since the next turn would continue from a session without the answer shown: %w", out.UnsavedAnswer)
	}
	return l.signals.ended()
}

// describeSaved says what a turn left in the session.
func describeSaved(out turnOutcome) string {
	if out.Session == nil {
		return "nothing was saved"
	}
	if head := out.Session.Head; head != nil && head.ID != "" {
		return fmt.Sprintf("the session holds what it saved, through message %s/%s", session.GetSessionID(head.Path), head.ID)
	}
	return fmt.Sprintf("the session holds what it saved, in %s", session.GetSessionID(out.Session.Path))
}
