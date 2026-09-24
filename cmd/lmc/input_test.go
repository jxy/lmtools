package main

import (
	"bufio"
	"context"
	"errors"
	"lmtools/internal/limitio"
	"os"
	"strings"
	"testing"
	"time"
)

// pipeOwner returns an input owner reading a pipe, the way lmc reads input
// that is not a terminal, and the pipe's write end.
func pipeOwner(t *testing.T) (*inputOwner, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})
	owner := newInputOwner(r)
	if owner.terminal {
		t.Fatal("a pipe was taken for a terminal")
	}
	return owner, w
}

// Lines of input that is not a terminal come out in order. A request that
// ends before a line arrives loses none: the next request gets it. After the
// end of input every request gets the end, without the last line's text.
func TestStreamLinesKeepOrderAcrossAnEndedRequest(t *testing.T) {
	owner, w := pipeOwner(t)
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := owner.readLine(ended); !errors.Is(err, context.Canceled) {
		t.Fatalf("readLine() with an ended context error = %v, want its cancellation", err)
	}

	if _, err := w.WriteString("first\nsecond"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = w.Close()
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	for i, want := range []inputLine{{text: "first"}, {text: "second", eof: true}, {eof: true}} {
		got, err := owner.readLine(ctx)
		if err != nil || got != want {
			t.Fatalf("readLine() %d = %+v, %v; want %+v", i+1, got, err, want)
		}
	}
}

// A line longer than the bound is read to its end and reported as too
// long, and the next line is whole.
func TestStreamLineReadsAnOverlongLineToItsEnd(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 100)+"\nnext\n"), 16)
	got, err := readStreamLine(reader, 20)
	if err != nil || !got.tooLong || got.text != "" || got.eof {
		t.Fatalf("readStreamLine() = %+v, %v; want a line too long", got, err)
	}
	got, err = readStreamLine(reader, 20)
	if err != nil || got != (inputLine{text: "next"}) {
		t.Fatalf("readStreamLine() = %+v, %v; want the next line", got, err)
	}
}

// A single run reads input that is not a terminal to its end, and refuses
// input past the limit with the error it always gave.
func TestInputOwnerReadsAllOfAPipe(t *testing.T) {
	owner, w := pipeOwner(t)
	if _, err := w.WriteString("line one\nline two\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = w.Close()
	got, err := owner.readAll(context.Background(), 1024)
	if err != nil || string(got) != "line one\nline two\n" {
		t.Fatalf("readAll() = %q, %v; want both lines", got, err)
	}

	owner, w = pipeOwner(t)
	if _, err := w.WriteString("past the limit"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = w.Close()
	var sizeErr *limitio.SizeLimitError
	if _, err := owner.readAll(context.Background(), 4); !errors.As(err, &sizeErr) || sizeErr.MaxSize != 4 {
		t.Fatalf("readAll() error = %v, want the size limit", err)
	}
}

// A request whose context has already ended takes no line, even one that is
// ready: select chooses at random between the two. The line stays for the
// next request.
func TestStreamLinesEndedRequestLeavesAQueuedLine(t *testing.T) {
	s := &streamLines{results: make(chan lineResult, 1)}
	s.results <- lineResult{line: inputLine{text: "queued"}}
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 20; i++ {
		if got, err := s.next(ended); !errors.Is(err, context.Canceled) {
			t.Fatalf("next() %d with an ended context = %+v, %v; want its cancellation", i+1, got, err)
		}
	}
	if got, err := s.next(context.Background()); err != nil || got != (inputLine{text: "queued"}) {
		t.Fatalf("next() = %+v, %v; want the queued line", got, err)
	}
}

// Through the owner: once a line is queued behind the one delivered,
// requests whose context has ended leave it, and the next request reads it.
func TestStreamLinesQueuedLineSurvivesEndedRequests(t *testing.T) {
	owner, w := pipeOwner(t)
	if _, err := w.WriteString("first\nsecond\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if got, err := owner.readLine(ctx); err != nil || got.text != "first" {
		t.Fatalf("readLine() = %+v, %v; want the first line", got, err)
	}
	// Let the reader queue the second line.
	time.Sleep(50 * time.Millisecond)
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 20; i++ {
		if got, err := owner.readLine(ended); !errors.Is(err, context.Canceled) {
			t.Fatalf("readLine() %d with an ended context = %+v, %v; want its cancellation", i+1, got, err)
		}
	}
	_ = w.Close()
	for i, want := range []inputLine{{text: "second"}, {eof: true}} {
		if got, err := owner.readLine(ctx); err != nil || got != want {
			t.Fatalf("readLine() %d = %+v, %v; want %+v", i+1, got, err, want)
		}
	}
}
