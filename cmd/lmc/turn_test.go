package main

import (
	"bytes"
	"context"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"io"
	"lmtools/internal/config"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"lmtools/internal/mockserver"
	"lmtools/internal/session"
	"lmtools/internal/ui"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// turnTestEnv is a turn environment wired to a mock provider, a temporary
// sessions directory, and a temporary log directory, with the operator
// surfaces replaced by test doubles so no test depends on the terminal.
type turnTestEnv struct {
	env         *turnEnv
	opts        core.RequestOptions
	stdout      *bytes.Buffer
	notifier    *core.TestNotifier
	sessionsDir string
}

func newTurnTestEnv(t *testing.T, server *mockserver.MockServer, extraFlags ...string) turnTestEnv {
	t.Helper()
	return newTurnTestEnvAt(t, server.URL(), extraFlags...)
}

// earlierModeFlags pin the modes lmc ran in before streaming, the thinking
// display, and the Responses API became its defaults. The tests were written
// for them, and the mocks serve buffered Chat Completions. A later flag wins,
// so a test that needs one of the modes passes streamOn, thinkingOn, or
// responsesOn after them.
var earlierModeFlags = []string{"-no-stream", "-hide-thinking", "-chat-completions"}

const (
	streamOn    = "-no-stream=false"
	thinkingOn  = "-hide-thinking=false"
	responsesOn = "-chat-completions=false"
)

// newTurnTestEnvAt is newTurnTestEnv for a provider served at baseURL by a
// test's own handler.
func newTurnTestEnvAt(t *testing.T, baseURL string, extraFlags ...string) turnTestEnv {
	t.Helper()

	sessionsDir := t.TempDir()

	logDir := t.TempDir()
	logger.ResetForTesting()
	if err := logger.InitializeWithOptions(
		logger.WithLogDir(logDir),
		logger.WithLevel("error"),
		logger.WithStderr(false),
		logger.WithFile(true),
	); err != nil {
		t.Fatalf("InitializeWithOptions() error = %v", err)
	}
	t.Cleanup(logger.ResetForTesting)

	args := append([]string{
		"-provider", "openai",
		"-provider-url", baseURL + "/v1",
		"-model", "gpt-test",
		"-sessions-dir", sessionsDir,
		"-retries", "0",
	}, earlierModeFlags...)
	args = append(args, extraFlags...)
	cfg, err := config.ParseFlags(args)
	if err != nil {
		t.Fatalf("ParseFlags(%v) error = %v", args, err)
	}

	stdout := &bytes.Buffer{}
	notifier := core.NewTestNotifier()
	env := &turnEnv{
		cfg:             &cfg,
		store:           openStore(&cfg),
		notifier:        notifier,
		toolUI:          core.TestToolUI{},
		logDir:          logDir,
		pendingToolMode: session.PendingToolExecute,
		stdout:          stdout,
		stderr:          &bytes.Buffer{},
	}
	return turnTestEnv{env: env, opts: cfg.RequestOptions(), stdout: stdout, notifier: notifier, sessionsDir: sessionsDir}
}

func sessionDirCount(t *testing.T, sessionsDir string) int {
	t.Helper()
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", sessionsDir, err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			count++
		}
	}
	return count
}

// A single run separates its answers on stdout when stdout is another file
// than stderr. A shell's 2>&1 gives both one file, a copy of one descriptor,
// where the notes on stderr separate them as on a terminal.
func TestSameFileTellsOneFileFromTwo(t *testing.T) {
	_, first, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	defer first.Close()
	_, second, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	defer second.Close()
	fd, err := syscall.Dup(int(first.Fd()))
	if err != nil {
		t.Fatalf("Dup() error = %v", err)
	}
	copied := os.NewFile(uintptr(fd), "copy")
	defer copied.Close()

	if !sameFile(first, copied) {
		t.Fatal("sameFile(pipe, its copy) = false, want true")
	}
	if sameFile(first, second) {
		t.Fatal("sameFile(pipe, another pipe) = true, want false")
	}
}

func TestRunTurnCommitsAndSavesTheAnswer(t *testing.T) {
	server := mockserver.NewMockServer(mockserver.WithDefaultResponse("pong from the mock"))
	defer server.Close()
	te := newTurnTestEnv(t, server)

	outcome, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "ping the mock"})
	if err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}
	if !outcome.Committed() {
		t.Fatal("outcome.Committed() = false, want the turn's session")
	}
	if outcome.UnsavedAnswer != nil {
		t.Fatalf("outcome.UnsavedAnswer = %v, want nil", outcome.UnsavedAnswer)
	}
	if got := te.stdout.String(); !strings.Contains(got, "pong from the mock") {
		t.Fatalf("stdout = %q, want the answer", got)
	}

	lines := lmcTypedTextLines(historyOf(t, outcome.Session))
	if len(lines) < 2 || lines[len(lines)-2] != "user:ping the mock" || lines[len(lines)-1] != "assistant:pong from the mock" {
		t.Fatalf("session lines = %q, want the user turn followed by the answer", lines)
	}
}

func TestRunTurnProviderFailureCommitsNothing(t *testing.T) {
	server := mockserver.NewMockServer()
	defer server.Close()
	server.SimulateError(http.StatusInternalServerError, "provider unavailable")
	te := newTurnTestEnv(t, server)

	outcome, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "ping the mock"})
	if err == nil {
		t.Fatal("runTurn() error = nil, want the provider failure")
	}
	if outcome.Committed() {
		t.Fatalf("outcome.Session = %+v, want nil when the request failed before the commit", outcome.Session)
	}
	if n := sessionDirCount(t, te.sessionsDir); n != 0 {
		t.Fatalf("session directories = %d, want none after a failed request", n)
	}
}

func TestRunTurnEmptyInputCommitsNothing(t *testing.T) {
	server := mockserver.NewMockServer()
	defer server.Close()
	te := newTurnTestEnv(t, server)

	outcome, err := runTurn(context.Background(), te.env, te.opts, turnInput{})
	if err == nil || !strings.Contains(err.Error(), "input cannot be empty") {
		t.Fatalf("runTurn() error = %v, want the empty input refusal", err)
	}
	if outcome.Committed() {
		t.Fatalf("outcome.Session = %+v, want nil", outcome.Session)
	}
	if got := len(server.GetRequests()); got != 0 {
		t.Fatalf("provider requests = %d, want none for an empty turn", got)
	}
	if n := sessionDirCount(t, te.sessionsDir); n != 0 {
		t.Fatalf("session directories = %d, want none", n)
	}
}

func TestRunTurnPrintCurlSendsAndWritesNothing(t *testing.T) {
	server := mockserver.NewMockServer()
	defer server.Close()
	te := newTurnTestEnv(t, server, "-print-curl")
	te.env.pendingToolMode = session.PendingToolPreview

	outcome, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "ping the mock"})
	if err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}
	if outcome.Committed() {
		t.Fatalf("outcome.Session = %+v, want nil for a preview", outcome.Session)
	}
	if got := te.stdout.String(); !strings.HasPrefix(got, "curl ") {
		t.Fatalf("stdout = %q, want the curl command", got)
	}
	if got := len(server.GetRequests()); got != 0 {
		t.Fatalf("provider requests = %d, want none for a preview", got)
	}
	if n := sessionDirCount(t, te.sessionsDir); n != 0 {
		t.Fatalf("session directories = %d, want none", n)
	}
}

func TestRunTurnReportsAnAnswerItCouldNotSave(t *testing.T) {
	server := mockserver.NewMockServer(mockserver.WithDefaultResponse("pong from the mock"))
	defer server.Close()
	te := newTurnTestEnv(t, server)

	// Replace the session directory with a file once the plan commits, so the
	// answer is shown and its save fails.
	afterTurnCommitForTest = func(sess *session.Session) {
		replaceWithFile(t, filepath.Join(te.sessionsDir, sess.Number))
	}
	t.Cleanup(func() { afterTurnCommitForTest = nil })

	outcome, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "ping the mock"})
	if err != nil {
		t.Fatalf("runTurn() error = %v, want success with the save failure in the outcome", err)
	}
	if !outcome.Committed() {
		t.Fatal("outcome.Committed() = false, want the committed session")
	}
	if outcome.UnsavedAnswer == nil {
		t.Fatal("outcome.UnsavedAnswer = nil, want the save failure")
	}
	if got := te.stdout.String(); !strings.Contains(got, "pong from the mock") {
		t.Fatalf("stdout = %q, want the answer shown before the failed save", got)
	}
	if len(te.notifier.WarnMessages) == 0 || !strings.Contains(te.notifier.WarnMessages[0], "Failed to save response to session") {
		t.Fatalf("warnings = %q, want the save failure warning a single run has always printed", te.notifier.WarnMessages)
	}
}

// A turn reads each saved message once: its tool follow-ups and the next
// turn from its session build their requests from the history the session
// keeps. The tool here removes the question's content file, which a request
// that read the history again could not send.
func TestTurnsReadEachSavedMessageOnce(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	var question string
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(r *http.Request) (interface{}, int, error) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		bodies = append(bodies, string(body))
		if len(bodies) > 1 {
			return map[string]interface{}{
				"id": "chatcmpl-text", "object": "chat.completion", "model": "gpt-test",
				"choices": []map[string]interface{}{{
					"index": 0, "finish_reason": "stop",
					"message": map[string]interface{}{"role": "assistant", "content": fmt.Sprint("answer ", len(bodies))},
				}},
			}, http.StatusOK, nil
		}
		args, _ := json.Marshal(map[string]interface{}{"command": []string{"rm", question}})
		return map[string]interface{}{
			"id": "chatcmpl-tool", "object": "chat.completion", "model": "gpt-test",
			"choices": []map[string]interface{}{{
				"index": 0, "finish_reason": "tool_calls",
				"message": map[string]interface{}{
					"role": "assistant", "content": nil,
					"tool_calls": []map[string]interface{}{{
						"id": "call_rm", "type": "function",
						"function": map[string]interface{}{"name": "universal_command", "arguments": string(args)},
					}},
				},
			}},
		}, http.StatusOK, nil
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server, "-tool", "-tool-auto-approve")
	question = filepath.Join(te.sessionsDir, "0001", "0002.content.json")

	first, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "remove the question"})
	if err != nil {
		t.Fatalf("the first turn: %v", err)
	}
	if _, err := os.Stat(question); !os.IsNotExist(err) {
		t.Fatalf("the tool left the question's content: %v", err)
	}
	if _, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "and then", from: first.Session}); err != nil {
		t.Fatalf("the next turn: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 3 {
		t.Fatalf("%d requests, want the question, its follow-up and the next turn", len(bodies))
	}
	for i, body := range bodies[1:] {
		if !strings.Contains(body, "remove the question") {
			t.Errorf("request %d lacks the question: %s", i+2, body)
		}
	}
	if !strings.Contains(bodies[2], "answer 2") || !strings.Contains(bodies[2], "and then") {
		t.Errorf("the next turn's request: %s", bodies[2])
	}
}

func TestRunTurnToolRoundFailureKeepsTheAdvancedSession(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls > 1 {
			return nil, http.StatusInternalServerError, stdErrors.New("follow-up failed")
		}
		return map[string]interface{}{
			"id":     "chatcmpl-tool",
			"object": "chat.completion",
			"model":  "gpt-test",
			"choices": []map[string]interface{}{{
				"index":         0,
				"finish_reason": "tool_calls",
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []map[string]interface{}{{
						"id":   "call_echo",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "universal_command",
							"arguments": `{"command":["echo","from the tool"]}`,
						},
					}},
				},
			}},
		}, http.StatusOK, nil
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server, "-tool", "-tool-auto-approve")

	outcome, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "run the tool"})
	if err == nil {
		t.Fatal("runTurn() error = nil, want the follow-up failure")
	}
	if !outcome.Committed() {
		t.Fatal("outcome.Committed() = false, want the session the tool round advanced")
	}

	messages := historyOf(t, outcome.Session)
	var roles []string
	for _, msg := range messages {
		roles = append(roles, msg.Role)
	}
	want := []string{"system", "user", "assistant", "user"}
	if strings.Join(roles, ",") != strings.Join(want, ",") {
		t.Fatalf("session roles = %v, want %v", roles, want)
	}
	var sawCall, sawResult bool
	for _, block := range messages[2].Blocks {
		if _, ok := block.(core.ToolUseBlock); ok {
			sawCall = true
		}
	}
	for _, block := range messages[3].Blocks {
		if result, ok := block.(core.ToolResultBlock); ok && strings.Contains(result.Content, "from the tool") {
			sawResult = true
		}
	}
	if !sawCall || !sawResult {
		t.Fatalf("tool call saved = %v, result saved = %v, want both before the follow-up failed", sawCall, sawResult)
	}
}

func openAITextResponse(text string) map[string]interface{} {
	return map[string]interface{}{
		"id":     "chatcmpl-text",
		"object": "chat.completion",
		"model":  "gpt-test",
		"choices": []map[string]interface{}{{
			"index":         0,
			"finish_reason": "stop",
			"message":       map[string]interface{}{"role": "assistant", "content": text},
		}},
	}
}

// A resumed session holds a pending call. The turn runs it and commits its
// result, then the provider request fails. The retry sends the recorded
// result and runs nothing: retrying a request and running a tool again are
// different operations.
func TestRunTurnRetryAfterAProviderFailureDoesNotRunToolsAgain(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if attempts == 1 {
			return nil, http.StatusInternalServerError, stdErrors.New("provider down")
		}
		return openAITextResponse("done after the tool"), http.StatusOK, nil
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server, "-tool", "-tool-auto-approve")

	counter := filepath.Join(t.TempDir(), "runs")
	calls := []core.ToolCall{{
		ID:   "call_count",
		Name: "universal_command",
		Args: json.RawMessage(fmt.Sprintf(`{"command":["sh","-c","echo run >> \"$1\"; echo ran","sh",%q]}`, counter)),
	}}
	sess := newTestSession(t, te.store(), textEntry(core.RoleUser, "count once"))
	saveToolCalls(t, sess, "counting", claimedCalls(t, te.store(), calls))
	te.env.cfg.Start = sess.Ref()

	outcome, err := runTurn(context.Background(), te.env, te.opts, turnInput{})
	if err == nil {
		t.Fatal("first turn error = nil, want the provider failure")
	}
	if !outcome.Committed() {
		t.Fatal("first turn committed nothing, want the pending call's result committed before the request")
	}
	if tools := newestTools(t, outcome.Session); tools == nil || len(tools.Results) != 1 {
		t.Fatalf("newest message after the failed request holds %+v, want the call's result", tools)
	}

	te.env.cfg.Start = outcome.Session.Ref()
	if _, err := runTurn(context.Background(), te.env, te.opts, turnInput{}); err != nil {
		t.Fatalf("retry error = %v", err)
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if runs := strings.Count(string(data), "\n"); runs != 1 {
		t.Fatalf("the pending call ran %d times, want once", runs)
	}
	requests := server.GetRequests()
	if len(requests) != 2 || !strings.Contains(requests[1].Body, "ran") {
		t.Fatalf("retry request = %v, want it to carry the recorded result", requests)
	}
}

// Another writer appends after the turn's question between the plan's
// commit and the answer save. The answer still follows the question, as an
// alternative to the other writer's message, and the turn's history holds
// none of the other writer's.
func TestRunTurnWritesAfterItsOwnMessages(t *testing.T) {
	server := mockserver.NewMockServer(mockserver.WithDefaultResponse("pong from the mock"))
	defer server.Close()
	te := newTurnTestEnv(t, server)

	var question session.Ref
	afterTurnCommitForTest = func(sess *session.Session) {
		question = sess.Ref()
		other := *sess
		appendTestSessionMessage(t, context.Background(), &other, core.RoleAssistant, "someone else's answer")
	}
	t.Cleanup(func() { afterTurnCommitForTest = nil })

	outcome, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "ping the mock"})
	if err != nil {
		t.Fatalf("runTurn() error = %v", err)
	}
	joined := strings.Join(lmcTypedTextLines(historyOf(t, outcome.Session)), "|")
	if !strings.HasSuffix(joined, "user:ping the mock|assistant:pong from the mock") || strings.Contains(joined, "someone else") {
		t.Fatalf("lineage = %q, want this turn without the other writer's answer", joined)
	}
	tree, err := te.store().Tree(question.Session)
	if err != nil {
		t.Fatal(err)
	}
	if children := tree.Children[question.Message]; len(children) != 2 {
		t.Fatalf("the question has children %v, want both answers", children)
	}
}

func toolCallResponse(command ...string) map[string]interface{} {
	args, _ := json.Marshal(map[string][]string{"command": command})
	return map[string]interface{}{
		"id":     "chatcmpl-tool",
		"object": "chat.completion",
		"model":  "gpt-test",
		"choices": []map[string]interface{}{{
			"index":         0,
			"finish_reason": "tool_calls",
			"message": map[string]interface{}{
				"role":    "assistant",
				"content": "running it",
				"tool_calls": []map[string]interface{}{{
					"id":       "call_1",
					"type":     "function",
					"function": map[string]interface{}{"name": "universal_command", "arguments": string(args)},
				}},
			},
		}},
	}
}

// replaceWithFile swaps a session directory for a regular file, so every
// later save into it fails.
func replaceWithFile(t *testing.T, dir string) {
	t.Helper()
	if err := os.Rename(dir, dir+".moved"); err != nil {
		t.Errorf("Rename() error = %v", err)
	}
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Errorf("WriteFile() error = %v", err)
	}
}

// The tool-bearing response is presented, then its save fails: the outcome
// records the unsaved answer, as the answer-only path does.
func TestRunTurnReportsAToolBearingAnswerItCouldNotSave(t *testing.T) {
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		return toolCallResponse("echo", "hi"), http.StatusOK, nil
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server, "-tool", "-tool-auto-approve")

	afterTurnCommitForTest = func(sess *session.Session) { replaceWithFile(t, filepath.Join(te.sessionsDir, sess.Number)) }
	t.Cleanup(func() { afterTurnCommitForTest = nil })

	outcome, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "run the tool"})
	if err == nil || outcome.UnsavedAnswer == nil {
		t.Fatalf("runTurn() = %+v, %v; want the failed save as the error and the unsaved answer", outcome, err)
	}
	if !strings.Contains(te.stdout.String(), "running it") {
		t.Fatalf("stdout = %q, want the answer presented before the failed save", te.stdout.String())
	}
}

// A run cancelled after its commit saves the answer with its calls, which
// did not start, and their results saying so; a run that later continues at
// the call finds that outcome in the journal and runs nothing.
func TestCancelAfterCommitPreservesAnswerAndUnstartedCalls(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		return toolCallResponse("touch", marker), http.StatusOK, nil
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server, "-notebook", t.TempDir(), "-context-window", "8192", "-tool", "-tool-auto-approve")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	afterTurnCommitForTest = func(*session.Session) { cancel() }
	t.Cleanup(func() { afterTurnCommitForTest = nil })
	out, err := runTurn(ctx, te.env, te.opts, turnInput{text: "test setup"})
	if err == nil || out.UnsavedAnswer != nil || !out.PlanCommitted || out.Session == nil || !stdErrors.Is(err, context.Canceled) {
		t.Fatalf("outcome %+v, error %v", out, err)
	}
	h := historyAt(t, te.store(), out.Session.Ref())
	n := len(h.Messages)
	assistant, result := h.Contents[n-2], h.Contents[n-1]
	if h.Messages[n-2].Role != core.RoleAssistant || assistant.Tools == nil || len(assistant.Tools.Calls) != 1 || assistant.Tools.Calls[0].InvocationID == "" {
		t.Fatalf("assistant %+v", assistant)
	}
	if result.Tools == nil || len(result.Tools.Results) != 1 || !result.Tools.Results[0].NotRun || result.Tools.Results[0].ID != assistant.Tools.Calls[0].ID {
		t.Fatalf("results %+v", result.Tools)
	}
	if awaitsAnswer(t, out.Session) != true {
		t.Fatal("the saved results do not await an answer")
	}
	// A branch at the call sees the journal's outcome without the result message.
	at, err := te.store().Session(h.Ref(n - 2))
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := at.ResolvePending(context.Background(), te.opts, core.NewTestLogger(false), te.notifier, core.TestToolUI{}, nil, nil)
	if err != nil || !recovered.Committed {
		t.Fatal(recovered, err)
	}
	saved := newestTools(t, at)
	if saved == nil || len(saved.Results) != 1 || saved.Results[0].Error != result.Tools.Results[0].Error || !saved.Results[0].NotRun {
		t.Fatal(saved)
	}
	if _, err := os.Stat(marker); !stdErrors.Is(err, os.ErrNotExist) {
		t.Fatal("command ran", err)
	}
}

// A later response in the tool loop is presented, then its save fails.
func TestRunTurnReportsAFinalAnswerItCouldNotSave(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	var sessionsDir string
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return toolCallResponse("echo", "hi"), http.StatusOK, nil
		}
		// The tool results are saved by now; break the session before the
		// final answer is.
		entries, err := os.ReadDir(sessionsDir)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		for _, entry := range entries {
			if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
				replaceWithFile(t, filepath.Join(sessionsDir, entry.Name()))
			}
		}
		return openAITextResponse("final answer"), http.StatusOK, nil
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server, "-tool", "-tool-auto-approve")
	sessionsDir = te.sessionsDir

	outcome, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "run the tool"})
	if err == nil || outcome.UnsavedAnswer == nil {
		t.Fatalf("runTurn() = %+v, %v; want the failed save as the error and the unsaved answer", outcome, err)
	}
	if !strings.Contains(te.stdout.String(), "final answer") {
		t.Fatalf("stdout = %q, want the final answer presented before the failed save", te.stdout.String())
	}
}

// The answer is presented as it arrives, before the plan commits; when the
// commit fails, the outcome says the transcript lacks an answer that was
// shown, and that nothing was committed.
func TestRunTurnReportsAnAnswerShownBeforeTheCommitFailed(t *testing.T) {
	server := mockserver.NewMockServer(mockserver.WithDefaultResponse("pong from the mock"))
	defer server.Close()
	te := newTurnTestEnv(t, server)
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	te.env.store = session.Open(blocked)

	outcome, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "ping the mock"})
	if err == nil || outcome.UnsavedAnswer == nil {
		t.Fatalf("runTurn() = %+v, %v; want the failed commit as the error and the unsaved answer", outcome, err)
	}
	if outcome.Committed() {
		t.Fatalf("outcome.Session = %+v, want nothing committed", outcome.Session)
	}
	if !strings.Contains(te.stdout.String(), "pong from the mock") {
		t.Fatalf("stdout = %q, want the answer presented before the commit", te.stdout.String())
	}
}

// blockJournal puts a file where the invocation journal's directory goes, so
// claiming any call fails.
func blockJournal(t *testing.T, sessionsDir string) {
	t.Helper()
	journal := filepath.Join(sessionsDir, ".journal")
	if err := os.RemoveAll(journal); err != nil {
		t.Errorf("RemoveAll() error = %v", err)
	}
	if err := os.WriteFile(journal, nil, 0o600); err != nil {
		t.Errorf("WriteFile() error = %v", err)
	}
}

// The tool-bearing response is presented, then claiming its calls fails
// before anything saves it: the outcome still reports the answer as unsaved.
func TestRunTurnReportsAnAnswerLeftUnsavedWhenClaimingFails(t *testing.T) {
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		return toolCallResponse("echo", "hi"), http.StatusOK, nil
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server, "-tool", "-tool-auto-approve")
	blockJournal(t, te.sessionsDir)

	outcome, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "run the tool"})
	if err == nil || outcome.UnsavedAnswer == nil {
		t.Fatalf("runTurn() = %+v, %v; want the claim failure reported as leaving the answer unsaved", outcome, err)
	}
	if !strings.Contains(te.stdout.String(), "running it") {
		t.Fatalf("stdout = %q, want the answer presented before the failure", te.stdout.String())
	}
}

// A later tool-bearing response is presented, then claiming its calls fails.
func TestRunTurnReportsALaterAnswerLeftUnsavedWhenClaimingFails(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	var sessionsDir string
	server := mockserver.NewMockServer(mockserver.WithResponseFunc(func(*http.Request) (interface{}, int, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 2 {
			blockJournal(t, sessionsDir)
		}
		return toolCallResponse("echo", "hi"), http.StatusOK, nil
	}))
	defer server.Close()
	te := newTurnTestEnv(t, server, "-tool", "-tool-auto-approve")
	sessionsDir = te.sessionsDir

	outcome, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "run the tool"})
	if err == nil || outcome.UnsavedAnswer == nil {
		t.Fatalf("runTurn() = %+v, %v; want the second claim failure reported as leaving the answer unsaved", outcome, err)
	}
	if got := strings.Count(te.stdout.String(), "running it"); got != 2 {
		t.Fatalf("stdout = %q, want both answers presented", te.stdout.String())
	}
}

// A turn that fails leaves the error it ended with in the log file, next to
// the attempts before it, since the REPL and a single run print it only on
// the terminal. Here the stream's connection closes partway, as Argo's did
// on 2026-10-30, and the error names the stream it broke.
func TestRunTurnLogsTheErrorItEndsWith(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n5\r\n: hi\n\r\n")
		_ = buf.Flush()
	}))
	defer server.Close()
	te := newTurnTestEnvAt(t, server.URL, streamOn)

	_, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "hello"})
	if err == nil || err.Error() != "read response stream: unexpected EOF" {
		t.Fatalf("runTurn() error = %v, want read response stream: unexpected EOF", err)
	}
	var logged strings.Builder
	files, _ := filepath.Glob(filepath.Join(te.env.logDir, "*.log"))
	for _, file := range files {
		data, _ := os.ReadFile(file)
		logged.Write(data)
	}
	if !strings.Contains(logged.String(), "[ERROR]") || !regexp.MustCompile(`Request failed after \S+: read response stream: unexpected EOF`).MatchString(logged.String()) {
		t.Fatalf("log files = %q, want the turn's error at ERROR", logged.String())
	}
}

// The first session written in a directory of old sessions comes with a
// notice naming how many there are and how to remove them; later runs say
// nothing more.
func TestFirstWriteReportsOldSessions(t *testing.T) {
	server := mockserver.NewMockServer(mockserver.WithDefaultResponse("pong"))
	defer server.Close()
	te := newTurnTestEnv(t, server)
	old := filepath.Join(te.sessionsDir, "0001")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "0001.json"), []byte(`{"role":"user"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// The first commit fails after its session is made; the notice comes
	// all the same, and only once.
	commitForTest = func(ctx context.Context, plan *session.Plan) (*session.Session, error) {
		sess, err := plan.Commit(ctx)
		if err == nil {
			err = stdErrors.New("disk full")
		}
		return sess, err
	}
	t.Cleanup(func() { commitForTest = nil })
	if _, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "ping"}); err == nil {
		t.Fatal("the failed commit succeeded")
	}
	commitForTest = nil
	for i := 0; i < 2; i++ {
		if _, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "ping"}); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}
	notices := 0
	for _, warning := range te.notifier.WarnMessages {
		if strings.Contains(warning, "written by an older lmc") && strings.Contains(warning, "-delete") {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("warnings %q, want one upgrade notice", te.notifier.WarnMessages)
	}
}

// A commit that saves nothing in a resumed session leaves the turn with no
// session to report: the answer shown is unsaved, and nothing moved.
func TestRunTurnCommitThatSavesNothingReportsNoSession(t *testing.T) {
	server := mockserver.NewMockServer(mockserver.WithDefaultResponse("pong"))
	defer server.Close()
	te := newTurnTestEnv(t, server)
	sess := newTestSession(t, te.store(), textEntry(core.RoleUser, "q1"), textEntry(core.RoleAssistant, "a1"))
	te.env.cfg.Start = sess.Ref()
	if err := os.MkdirAll(filepath.Join(te.sessionsDir, sess.Number, "0003.content.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	outcome, err := runTurn(context.Background(), te.env, te.opts, turnInput{text: "q2"})
	if err == nil || outcome.UnsavedAnswer == nil || outcome.Session != nil {
		t.Fatalf("outcome %+v, error %v", outcome, err)
	}
	if newest, _ := te.store().Newest(sess.Number); newest != sess.Head {
		t.Fatalf("the failed commit left %s", newest)
	}
}

// A single run reports where it saved, and after a failure that left a
// question unanswered, how to get the answer; a commit that stopped part
// way says so in its own error.
func TestReportSavedNamesTheRecovery(t *testing.T) {
	store := session.Open(t.TempDir())
	answered := newTestSession(t, store, textEntry(core.RoleUser, "q"), textEntry(core.RoleAssistant, "partial"))
	asked := newTestSession(t, store, textEntry(core.RoleUser, "q"))
	partial := &session.SaveError{Saved: asked.Ref(), Next: "resume there with the same prompt to retry", Err: stdErrors.New("disk full")}
	results := newTestSession(t, store, textEntry(core.RoleUser, "q"))
	saveToolCalls(t, results, "", []core.ToolCall{{ID: "call", Name: "universal_command"}})
	if _, err := results.SaveToolResults(context.Background(), []core.ToolResult{{ID: "call", Output: "ran"}}, ""); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		out      turnOutcome
		err      error
		prompted bool
		want     string
	}{
		{turnOutcome{Session: answered, PlanCommitted: true}, nil, true, "Note: Saved at 0001/0002.\n"},
		{turnOutcome{Session: answered, PlanCommitted: true}, stdErrors.New("incomplete answer"), true, "Note: Saved at 0001/0002.\n"},
		{turnOutcome{Session: asked, PlanCommitted: true}, stdErrors.New("provider down"), true, "Note: Saved through 0002/0001, your question; resume there with empty input to get its answer.\n"},
		{turnOutcome{Session: results}, stdErrors.New("provider down"), true, "Note: Saved through 0003/0003, 1 tool result; resume there with the same prompt to retry.\n"},
		{turnOutcome{Session: results}, stdErrors.New("provider down"), false, "Note: Saved through 0003/0003, 1 tool result; resume there with empty input to get its answer.\n"},
		{turnOutcome{Session: asked}, fmt.Errorf("commit session: %w", partial), true, ""},
		{turnOutcome{}, stdErrors.New("provider down"), true, ""},
	} {
		var out bytes.Buffer
		reportSaved(ui.NewNotifierWithWriter(&out), tc.out, tc.err, tc.prompted)
		if out.String() != tc.want {
			t.Errorf("after %v: %q, want %q", tc.err, out.String(), tc.want)
		}
	}
}
