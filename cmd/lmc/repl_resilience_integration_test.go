//go:build integration && (darwin || freebsd || linux)

package main

import (
	"encoding/json"
	"fmt"
	"lmtools/internal/mockserver"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func resizeREPL(t *testing.T, r *replUnderTerminal, columns int) {
	t.Helper()
	size := struct{ Rows, Cols, X, Y uint16 }{Rows: 24, Cols: uint16(columns)}
	if err := terminalIoctl(int(r.pty.Slave.Fd()), syscall.TIOCSWINSZ, unsafe.Pointer(&size)); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalREPLResizeRedrawsIdleDraft(t *testing.T) {
	const draft = "abcdefghijklmnopqrstuvwxyz0123456789"
	for _, tt := range []struct{ name, before, after, clipped, want string }{
		{"end", "", "\n", "> <", draft},
		{"start", "\x01", "X\n", "> abcdefgh>", "X" + draft},
		{"partial UTF-8", "\xc3", "\xa9\n", "> <", draft + "é"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := mockserver.NewMockServer(numberedAnswers())
			defer server.Close()
			r := startREPLUnderTerminal(t, server.URL(), nil)
			r.expect(t, freshPrompt)
			resizeREPL(t, r, 80)
			r.typeKeys(t, draft)
			r.expect(t, "> "+draft)
			r.typeKeys(t, tt.before)
			if tt.name == "start" {
				r.expect(t, "\x1b[2C")
			}
			resizeREPL(t, r, 12)
			if err := r.screen.Expect("\r\x1b[2K"+tt.clipped, time.Second); err != nil {
				t.Fatal("resize did not repaint the active draft: ", err)
			}
			// Growing again must recover the full viewport without editing.
			resizeREPL(t, r, 80)
			r.expect(t, "> "+draft)
			r.typeKeys(t, tt.after)
			r.expect(t, "answer 1")
			r.expect(t, freshPrompt)
			r.typeKeys(t, "/exit\n")
			if code := r.wait(t); code != 0 {
				t.Fatalf("exit=%d", code)
			}
			assertLastREPLPrompt(t, server, tt.want)
		})
	}
}

func TestTerminalREPLResizePreservesSearch(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)
	r.expect(t, freshPrompt)
	resizeREPL(t, r, 80)
	r.typeKeys(t, "abcdefghijklmnopqrstuvwxyz0123456789\n")
	r.expect(t, "answer 1")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "unsent\x12abc")
	r.expect(t, `search "abc": `)
	resizeREPL(t, r, 12)
	if err := r.screen.Expect("\r\x1b[2K> <", time.Second); err != nil {
		t.Fatal("resize did not repaint history search: ", err)
	}
	r.typeKeys(t, "\x1b\n")
	r.expect(t, "answer 2")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "\x15/exit\n")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	assertLastREPLPrompt(t, server, "unsent")
}

func TestTerminalREPLResizePreservesApproval(t *testing.T) {
	server := mockserver.NewMockServer()
	server.SetResponseFunc(func(*http.Request) (interface{}, int, error) {
		return toolCallResponse("echo", "approved"), http.StatusOK, nil
	})
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil, "-tool")
	r.expect(t, freshPrompt)
	resizeREPL(t, r, 80)
	r.typeKeys(t, "question\n")
	r.expect(t, "      > ")
	const reason = "Please inspect the files without changing them."
	r.typeKeys(t, reason)
	r.expect(t, "      > "+reason)
	resizeREPL(t, r, 12)
	if err := r.screen.Expect("\r\x1b[2K> <", time.Second); err != nil {
		t.Fatal("resize did not repaint approval: ", err)
	}
	server.SetResponseFunc(func(*http.Request) (interface{}, int, error) {
		return openAITextResponse("denied as requested"), http.StatusOK, nil
	})
	r.typeKeys(t, "\n")
	r.expect(t, "denied as requested")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/exit\n")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	requests := server.GetRequests()
	if len(requests) != 2 || !strings.Contains(requests[1].Body, reason) {
		t.Fatalf("resize changed or submitted the approval: %v", requests)
	}
}

func TestTerminalREPLContinuedPasteReachesProvider(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)
	r.expect(t, freshPrompt)
	for i, pasted := range []string{"// comment\nbody\\", "/ed"} {
		r.typeKeys(t, "prefix\\\n\x1b[200~"+pasted+"\x1b[201~\n")
		r.expect(t, fmt.Sprintf("answer %d", i+1))
		r.expect(t, freshPrompt)
		assertLastREPLPrompt(t, server, "prefix\n"+pasted)
	}
	r.typeKeys(t, "/exit\n")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit=%d", code)
	}
}

func assertLastREPLPrompt(t *testing.T, server *mockserver.MockServer, want string) {
	t.Helper()
	requests := server.GetRequests()
	if len(requests) == 0 {
		t.Fatal("no provider request")
	}
	var request struct {
		Messages []struct{ Role, Content string }
	}
	if err := json.Unmarshal([]byte(requests[len(requests)-1].Body), &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) == 0 {
		t.Fatal("no request messages")
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != "user" || last.Content != want {
		t.Fatalf("submitted %+v, want user text %q", last, want)
	}
}

func TestTerminalREPLFatalErrorUsesDiagnosticSurface(t *testing.T) {
	for _, redirectAnswer := range []bool{false, true} {
		name := "terminal answer"
		if redirectAnswer {
			name = "redirected answer"
		}
		t.Run(name, func(t *testing.T) {
			paths := make(chan string, 1)
			server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
				replaceWithFile(t, <-paths)
				return openAITextResponse("unsaved answer"), http.StatusOK, nil
			}))
			defer server.Close()
			stderr, err := os.CreateTemp(t.TempDir(), "stderr")
			if err != nil {
				t.Fatal(err)
			}
			defer stderr.Close()
			var stdout *os.File
			if redirectAnswer {
				stdout, err = os.CreateTemp(t.TempDir(), "stdout")
				if err != nil {
					t.Fatal(err)
				}
				defer stdout.Close()
			}
			r := startREPLUnderTerminalStreams(t, server.URL(), stdout, stderr)
			paths <- r.sessionsDir
			r.expect(t, freshPrompt)
			r.typeKeys(t, "question\n")
			if code := r.wait(t); code != 1 {
				t.Fatalf("exit=%d, want persistence failure", code)
			}
			if err := r.screen.Expect("Error: answer could not be saved", time.Second); err != nil {
				t.Fatal(err)
			}
			redirected, err := os.ReadFile(stderr.Name())
			if err != nil {
				t.Fatal(err)
			}
			if len(redirected) != 0 || strings.Count(r.screen.String(), "Error:") != 1 {
				t.Fatalf("error duplicated or sent to the wrong stream: terminal=%q stderr=%q", r.screen.String(), redirected)
			}
			if redirectAnswer {
				answer, err := os.ReadFile(stdout.Name())
				if err != nil || string(answer) != "unsaved answer\n" {
					t.Fatalf("answer=%q err=%v", answer, err)
				}
			} else if !strings.Contains(r.screen.String(), "unsaved answer\r\n\r\n") {
				t.Fatalf("fatal error lost answer separation: %q", r.screen.String())
			}
		})
	}
}

func TestTerminalREPLDumbTerminalShowsProgress(t *testing.T) {
	started := make(chan struct{})
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(req *http.Request) (interface{}, int, error) {
		close(started)
		<-req.Context().Done()
		return openAITextResponse("cancelled"), http.StatusOK, nil
	}))
	t.Cleanup(server.Close)
	r := startREPLWithTerminalType(t, server.URL(), nil, nil, "dumb")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "question\n")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not arrive")
	}
	if err := r.screen.Expect("Waiting for response...\r\n", waitingStatusDelay+time.Second); err != nil {
		t.Fatal("basic terminal has no waiting feedback: ", err)
	}
	r.typeKeys(t, "\x03")
	r.expect(t, "Turn cancelled")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/exit\n")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if strings.Contains(r.screen.String(), "\x1b") {
		t.Fatalf("basic terminal received cursor controls: %q", r.screen.String())
	}
}

func TestTerminalREPLRequestFailureUsesDiagnosticSurface(t *testing.T) {
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	logDir := t.TempDir()
	r := startREPLUnderTerminalStreams(t, "unsupported://example.test", nil, stderr,
		"-log-dir", logDir, "-log-level", "ERROR", "-retries", "0")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "question\n")
	r.expect(t, `unsupported protocol scheme "unsupported"`)
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/exit\n")
	if code := r.wait(t); code != 0 {
		t.Fatalf("recovered interactive session exited %d", code)
	}
	redirected, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(redirected) != 0 || strings.Count(r.screen.String(), `unsupported protocol scheme "unsupported"`) != 1 {
		t.Fatalf("error duplicated or misrouted: terminal=%q stderr=%q", r.screen.String(), redirected)
	}
	logged := readProcessLog(t, logDir)
	if strings.Count(logged, "Request failed") != 1 || !strings.Contains(logged, "[ERROR]") {
		t.Fatalf("missing request failure log: %q", logged)
	}
}
