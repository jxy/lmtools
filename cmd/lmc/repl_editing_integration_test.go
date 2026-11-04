//go:build integration && (darwin || freebsd || linux)

package main

import (
	"encoding/json"
	"lmtools/internal/mockserver"
	"lmtools/internal/ptytest"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestTerminalREPLPastedSearchRequiresTwoEnters(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)
	r.expect(t, freshPrompt)
	const prompt = "needle café\n界\\"
	r.typeKeys(t, "\x1b[200~"+prompt+"\x1b[201~\n")
	r.expect(t, "answer 1")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "unrelated\n")
	r.expect(t, "answer 2")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "\x12\x1b[200~café\n界\\\x1b[201~")
	r.expect(t, "History search: Enter accepts; Esc restores draft; Ctrl-R finds older.")
	r.expect(t, `search "caf`)
	if len(server.GetRequests()) != 2 {
		t.Fatal("pasting a query submitted a prompt")
	}
	r.typeKeys(t, "\r\n")
	r.expect(t, "[2/2] "+freshPrompt+"界\\")
	if len(server.GetRequests()) != 2 {
		t.Fatal("accepting the search submitted a prompt")
	}
	r.typeKeys(t, "X\n")
	r.expect(t, "answer 3")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/exit\n")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit=%d screen=%q", code, r.screen.String())
	}
	requests := server.GetRequests()
	if len(requests) != 3 {
		t.Fatalf("requests=%d, want 3", len(requests))
	}
	var request struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(requests[2].Body), &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) == 0 {
		t.Fatal("request has no messages")
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != "user" || last.Content != prompt+"X" {
		t.Fatalf("search edited or submitted the query: %+v", last)
	}
}

func TestTerminalREPLEscapeThenEnterSubmitsRestoredDraft(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)
	r.expect(t, freshPrompt)
	r.typeKeys(t, "old prompt\n")
	r.expect(t, "answer 1")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "draft\x12")
	r.expect(t, "History search: Enter accepts; Esc restores draft; Ctrl-R finds older.")
	// No pause separates Escape and Enter, so both can arrive in one read.
	r.typeKeys(t, "\x1b\r\n")
	r.expect(t, "answer 2")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/exit\n")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit=%d transcript=%q", code, r.screen.String())
	}
	requests := server.GetRequests()
	if len(requests) != 2 {
		t.Fatalf("requests=%d, want 2", len(requests))
	}
	var request struct {
		Messages []struct {
			Role, Content string
		}
	}
	if err := json.Unmarshal([]byte(requests[1].Body), &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) == 0 {
		t.Fatal("request has no messages")
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != "user" || last.Content != "draft" {
		t.Fatalf("Escape submitted the history candidate: %+v", last)
	}
}

// Keys typed after a paste work as usual, and a paste goes out as it came: a
// typed backslash continues a pasted draft without printing it again, code
// that opens with a // comment keeps both slashes, and a pasted line that
// starts with a slash is text.
func TestTerminalREPLTypedKeysWorkAfterPaste(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)
	r.expect(t, freshPrompt)
	r.typeKeys(t, "\x1b[200~func f() {\n\treturn\n}\x1b[201~")
	r.expect(t, "Type \\ and Enter to continue.")
	r.typeKeys(t, "\\\n")
	r.expect(t, freshPrompt+"[the pasted draft above, 3 lines]\\")
	r.expect(t, continuationPrompt)
	if len(server.GetRequests()) != 0 {
		t.Fatal("a backslash typed after a paste sent the draft")
	}
	r.typeKeys(t, "What does it do?\n")
	r.expect(t, "answer 1")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "\x1b[200~// Package x\npackage x\x1b[201~")
	r.expect(t, "Enter sends;")
	r.typeKeys(t, "\n")
	r.expect(t, "answer 2")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "\x1b[200~/home/me/x.c:12: error: oops\x1b[201~\n")
	r.expect(t, "answer 3")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/exit\n")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit=%d screen=%q", code, r.screen.String())
	}
	requests := server.GetRequests()
	if len(requests) != 3 ||
		!strings.Contains(requests[0].Body, `"func f() {\n\treturn\n}\nWhat does it do?"`) ||
		!strings.Contains(requests[1].Body, `"// Package x\npackage x"`) ||
		!strings.Contains(requests[2].Body, `"/home/me/x.c:12: error: oops"`) {
		t.Fatalf("pasted turns were changed: %v", requests)
	}
	if strings.Contains(r.screen.String(), "Unknown REPL command") {
		t.Fatal("a pasted line ran as a command")
	}
}

func TestTerminalREPLBracketedPasteAndRecall(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)
	r.expect(t, freshPrompt)
	// The terminal can be narrower than a draft or status line.
	size := struct{ Rows, Cols, X, Y uint16 }{Rows: 24, Cols: 12}
	if err := terminalIoctl(int(r.pty.Slave.Fd()), syscall.TIOCSWINSZ, unsafe.Pointer(&size)); err != nil {
		t.Fatal(err)
	}
	r.typeKeys(t, "\x1b[200~alpha\n\nbeta\\\x1b[201~")
	r.expect(t, "Enter sends; arrows edit; Ctrl-C discards.")
	if len(server.GetRequests()) != 0 {
		t.Fatal("bracketed paste sent without Enter")
	}
	r.typeKeys(t, "\x1b[DX\x05\n")
	r.expect(t, "answer 1")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "\x1b[A\n")
	r.expect(t, "answer 2")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "//exit\n")
	r.expect(t, "answer 3")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "\x1b[A\n")
	r.expect(t, "answer 4")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/exit\n")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit=%d screen=%q", code, r.screen.String())
	}
	requests := server.GetRequests()
	if len(requests) != 4 || !strings.Contains(requests[0].Body, `"alpha\n\nbetaX\\"`) || strings.Count(requests[1].Body, `"alpha\n\nbetaX\\"`) != 2 || strings.Count(requests[3].Body, `"/exit"`) != 2 {
		t.Fatalf("paste/recall changed the draft: %v", requests)
	}
	if regexp.MustCompile("\x1b\\[[0-9;]*m").MatchString(r.screen.String()) {
		t.Fatal("UI introduced terminal styling")
	}
}

func TestTerminalREPLQueuedInputStaysHiddenWhileWaiting(t *testing.T) {
	requested := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var calls atomic.Int32
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(req *http.Request) (interface{}, int, error) {
		if calls.Add(1) == 1 {
			close(requested)
			select {
			case <-release:
			case <-req.Context().Done():
			}
		}
		return openAITextResponse("finished"), http.StatusOK, nil
	}))
	defer server.Close()
	defer unblock()
	r := startREPLUnderTerminal(t, server.URL(), nil)
	r.expect(t, freshPrompt)
	r.typeKeys(t, "first\n")
	select {
	case <-requested:
	case <-time.After(5 * time.Second):
		t.Fatal("no request")
	}
	r.typeKeys(t, "queued secret\n")
	r.expect(t, "Waiting for response...")
	if strings.Contains(r.screen.String(), "queued secret") {
		t.Fatal("queued input appeared during the response")
	}
	unblock()
	r.expect(t, "finished")
	r.expect(t, "queued secret")
	r.expect(t, "finished")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/exit\n")
	if code := r.wait(t); code != 0 || len(server.GetRequests()) != 2 {
		t.Fatalf("exit=%d requests=%v", code, server.GetRequests())
	}
}

func TestTerminalREPLDiscardInputAndRestoreOnEOF(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)
	r.expect(t, freshPrompt)
	r.typeKeys(t, "unfinished")
	r.expect(t, freshPrompt+"unfinished")
	r.typeKeys(t, ptytest.Interrupt)
	r.expect(t, "Discarded the unfinished turn.")
	r.expect(t, freshPrompt)
	// Ctrl-D with text on the line ends nothing; on the empty line of an
	// unfinished turn it ends the input, and the turn is not sent.
	r.typeKeys(t, "also unfinished"+ptytest.EndOfInput)
	r.typeKeys(t, "\\\n")
	r.expect(t, continuationPrompt)
	r.typeKeys(t, ptytest.EndOfInput)
	r.expect(t, "Input ended inside an unfinished turn, which was not sent.")
	if code := r.wait(t); code != 0 || len(server.GetRequests()) != 0 {
		t.Fatalf("exit=%d requests=%v", code, server.GetRequests())
	}
	// BSD revokes the slave when the session leader exits, so the in-process
	// terminal test checks termios; this subprocess checks paste teardown.
	if !strings.Contains(r.screen.String(), "\x1b[?2004l") {
		t.Fatal("REPL did not restore terminal editing/paste mode")
	}
}
