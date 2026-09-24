package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"lmtools/internal/constants"
	"lmtools/internal/limitio"
	"os"
	"strings"
	"sync"
)

// maxAnswerBytes bounds the answer to a question. The longest answer that
// approves is "yes"; a longer line is refused as a denial, and the rest of
// it is read away, so it cannot answer a later question or become a prompt.
const maxAnswerBytes = 64

// inputLine is one line read from standard input.
type inputLine struct {
	// text is the line without its newline, or, when input ended instead,
	// what came before the end, possibly nothing.
	text string
	// eof reports that input ended before a newline.
	eof bool
	// tooLong reports that the line passed the bound it was read under.
	// text is then empty, and the whole line has been read.
	tooLong bool
}

// lineBuilder gathers the pieces of one line under a bound.
type lineBuilder struct {
	text    []byte
	bound   int
	tooLong bool
}

func (b *lineBuilder) add(part []byte) {
	if b.tooLong {
		return
	}
	if len(b.text)+len(part) > b.bound {
		b.tooLong = true
		b.text = nil
		return
	}
	b.text = append(b.text, part...)
}

func (b *lineBuilder) line(eof bool) inputLine {
	return inputLine{text: string(b.text), eof: eof, tooLong: b.tooLong}
}

// answerSource reads the answer to one question. show displays the
// question. A source that can drop what was typed before the question
// appeared drops it first, and tells show whether a complete line was among
// what it dropped.
type answerSource interface {
	answer(ctx context.Context, show func(discardedLine bool)) (inputLine, error)
}

// terminalInput is the owner's own open of the terminal on standard input.
type terminalInput interface {
	// readLine reads one line under bound, without dropping anything first.
	readLine(ctx context.Context, bound int) (inputLine, error)
	// readAll reads to the end of input, refusing more than limit bytes.
	readAll(ctx context.Context, limit int64) ([]byte, error)
	// flush discards the input the terminal holds and reports whether a
	// complete line was among it.
	flush() (bool, error)
	close() error
}

// inputOwner is the one reader of standard input, for prompts and for the
// questions tool approval asks. On a terminal it reads through a descriptor
// of its own, opened by the terminal's name, and only while a request waits
// for input; see openTerminalReader. Other input is read in order as it
// arrives. One request is served at a time.
type inputOwner struct {
	stdin *os.File
	// terminal reports whether standard input is a terminal.
	terminal bool
	// term reads the terminal. It is nil when standard input is not one,
	// or when the terminal could not be reopened, which termErr explains.
	term    terminalInput
	termErr error

	mu sync.Mutex
	// lines reads input that is not a terminal, once a line is asked for.
	lines *streamLines
}

func newInputOwner(stdin *os.File) *inputOwner {
	owner := &inputOwner{stdin: stdin, terminal: isTerminalFile(stdin)}
	if owner.terminal {
		owner.term, owner.termErr = openTerminalReader(stdin)
	}
	return owner
}

// readAll reads the prompt of a single run: everything up to the end of
// input, at most limit bytes. On a terminal the end is Ctrl-D at the start
// of a line, and the read ends when ctx does. Other input is read to its end
// as it always was.
func (o *inputOwner) readAll(ctx context.Context, limit int64) ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.term != nil {
		return o.term.readAll(ctx, limit)
	}
	return limitio.ReadLimited(o.stdin, limit)
}

// readLine reads one line at a prompt. Nothing is dropped first, so lines
// typed while a turn ran are read in order, as they would be from a pipe.
func (o *inputOwner) readLine(ctx context.Context) (inputLine, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.term != nil {
		return o.term.readLine(ctx, constants.MaxCLIInputSize)
	}
	if o.lines == nil {
		o.lines = startStreamLines(o.stdin, constants.MaxCLIInputSize)
	}
	return o.lines.next(ctx)
}

// answer reads the answer to a question on the owner's terminal. It drops
// what the terminal holds, then shows the question, then reads, so nothing
// typed before the question appeared can answer it: a line typed while the
// model was generating, or a keystroke not yet ended by Enter.
func (o *inputOwner) answer(ctx context.Context, show func(discardedLine bool)) (inputLine, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.term == nil {
		return inputLine{}, errors.New("standard input is not a terminal this process opened")
	}
	discarded, err := o.term.flush()
	if err != nil {
		return inputLine{}, err
	}
	show(discarded)
	return o.term.readLine(ctx, maxAnswerBytes)
}

// answers returns where the approver reads answers: the owner's terminal,
// or, when the terminal could not be reopened, standard input as a stream.
// Only a single run gets that far without the owner's open; see
// streamAnswers.
func (o *inputOwner) answers() answerSource {
	if o.term != nil {
		return o
	}
	return newStreamAnswers(o.stdin)
}

// close releases the owner's terminal. No request may be in progress.
func (o *inputOwner) close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.term == nil {
		return nil
	}
	err := o.term.close()
	o.term = nil
	return err
}

// streamLines reads the lines of input that is not a terminal on a
// goroutine of its own, at most one line ahead, and hands them out in
// order. A request whose context has ended takes no line, so none is lost.
type streamLines struct {
	results chan lineResult
	// last is what every request after the end of reading gets: the end of
	// input, or the error that ended reading.
	last *lineResult
}

type lineResult struct {
	line inputLine
	err  error
}

func startStreamLines(r io.Reader, bound int) *streamLines {
	s := &streamLines{results: make(chan lineResult)}
	go func() {
		reader := bufio.NewReader(r)
		for {
			line, err := readStreamLine(reader, bound)
			s.results <- lineResult{line: line, err: err}
			if err != nil || line.eof {
				return
			}
		}
	}()
	return s
}

// next hands out the next line. The context is checked first: select
// chooses at random between a line that is ready and a context that has
// ended.
func (s *streamLines) next(ctx context.Context) (inputLine, error) {
	if err := ctx.Err(); err != nil {
		return inputLine{}, err
	}
	if s.last != nil {
		return s.last.line, s.last.err
	}
	select {
	case result := <-s.results:
		if result.err != nil || result.line.eof {
			s.last = &lineResult{line: inputLine{eof: result.line.eof}, err: result.err}
		}
		return result.line, result.err
	case <-ctx.Done():
		return inputLine{}, ctx.Err()
	}
}

// readStreamLine reads one line under bound; a longer line is read to its
// end and reported as too long.
func readStreamLine(reader *bufio.Reader, bound int) (inputLine, error) {
	b := lineBuilder{bound: bound}
	for {
		chunk, err := reader.ReadSlice('\n')
		if end := len(chunk) - 1; end >= 0 && chunk[end] == '\n' {
			b.add(chunk[:end])
			return b.line(false), nil
		}
		b.add(chunk)
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF):
			return b.line(true), nil
		case err != nil:
			return inputLine{}, err
		}
	}
}

// streamAnswers reads answers from standard input as a stream, on a
// goroutine per question, when the terminal on standard input could not be
// reopened. A question that ends early leaves its read pending, and that
// read takes the next line typed. A single run ends after such a question,
// so nothing is left to take the line from; a process that reads again
// needs the owner's own open.
type streamAnswers struct {
	reader *bufio.Reader
}

func newStreamAnswers(input io.Reader) *streamAnswers {
	return &streamAnswers{reader: bufio.NewReader(input)}
}

func (s *streamAnswers) answer(ctx context.Context, show func(discardedLine bool)) (inputLine, error) {
	show(false)
	type readResult struct {
		line string
		err  error
	}
	results := make(chan readResult, 1)
	go func() {
		line, err := s.reader.ReadString('\n')
		results <- readResult{line: line, err: err}
	}()
	select {
	case <-ctx.Done():
		return inputLine{}, ctx.Err()
	case result := <-results:
		switch {
		case errors.Is(result.err, io.EOF):
			// An answer and the end of input arrive together when the
			// operator types y and then Ctrl-D.
			return inputLine{text: result.line, eof: true}, nil
		case result.err != nil:
			return inputLine{}, result.err
		}
		return inputLine{text: strings.TrimSuffix(result.line, "\n")}, nil
	}
}
