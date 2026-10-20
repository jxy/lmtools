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
	return startREPLUnderTerminalStreams(t, url, stdout, nil, extra...)
}

func startREPLUnderTerminalStreams(t *testing.T, url string, stdout, stderr *os.File, extra ...string) *replUnderTerminal {
	t.Helper()
	return startREPLWithTerminalType(t, url, stdout, stderr, "xterm", extra...)
}

func startREPLWithTerminalType(t *testing.T, url string, stdout, stderr *os.File, terminalType string, extra ...string) *replUnderTerminal {
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
	r.cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "TERM="+terminalType)
	if stdout != nil {
		r.cmd.Stdout = stdout
	}
	if stderr != nil {
		r.cmd.Stderr = stderr
	}
	if err := pty.Start(r.cmd); err != nil {
		t.Fatalf("start lmc -repl: %v", err)
	}
	go func() { r.exited <- r.cmd.Wait() }()
	t.Cleanup(func() {
		var exitErr error
		select {
		case exitErr = <-r.exited:
		default:
			_ = r.cmd.Process.Kill()
			exitErr = <-r.exited
		}
		if exitErr != nil {
			t.Logf("subprocess exit during cleanup: %v", exitErr)
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
// at a fresh prompt exits with success; cursor edits are sent on Enter.
func TestTerminalREPLTurnsAndCtrlD(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)

	r.expect(t, `lmc | model: gpt-test | buffered | thinking off`)
	r.expect(t, "/help commands | /info settings | Ctrl-C cancel | Ctrl-D exit")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "first question\n")
	r.expect(t, "Assistant:\r\n")
	r.expect(t, "answer 1\r\n\r\nSession: ")
	r.expect(t, freshPrompt)
	if r.nonblocking(t) {
		t.Fatal("standard input's description became nonblocking")
	}
	r.typeKeys(t, "partal\x1b[D\x1b[Di\x05\n")
	r.expect(t, "Assistant:\r\n")
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
	for i, prompt := range []string{"one", "two", "three"} {
		r.expect(t, freshPrompt+prompt)
		r.expect(t, fmt.Sprintf("answer %d", i+1))
	}
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
	r.expect(t, "^C\r\nTurn cancelled; nothing was saved.\r\n"+freshPrompt)
	r.typeKeys(t, ptytest.Interrupt)
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit code %d, want 0; the terminal showed %q", code, r.screen.String())
	}
	if count := sessionDirCount(t, r.sessionsDir); count != 0 {
		t.Fatalf("%d sessions, want none from the cancelled turn", count)
	}
}

// Termination while a tool runs cancels the turn, and the process exits only
// once the outcome of the interrupted call is on disk: the session holds no
// pending call afterwards.
func TestTerminalREPLTerminationDuringAToolRun(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGQUIT} {
		t.Run(sig.String(), func(t *testing.T) { testTerminalREPLTerminationDuringAToolRun(t, sig) })
	}
}

func testTerminalREPLTerminationDuringAToolRun(t *testing.T, sig syscall.Signal) {
	t.Helper()
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
	if err := r.cmd.Process.Signal(sig); err != nil {
		t.Fatalf("send %s: %v", sig, err)
	}
	if code := r.wait(t); code != 130 {
		t.Fatalf("exit code %d after %s; the terminal showed %q", code, sig, r.screen.String())
	}
	r.expect(t, "\x1b[?2004l")

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
		t.Fatalf("pending calls after termination = %v (err %v), want the interrupted call's outcome recorded", pending, err)
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

// Interactive terminal editing stays available. Commands in a paste are handled
// in order, and the former direct resend command explains the review step.
func TestTerminalREPLCommandsAndEditing(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)
	r.expect(t, freshPrompt)
	r.typeKeys(t, "discard this\x15kept wrong\x17wordx\x7f\n")
	r.expect(t, freshPrompt+"kept word")
	r.expect(t, "answer 1")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/help\n/history\n/again 1\n/exit\n")
	r.expect(t, "REPL commands:")
	r.expect(t, "1: kept word")
	r.expect(t, "Use /ed N to edit a history prompt")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit code %d, want 0; the terminal showed %q", code, r.screen.String())
	}
	requests := server.GetRequests()
	if len(requests) != 1 || !strings.Contains(requests[0].Body, `"kept word"`) {
		t.Fatalf("requests = %v, want only the edited prompt", requests)
	}
	if strings.Count(r.screen.String(), "Session: ") != 1 {
		t.Fatalf("the session ID was not shown once: %q", r.screen.String())
	}
}

// REPL diagnostics still use the controlling terminal when both output streams
// are redirected. Enter with an attached image sends a turn without text.
func TestTerminalREPLImageOnlyWithRedirectedStreams(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	dir := t.TempDir()
	image := filepath.Join(dir, "picture with spaces.png")
	if err := os.WriteFile(image, replPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	stdoutPath, stderrPath := filepath.Join(dir, "answers"), filepath.Join(dir, "diagnostics")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	r := startREPLUnderTerminalStreams(t, server.URL(), stdout, stderr)
	r.expect(t, "lmc | model:")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/image "+image+"\n")
	r.expect(t, "1 image(s) pending for the next turn.")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "\n")
	r.expect(t, "Sending 1 image(s).")
	r.expect(t, "Session: ")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/help\n/exit\n")
	r.expect(t, "REPL commands:")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit code %d, want 0; the terminal showed %q", code, r.screen.String())
	}
	answers, err := os.ReadFile(stdoutPath)
	if err != nil || string(answers) != "answer 1\n" {
		t.Fatalf("stdout = %q (err %v), want answer bytes alone", answers, err)
	}
	diagnostics, err := os.ReadFile(stderrPath)
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("stderr = %q (err %v), want REPL diagnostics on the terminal", diagnostics, err)
	}
	requests := server.GetRequests()
	if len(requests) != 1 || strings.Count(requests[0].Body, "data:image/png") != 1 {
		t.Fatalf("requests = %v, want one image-only turn", requests)
	}
	if strings.Contains(r.screen.String(), "answer 1") {
		t.Fatalf("the answer appeared on the terminal: %q", r.screen.String())
	}
}

// A cancelled prompt remains available for recall while its uncommitted turn
// remains absent from the session that the recalled prompt starts.
func TestTerminalREPLRecallAfterCancellation(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	requested := make(chan struct{}, 1)
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(r *http.Request) (interface{}, int, error) {
		mu.Lock()
		attempts++
		attempt := attempts
		mu.Unlock()
		if attempt == 1 {
			requested <- struct{}{}
			<-r.Context().Done()
			return nil, http.StatusServiceUnavailable, errors.New("cancelled")
		}
		return openAITextResponse("recalled answer"), http.StatusOK, nil
	}))
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil)
	r.expect(t, freshPrompt)
	r.typeKeys(t, "slow question\n")
	select {
	case <-requested:
	case <-time.After(20 * time.Second):
		t.Fatal("the first request never started")
	}
	r.typeKeys(t, ptytest.Interrupt)
	r.expect(t, "Turn cancelled; nothing was saved.")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/history\nslow question\n/exit\n")
	r.expect(t, "1: slow question")
	r.expect(t, "recalled answer")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit code %d, want 0; the terminal showed %q", code, r.screen.String())
	}
	requests := server.GetRequests()
	if len(requests) != 2 || strings.Count(requests[1].Body, `"slow question"`) != 1 {
		t.Fatalf("requests = %v, want the recalled prompt without the cancelled turn", requests)
	}
	if count := sessionDirCount(t, r.sessionsDir); count != 1 {
		t.Fatalf("%d sessions, want only the recalled turn's session", count)
	}
}

func TestTerminalREPLDeveloperCommands(t *testing.T) {
	server := mockserver.NewMockServer(numberedAnswers())
	defer server.Close()
	r := startREPLUnderTerminal(t, server.URL(), nil, "-developer", "Startup instruction")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "initial\n")
	r.expect(t, "answer 1")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/developer old\n/developer New line\\\nSecond line\n/history\n")
	r.expect(t, "Developer instruction queued for the next turn; it will remain in saved conversation history.")
	r.expect(t, "Developer instruction queued for the next turn; it will remain in saved conversation history.")
	r.expect(t, "1: initial")
	r.expect(t, freshPrompt)
	if requests := server.GetRequests(); len(requests) != 1 {
		t.Fatalf("commands caused inference: %v", requests)
	}
	r.typeKeys(t, "second\n")
	r.expect(t, "answer 2")
	r.expect(t, freshPrompt)
	r.typeKeys(t, "/developer discarded\n/clear-developer\n//developer literal\n/exit\n")
	r.expect(t, "Cleared pending developer instruction; saved instructions are unchanged.")
	r.expect(t, freshPrompt+"//developer literal")
	r.expect(t, "answer 3")
	if code := r.wait(t); code != 0 {
		t.Fatalf("exit code %d; terminal=%q", code, r.screen.String())
	}
	requests := server.GetRequests()
	if len(requests) != 3 {
		t.Fatalf("%d requests, want the three text turns", len(requests))
	}
	for i, request := range requests {
		instructions := replRequestDeveloperTexts(t, request.Body)
		wantCount := 1
		if i > 0 {
			wantCount = 2
		}
		if len(instructions) != wantCount || instructions[0] != "Startup instruction" || (i > 0 && instructions[1] != "New line\nSecond line") {
			t.Fatalf("request %d instructions=%q, want prior instructions once in order", i+1, instructions)
		}
	}
	if strings.Count(r.screen.String(), "Session: ") != 1 || sessionDirCount(t, r.sessionsDir) != 1 {
		t.Fatalf("developer updates did not continue one session: %q", r.screen.String())
	}
}
