//go:build integration

package main

import (
	"encoding/json"
	"lmtools/internal/core"
	"lmtools/internal/mockserver"
	"lmtools/internal/session"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPrintCurlFreshDoesNotSendOrCreateSession(t *testing.T) {
	lmcBin := getLmcBinary(t)
	server := mockserver.NewMockServer()
	t.Cleanup(server.Close)
	sessionsDir := t.TempDir()

	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "testuser", "-model", "gpt4o", "-provider-url", server.URL(), "-sessions-dir", sessionsDir, "-print-curl"},
		"fresh preview")
	if err != nil {
		t.Fatalf("lmc -print-curl failed: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "curl -X POST") || !strings.Contains(stdout, "fresh preview") {
		t.Fatalf("stdout = %q, want curl command containing request body", stdout)
	}
	if !strings.Contains(stdout, "-H 'Authorization: Bearer testuser'") {
		t.Fatalf("stdout = %q, want Argo user Authorization header", stdout)
	}
	if got := len(server.GetRequests()); got != 0 {
		t.Fatalf("mock server received %d requests, want 0", got)
	}
	if entries, err := os.ReadDir(sessionsDir); err != nil {
		t.Fatalf("ReadDir(%s) error = %v", sessionsDir, err)
	} else if len(entries) != 0 {
		t.Fatalf("sessions dir entries = %d, want 0", len(entries))
	}
}

// The thinking display, on by default, with a gpt model on Argo's chat route
// says how to get thinking; on the Responses route, the default, an effort
// asks for the summary.
func TestPrintCurlShowThinkingOnArgoGPT(t *testing.T) {
	lmcBin := getLmcBinary(t)
	server := mockserver.NewMockServer()
	t.Cleanup(server.Close)
	args := []string{"-argo-user", "testuser", "-model", "gpt6astra", "-provider-url", server.URL(), "-sessions-dir", t.TempDir(), "-print-curl"}
	const warning = "Warning: Argo's chat completions route returns no thinking for gpt6astra; without -chat-completions, the Responses API returns reasoning summaries with -effort"

	stdout, stderr, err := runLmcCommand(t, lmcBin, append(args, "-chat-completions"), "think", WithLmcDefaults())
	if err != nil || !strings.Contains(stderr, warning) || !strings.Contains(stdout, "/chat/completions") {
		t.Fatalf("chat route: err=%v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	stdout, stderr, err = runLmcCommand(t, lmcBin, append(args, "-effort", "high"), "think", WithLmcDefaults())
	if err != nil || strings.Contains(stderr, "no thinking") || !strings.Contains(stdout, `"reasoning":{"effort":"high","summary":"auto"}`) {
		t.Fatalf("Responses route: err=%v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if got := len(server.GetRequests()); got != 0 {
		t.Fatalf("mock server received %d requests, want 0", got)
	}
}

func TestPrintCurlResumeIsReadOnly(t *testing.T) {
	lmcBin := getLmcBinary(t)
	server := mockserver.NewMockServer(mockserver.WithDefaultResponse("resume response"))
	t.Cleanup(server.Close)
	sessionsDir := t.TempDir()

	_, stderr, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "testuser", "-model", "gpt4o", "-provider-url", server.URL(), "-sessions-dir", sessionsDir},
		"Initial message")
	if err != nil {
		t.Fatalf("create session failed: %v\nstderr: %s", err, stderr)
	}
	sessionID := firstSessionIDForTest(t, lmcBin, sessionsDir)
	before := snapshotDir(t, sessionsDir)
	server.Reset()

	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "testuser", "-model", "gpt4o", "-provider-url", server.URL(), "-sessions-dir", sessionsDir, "-resume", sessionID, "-print-curl"},
		"Follow-up preview")
	if err != nil {
		t.Fatalf("lmc -print-curl -resume failed: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "Initial message") || !strings.Contains(stdout, "Follow-up preview") {
		t.Fatalf("stdout = %q, want prior history and preview input", stdout)
	}
	if got := len(server.GetRequests()); got != 0 {
		t.Fatalf("mock server received %d requests, want 0", got)
	}
	assertSnapshotEqual(t, before, snapshotDir(t, sessionsDir))
}

// Resumed at the question with empty input, a preview asks for the
// question's answer again without the answer it has, and writes nothing.
func TestPrintCurlAnswerAgainIsReadOnly(t *testing.T) {
	lmcBin := getLmcBinary(t)
	server := mockserver.NewMockServer(mockserver.WithDefaultResponse("assistant response"))
	t.Cleanup(server.Close)
	sessionsDir := t.TempDir()

	_, stderr, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "testuser", "-model", "gpt4o", "-provider-url", server.URL(), "-sessions-dir", sessionsDir},
		"Initial message")
	if err != nil {
		t.Fatalf("create session failed: %v\nstderr: %s", err, stderr)
	}
	sessionID := firstSessionIDForTest(t, lmcBin, sessionsDir)
	before := snapshotDir(t, sessionsDir)
	server.Reset()

	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "testuser", "-model", "gpt4o", "-provider-url", server.URL(), "-sessions-dir", sessionsDir, "-resume", sessionID + "/0002", "-print-curl"},
		"")
	if err != nil {
		t.Fatalf("lmc -print-curl -resume at the question failed: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "Initial message") {
		t.Fatalf("stdout = %q, want prior user message", stdout)
	}
	if strings.Contains(stdout, "assistant response") {
		t.Fatalf("stdout = %q, want the question without its answer", stdout)
	}
	if got := len(server.GetRequests()); got != 0 {
		t.Fatalf("mock server received %d requests, want 0", got)
	}
	assertSnapshotEqual(t, before, snapshotDir(t, sessionsDir))
}

func TestPrintCurlResumeWithPendingToolsUsesPlaceholderWithoutExecuting(t *testing.T) {
	lmcBin := getLmcBinary(t)
	server := mockserver.NewMockServer()
	t.Cleanup(server.Close)
	sessionsDir := t.TempDir()
	sessionID := createPendingToolSession(t, sessionsDir)
	before := snapshotDir(t, sessionsDir)

	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "testuser", "-model", "gpt4o", "-provider-url", server.URL(), "-sessions-dir", sessionsDir, "-resume", sessionID, "-tool", "-print-curl"},
		"")
	if err != nil {
		t.Fatalf("lmc -print-curl -resume with pending tools failed: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "[print-curl placeholder]") ||
		!strings.Contains(stdout, "universal_command") ||
		!strings.Contains(stdout, "call-1") {
		t.Fatalf("stdout = %q, want placeholder tool result", stdout)
	}
	if got := len(server.GetRequests()); got != 0 {
		t.Fatalf("mock server received %d requests, want 0", got)
	}
	assertSnapshotEqual(t, before, snapshotDir(t, sessionsDir))
}

func TestPrintCurlResumeWithPendingToolsRequiresToolFlag(t *testing.T) {
	lmcBin := getLmcBinary(t)
	server := mockserver.NewMockServer()
	t.Cleanup(server.Close)
	sessionsDir := t.TempDir()
	sessionID := createPendingToolSession(t, sessionsDir)
	before := snapshotDir(t, sessionsDir)

	_, stderr, err := runLmcCommand(t, lmcBin,
		[]string{"-argo-user", "testuser", "-model", "gpt4o", "-provider-url", server.URL(), "-sessions-dir", sessionsDir, "-resume", sessionID, "-print-curl"},
		"")
	if err == nil {
		t.Fatal("lmc -print-curl -resume with pending tools succeeded without -tool")
	}
	if !strings.Contains(stderr, "pending tool calls require -tool") {
		t.Fatalf("stderr = %q, want pending tool error", stderr)
	}
	if got := len(server.GetRequests()); got != 0 {
		t.Fatalf("mock server received %d requests, want 0", got)
	}
	assertSnapshotEqual(t, before, snapshotDir(t, sessionsDir))
}

func firstSessionIDForTest(t *testing.T, lmcBin, sessionsDir string) string {
	t.Helper()
	stdout, stderr, err := runLmcCommand(t, lmcBin, []string{"-argo-user", "testuser", "-show-sessions", "-sessions-dir", sessionsDir}, "")
	if err != nil {
		t.Fatalf("show sessions failed: %v\nstderr: %s", err, stderr)
	}
	sessionID := extractFirstSessionID(stdout)
	if sessionID == "" {
		t.Fatalf("failed to extract session ID from output: %s", stdout)
	}
	return sessionID
}

func createPendingToolSession(t *testing.T, sessionsDir string) string {
	t.Helper()
	sess := newTestSession(t, session.Open(sessionsDir), textEntry(core.RoleUser, "run a tool"))
	saveToolCalls(t, sess, "I will run a tool", []core.ToolCall{{
		ID:   "call-1",
		Name: "universal_command",
		Args: json.RawMessage(`{"command":["echo","hi"]}`),
	}})
	return sess.Number
}

func snapshotDir(t *testing.T, root string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotDir(%s) error = %v", root, err)
	}
	return out
}

func assertSnapshotEqual(t *testing.T, before, after map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("session files changed\nbefore=%#v\nafter=%#v", before, after)
	}
}
