//go:build integration && (darwin || freebsd || linux)

package main

import (
	"lmtools/internal/mockserver"
	"lmtools/internal/ptytest"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// Keep the terminal outside the child's session so BSD does not revoke the
// slave on exit. This allows an independent check of the restored termios.
func TestTerminalREPLSIGQUITRestoresMode(t *testing.T) {
	pty, err := ptytest.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer pty.Close()
	screen := pty.Screen()
	var before syscall.Termios
	if err := terminalIoctl(int(pty.Slave.Fd()), terminalReadTermios, unsafe.Pointer(&before)); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(getLmcBinary(t), "-repl", "-provider", "openai", "-api-key-file", writeTestAPIKeyFile(t, "test-key"), "-model", "test-model", "-sessions-dir", t.TempDir(), "-log-dir", t.TempDir())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pty.Slave, pty.Slave, pty.Slave
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "TERM=xterm")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	defer func() { _ = cmd.Process.Kill(); <-exited }()
	if err := screen.Expect(freshPrompt, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(syscall.SIGQUIT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGQUIT did not stop the REPL")
	}
	if cmd.ProcessState.ExitCode() != 130 {
		t.Fatalf("SIGQUIT exit=%d, want 130", cmd.ProcessState.ExitCode())
	}
	var after syscall.Termios
	if err := terminalIoctl(int(pty.Slave.Fd()), terminalReadTermios, unsafe.Pointer(&after)); err != nil {
		t.Fatal(err)
	}
	if before.Lflag&^syscall.PENDIN != after.Lflag&^syscall.PENDIN || before.Iflag != after.Iflag || before.Cc != after.Cc {
		t.Fatalf("SIGQUIT left terminal raw: ICANON=%v ECHO=%v", after.Lflag&syscall.ICANON != 0, after.Lflag&syscall.ECHO != 0)
	}
}

func TestTerminalREPLCtrlBackslashDuringInput(t *testing.T) {
	for _, test := range []struct{ name, keys, shown string }{
		{"idle", "", ""},
		{"draft", "unfinished", "> unfinished"},
		{"paste", "\x1b[200~unsent\ntext\x1b[201~", "Enter sends; arrows edit; Ctrl-C discards."},
		{"search", "draft\x12\x1b[200~query\x1b[201~", "no match"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := mockserver.NewMockServer(numberedAnswers())
			defer server.Close()
			r := startREPLUnderTerminal(t, server.URL(), nil)
			r.expect(t, freshPrompt)
			if test.keys != "" {
				r.typeKeys(t, test.keys)
				r.expect(t, test.shown)
			}
			// The controlling terminal generates SIGQUIT from the actual key.
			r.typeKeys(t, "\x1c")
			if code := r.wait(t); code != 130 {
				t.Fatalf("Ctrl-\\ exit=%d screen=%q", code, r.screen.String())
			}
			r.expect(t, "\x1b[?2004l")
			if len(server.GetRequests()) != 0 || sessionDirCount(t, r.sessionsDir) != 0 {
				t.Fatal("termination submitted or saved an unfinished draft")
			}
		})
	}
}

func TestTerminalREPLSIGQUITDuringRequest(t *testing.T) {
	requested, release := make(chan struct{}), make(chan struct{})
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(req *http.Request) (interface{}, int, error) {
		close(requested)
		select {
		case <-release:
		case <-req.Context().Done():
		}
		return openAITextResponse("unfinished response"), http.StatusOK, nil
	}))
	defer server.Close()
	defer close(release)
	r := startREPLUnderTerminal(t, server.URL(), nil)
	r.expect(t, freshPrompt)
	r.typeKeys(t, "request\n")
	select {
	case <-requested:
	case <-time.After(5 * time.Second):
		t.Fatal("no request")
	}
	if err := r.cmd.Process.Signal(syscall.SIGQUIT); err != nil {
		t.Fatal(err)
	}
	if code := r.wait(t); code != 130 {
		t.Fatalf("SIGQUIT exit=%d screen=%q", code, r.screen.String())
	}
	r.expect(t, "\x1b[?2004l")
	if sessionDirCount(t, r.sessionsDir) != 0 {
		t.Fatal("cancelled request created a session")
	}
}

func TestTerminalREPLSIGQUITDuringApproval(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executed")
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		return toolCallResponse("touch", marker), http.StatusOK, nil
	}))
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil, "-tool")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "run the tool\n")
	r.expect(t, "      > ")
	r.typeKeys(t, "yes")
	r.expect(t, "      > yes")
	if err := r.cmd.Process.Signal(syscall.SIGQUIT); err != nil {
		t.Fatal(err)
	}
	if code := r.wait(t); code != 130 {
		t.Fatalf("SIGQUIT exit=%d screen=%q", code, r.screen.String())
	}
	r.expect(t, "\x1b[?2004l")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unapproved command created its output: %v", err)
	}
	if len(server.GetRequests()) != 1 {
		t.Fatal("termination accepted an unfinished approval")
	}
}
