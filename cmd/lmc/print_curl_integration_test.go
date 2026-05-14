//go:build integration

package main

import (
	"context"
	"encoding/json"
	"lmtools/internal/core"
	"lmtools/internal/mockserver"
	"lmtools/internal/session"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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
	if got := len(server.GetRequests()); got != 0 {
		t.Fatalf("mock server received %d requests, want 0", got)
	}
	if entries, err := os.ReadDir(sessionsDir); err != nil {
		t.Fatalf("ReadDir(%s) error = %v", sessionsDir, err)
	} else if len(entries) != 0 {
		t.Fatalf("sessions dir entries = %d, want 0", len(entries))
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

func TestPrintCurlBranchAssistantIsReadOnly(t *testing.T) {
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
		[]string{"-argo-user", "testuser", "-model", "gpt4o", "-provider-url", server.URL(), "-sessions-dir", sessionsDir, "-branch", sessionID + "/0002", "-print-curl"},
		"ignored by assistant regeneration")
	if err != nil {
		t.Fatalf("lmc -print-curl -branch failed: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "Initial message") {
		t.Fatalf("stdout = %q, want prior user message", stdout)
	}
	if strings.Contains(stdout, "assistant response") {
		t.Fatalf("stdout = %q, assistant regeneration should exclude target assistant", stdout)
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
	oldDir := session.GetSessionsDir()
	session.SetSessionsDir(sessionsDir)
	t.Cleanup(func() { session.SetSessionsDir(oldDir) })

	ctx := context.Background()
	sess, err := session.CreateSession("system", core.NewTestLogger(false))
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	appendTestSessionMessage(t, ctx, sess, core.RoleUser, "run a tool")
	_, err = session.AppendMessageWithToolInteraction(ctx, sess, session.Message{
		Role:      core.RoleAssistant,
		Content:   "I will run a tool",
		Timestamp: time.Now(),
		Model:     "gpt4o",
	}, []core.ToolCall{{
		ID:   "call-1",
		Name: "universal_command",
		Args: json.RawMessage(`{"command":["echo","hi"]}`),
	}}, nil)
	if err != nil {
		t.Fatalf("AppendMessageWithToolInteraction() error = %v", err)
	}
	return session.GetSessionID(sess.Path)
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
