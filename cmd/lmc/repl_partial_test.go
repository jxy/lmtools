package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// An answer interrupted after it began is the answer the operator read. The
// turn keeps it, prompt and partial text, so a following "continue" or
// "no, shorter" has something to refer to, and says so.
func TestREPLInterruptedAnswerKeepsWhatWasRead(t *testing.T) {
	var loop *replLoop
	var mu sync.Mutex
	requests := 0
	// The interrupt comes once the answer's first text is on screen.
	answer := &signalWriter{want: "The keeper climbed", seen: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n > 1 {
			_, _ = fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"continued"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"The keeper climbed"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-answer.seen:
		case <-time.After(10 * time.Second):
		}
		loop.signals.interrupt()
		<-r.Context().Done()
	}))
	defer server.Close()
	te := newTurnTestEnvAt(t, server.URL, streamOn)
	loop, w, notifier := newTestLoop(t, te)
	loop.env.stdout = answer
	typeTurns(t, w, "tell a story\n", false)

	result := make(chan error, 1)
	go func() { result <- loop.run(false) }()
	notifier.waitFor(t, "Turn cancelled; partial assistant response saved through message")
	typeTurns(t, w, "continue\n", true)
	select {
	case <-result:
	case <-time.After(10 * time.Second):
		t.Fatal("loop did not end")
	}
	lines := sessionLines(t, loop.from)
	got := strings.Join(lines, "\n")
	for _, want := range []string{"tell a story", "The keeper climbed", "continue", "continued"} {
		if !strings.Contains(got, want) {
			t.Fatalf("session lacks %q:\n%s", want, got)
		}
	}
}

// signalWriter closes seen once what was written to it contains want.
type signalWriter struct {
	mu   sync.Mutex
	buf  strings.Builder
	want string
	seen chan struct{}
}

func (w *signalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	had := strings.Contains(w.buf.String(), w.want)
	w.buf.Write(p)
	if !had && strings.Contains(w.buf.String(), w.want) {
		close(w.seen)
	}
	return len(p), nil
}
