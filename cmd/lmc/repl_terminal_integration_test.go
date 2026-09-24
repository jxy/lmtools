//go:build integration && (darwin || freebsd || linux)

package main

import (
	"context"
	"errors"
	"fmt"
	"lmtools/internal/mockserver"
	"lmtools/internal/ptytest"
	"lmtools/internal/session"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// replUnderTerminal is lmc -repl running on a pseudo terminal as its
// controlling terminal.
type replUnderTerminal struct {
	pty         *ptytest.PTY
	screen      *ptytest.Screen
	cmd         *exec.Cmd
	exited      chan error
	sessionsDir string
}

// startREPLUnderTerminal starts lmc -repl against the provider at url, on a
// pseudo terminal, with stdout redirected to stdout when it is not nil.
func startREPLUnderTerminal(t *testing.T, url string, stdout *os.File, extra ...string) *replUnderTerminal {
	t.Helper()
	pty, err := ptytest.Open()
	if err != nil {
		t.Fatalf("ptytest.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = pty.Close() })
	r := &replUnderTerminal{pty: pty, screen: pty.Screen(), exited: make(chan error, 1), sessionsDir: t.TempDir()}
	args := append([]string{
		"-repl", "-stream=false",
		"-provider", "openai",
		"-provider-url", url + "/v1",
		"-api-key-file", writeTestAPIKeyFile(t, "test-key"),
		"-model", "gpt-test",
		"-sessions-dir", r.sessionsDir,
		"-log-dir", t.TempDir(),
	}, extra...)
	r.cmd = exec.Command(getLmcBinary(t), args...)
	r.cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	if stdout != nil {
		r.cmd.Stdout = stdout
	}
	if err := pty.Start(r.cmd); err != nil {
		t.Fatalf("start lmc -repl: %v", err)
	}
	go func() { r.exited <- r.cmd.Wait() }()
	t.Cleanup(func() {
		select {
		case <-r.exited:
		default:
			_ = r.cmd.Process.Kill()
			<-r.exited
		}
	})
	return r
}

func (r *replUnderTerminal) expect(t *testing.T, want string) {
	t.Helper()
	if err := r.screen.Expect(want, 20*time.Second); err != nil {
		t.Fatal(err)
	}
}

func (r *replUnderTerminal) typeKeys(t *testing.T, keys string) {
	t.Helper()
	if err := r.pty.Type(keys); err != nil {
		t.Fatalf("Type(%q) error = %v", keys, err)
	}
}

// wait waits for lmc to exit and returns its exit code.
func (r *replUnderTerminal) wait(t *testing.T) int {
	t.Helper()
	select {
	case err := <-r.exited:
		r.exited <- err
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		if err != nil {
			t.Fatalf("lmc: %v", err)
		}
		return 0
	case <-time.After(20 * time.Second):
		t.Fatalf("lmc -repl did not exit; the terminal showed %q", r.screen.String())
		return -1
	}
}

// nonblocking reports whether the description behind lmc's standard input
// and output, which it shares with the test's open of the slave, has
// O_NONBLOCK.
func (r *replUnderTerminal) nonblocking(t *testing.T) bool {
	t.Helper()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, r.pty.Slave.Fd(), syscall.F_GETFL, 0)
	if errno != 0 {
		t.Fatalf("F_GETFL: %v", errno)
	}
	return flags&syscall.O_NONBLOCK != 0
}

func requestsContaining(server *mockserver.MockServer, want string) int {
	count := 0
	for _, request := range server.GetRequests() {
		if strings.Contains(request.Body, want) {
			count++
		}
	}
	return count
}

// On a terminal the loop prompts, and each line is a turn. An answer is
// shown as it came, and the next prompt starts below it after an empty line.
// The description behind standard input and output stays blocking. Ctrl-D
// after text delivers the text, and Enter then ends the line, which is one
// turn. Ctrl-D at a fresh prompt exits with success.
func TestTerminalREPLTurnsAndCtrlD(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)

	r.expect(t, freshPrompt)
	r.typeKeys(t, "first question\n")
	r.expect(t, "answer 1\r\n\r\n"+freshPrompt)
	if r.nonblocking(t) {
		t.Fatal("standard input's description became nonblocking")
	}
	r.typeKeys(t, "par"+ptytest.EndOfInput+"tial\n")
	r.expect(t, "answer 2")
	r.expect(t, freshPrompt)
	r.typeKeys(t, ptytest.EndOfInput)
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit code %d, want 0; the terminal showed %q", code, r.screen.String())
	}
	if requests := server.GetRequests(); len(requests) != 2 || !strings.Contains(requests[1].Body, `"partial"`) {
		t.Fatalf("requests = %v, want the second to carry the line partial", requests)
	}
	if count := requestsContaining(server, "answer 1"); count != 1 {
		t.Fatalf("%d requests carry the first answer, want the second alone", count)
	}
}

// A paste of several lines is a turn per line.
func TestTerminalREPLPaste(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)

	r.expect(t, freshPrompt)
	r.typeKeys(t, "one\ntwo\nthree\n")
	r.expect(t, "answer 3")
	r.expect(t, freshPrompt)
	r.typeKeys(t, ptytest.EndOfInput)
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	requests := server.GetRequests()
	if len(requests) != 3 || !strings.Contains(requests[2].Body, `"two"`) || !strings.Contains(requests[2].Body, `"three"`) {
		t.Fatalf("requests = %v, want one turn per pasted line", requests)
	}
}

// With stdout redirected to a file and standard input a terminal, the file
// holds the answers alone, separated by an empty line, and the prompts stay
// on the terminal.
func TestTerminalREPLRedirectedStdout(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	path := filepath.Join(t.TempDir(), "answers.txt")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer file.Close()
	r := startREPLUnderTerminal(t, server.URL(), file)

	r.expect(t, freshPrompt)
	r.typeKeys(t, "a\n")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "b\n")
	r.expect(t, freshPrompt)
	r.typeKeys(t, ptytest.EndOfInput)
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := string(data), "answer 1\n\nanswer 2\n"; got != want {
		t.Fatalf("stdout file = %q, want %q", got, want)
	}
	if strings.Contains(r.screen.String(), "answer") {
		t.Fatalf("the terminal showed an answer: %q", r.screen.String())
	}
}

// Ctrl-C during a turn cancels it and returns to the prompt, saying nothing
// was saved; Ctrl-C again at the idle prompt exits with success.
func TestTerminalREPLCtrlC(t *testing.T) {
	requested := make(chan struct{}, 1)
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(r *http.Request) (interface{}, int, error) {
		requested <- struct{}{}
		<-r.Context().Done()
		return nil, http.StatusServiceUnavailable, errors.New("cancelled")
	}))
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)

	r.expect(t, freshPrompt)
	r.typeKeys(t, "slow question\n")
	select {
	case <-requested:
	case <-time.After(20 * time.Second):
		t.Fatalf("no request; the terminal showed %q", r.screen.String())
	}
	r.typeKeys(t, ptytest.Interrupt)
	r.expect(t, "^C\r\nNote: Turn cancelled; nothing was saved.\r\n"+freshPrompt)
	r.typeKeys(t, ptytest.Interrupt)
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit code %d, want 0; the terminal showed %q", code, r.screen.String())
	}
	if count := sessionDirCount(t, r.sessionsDir); count != 0 {
		t.Fatalf("%d sessions, want none from the cancelled turn", count)
	}
}

// A hangup while a tool runs cancels the turn, and the process exits only
// once the outcome of the interrupted call is on disk: the session holds no
// pending call afterwards.
func TestTerminalREPLHangupDuringAToolRun(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	var mu sync.Mutex
	count := 0
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		mu.Lock()
		defer mu.Unlock()
		count++
		if count == 1 {
			return toolCallResponse("sh", "-c", `touch "$1"; exec sleep 30`, "sh", marker), http.StatusOK, nil
		}
		return openAITextResponse("after the tool"), http.StatusOK, nil
	}))
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil, "-tool", "-tool-auto-approve")

	r.expect(t, freshPrompt)
	r.typeKeys(t, "run the tool\n")
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the tool never started; the terminal showed %q", r.screen.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := r.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}
	if code := r.wait(t); code == 0 {
		t.Fatalf("exit code 0 after a hangup; the terminal showed %q", r.screen.String())
	}

	defer func(dir string) { session.SetSessionsDir(dir) }(session.GetSessionsDir())
	session.SetSessionsDir(r.sessionsDir)
	entries, err := os.ReadDir(r.sessionsDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	var sessions []string
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			sessions = append(sessions, entry.Name())
		}
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %q, want the turn's", sessions)
	}
	path := filepath.Join(r.sessionsDir, sessions[0])
	pending, err := session.CheckForPendingToolCalls(context.Background(), path)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending calls after the hangup = %v (err %v), want the interrupted call's outcome recorded", pending, err)
	}
	messages, err := session.BuildMessagesWithToolInteractions(context.Background(), path)
	if err != nil {
		t.Fatalf("BuildMessagesWithToolInteractions() error = %v", err)
	}
	if last := messages[len(messages)-1]; last.Role != "user" && last.Role != "tool" {
		t.Fatalf("the session ends with %s, want the tool result: %s", last.Role, fmt.Sprint(lmcTypedTextLines(messages)))
	}
}

// Ctrl-C while lmc -repl connects an MCP server that never answers ends the
// startup, and the run, with the interrupted status, before any prompt.
func TestTerminalREPLCtrlCDuringStartup(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	config := filepath.Join(dir, "mcp.json")
	silent := fmt.Sprintf(`{"mcpServers":{"silent":{"command":"sh","args":["-c","touch %s; exec sleep 60"]}}}`, marker)
	if err := os.WriteFile(config, []byte(silent), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	r := startREPLUnderTerminal(t, server.URL(), nil, "-mcp-config", config)

	// The server started, so lmc is waiting on it.
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server never started; the terminal showed %q", r.screen.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.typeKeys(t, ptytest.Interrupt)
	if code := r.wait(t); code != 130 {
		t.Fatalf("exit code %d, want 130; the terminal showed %q", code, r.screen.String())
	}
	if strings.Contains(r.screen.String(), freshPrompt) {
		t.Fatalf("a prompt appeared: %q", r.screen.String())
	}
	if len(server.GetRequests()) != 0 {
		t.Fatal("the provider got a request")
	}
}
