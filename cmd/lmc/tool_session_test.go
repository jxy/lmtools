//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/mockserver"
	"lmtools/internal/session"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A round of tool calls is saved as a chain: the reply holding the calls,
// then their results, then the answer, each after the message before it.
// Resuming at the calls finds them pending, and at the results finds an
// answer awaited.
func TestToolRoundsSaveInOrder(t *testing.T) {
	store := session.Open(t.TempDir())
	sess := newTestSession(t, store, textEntry(core.RoleUser, "List the files in the current directory"))
	question := sess.Head
	calls := []core.ToolCall{{ID: "call-123", Name: "universal_command", Args: json.RawMessage(`{"command":["ls","-la"]}`)}}
	withCalls := saveToolCalls(t, sess, "I'll list the files in the current directory for you.", calls)
	if withCalls.Parent != question || !awaitsAnswer(t, sess) {
		t.Fatalf("calls %+v after %s; pending %t", withCalls, question, awaitsAnswer(t, sess))
	}
	results, err := sess.SaveToolResults(context.Background(), []core.ToolResult{{ID: "call-123", Output: "total 48"}}, "")
	if err != nil || results.Parent != withCalls.ID || !awaitsAnswer(t, sess) {
		t.Fatalf("results %+v: %v", results, err)
	}
	answer, err := sess.SaveReply(context.Background(), core.Response{Text: "Here are the files."}, "gpt5")
	if err != nil || answer.Parent != results.ID || awaitsAnswer(t, sess) {
		t.Fatalf("answer %+v: %v", answer, err)
	}
	h := historyAt(t, store, sess.Ref())
	var kinds []string
	for i := range h.Messages {
		kinds = append(kinds, session.Kind(h.Messages[i], h.Contents[i]))
	}
	want := "user input|assistant response and pending tool calls|1 tool result|assistant response"
	if got := strings.Join(kinds, "|"); got != want {
		t.Fatalf("history %q, want %q", got, want)
	}
	typed := h.Typed()
	if result, ok := typed[2].Blocks[0].(core.ToolResultBlock); !ok || result.Name != "universal_command" {
		t.Fatalf("the saved result %+v does not name its tool", typed[2].Blocks)
	}
}

// TestPendingToolsIntegration is a more comprehensive integration test
// that requires the binary to be built
func TestPendingToolsIntegration(t *testing.T) {
	// Get lmc binary
	binPath := getLmcBinary(t)

	// Create temp directories
	tempDir := t.TempDir()
	sessionsDir := filepath.Join(tempDir, "sessions")
	logDir := filepath.Join(tempDir, "logs")
	if err := os.MkdirAll(sessionsDir, constants.DirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logDir, constants.DirPerm); err != nil {
		t.Fatal(err)
	}

	// Create whitelist
	whitelistPath := filepath.Join(tempDir, "whitelist.txt")
	if err := os.WriteFile(whitelistPath, []byte(`["echo"]`+"\n"), constants.FilePerm); err != nil {
		t.Fatal(err)
	}

	// Start mock server
	ms := mockserver.NewMockServer()
	ms.SetResponseFunc(func(req *http.Request) (interface{}, int, error) {
		// Simple response for resuming session
		return map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": "Continuing the conversation after executing the pending tool.",
				},
			},
		}, 200, nil
	})
	defer ms.Close()
	serverURL := ms.Server.URL

	// Save the call the way the tool loop does: with an invocation identity,
	// claimed in the journal, and abandoned before it started.
	store := session.Open(sessionsDir)
	sess := newTestSession(t, store, textEntry(core.RoleUser, "Echo hello"))
	saveToolCalls(t, sess, "I'll echo that for you:", claimedCalls(t, store, []core.ToolCall{{
		ID:   "call-1",
		Name: "universal_command",
		Args: json.RawMessage(`{"command": ["echo", "hello from tool"]}`),
	}}))
	sessionID := sess.Number

	// Resume with lmc binary
	args := []string{
		"-provider", "anthropic", // Specify provider type
		"-provider-url", serverURL + "/messages",
		"-model", "claude-3-opus-20240229", // Use a valid model name
		"-resume", sessionID,
		"-sessions-dir", sessionsDir,
		"-log-dir", logDir,
		"-tool",
		"-tool-whitelist", whitelistPath,
		"-tool-auto-approve",
	}

	stdout, stderr, err := runLmcCommand(t, binPath, args, "continuing", WithLogDir(logDir))
	if err != nil {
		t.Fatalf("Failed to run continuation: %v\nStderr: %s", err, stderr)
	}

	// Log output for debugging
	t.Logf("stdout: %s", stdout)
	t.Logf("stderr (first 500 chars): %.500s", stderr)

	// Pending tools use the same ordered transcript as new tool rounds.
	if !strings.Contains(stderr, "Tools: 1") {
		t.Error("Expected to see pending tool review header")
	}
	if !strings.Contains(stderr, `[1/1] Command: ["echo","hello from tool"]`) {
		t.Error("Expected to see pending command")
	}
	if !strings.Contains(stderr, "Running 1 tool...") || !strings.Contains(stderr, "Tool results:") {
		t.Error("Expected to see pending tool execution and results headers")
	}

	if !strings.Contains(stderr, "hello from tool") {
		t.Error("Expected to see tool output 'hello from tool'")
	}

	// The result follows the call, and the prompt follows the result.
	h := historyAt(t, store, session.Ref{Session: sessionID, Message: newestMessage(t, store, sessionID)})
	n := len(h.Messages)
	results := h.Contents[n-3].Tools
	if n < 5 || results == nil || len(results.Results) != 1 || !strings.Contains(results.Results[0].Output, "hello from tool") || h.Messages[n-3].Parent != sess.Head {
		t.Fatalf("history after the resume: %+v", h.Messages)
	}
}

// newestMessage returns the ID of the newest message of session number.
func newestMessage(t *testing.T, store *session.Store, number string) string {
	t.Helper()
	newest, err := store.Newest(number)
	if err != nil {
		t.Fatal(err)
	}
	return newest
}

// A pending call whose journal entry is missing, as a lost or pruned journal
// leaves it, is uncertain: nothing records whether an earlier run executed
// it. A resume that cannot ask the operator sends an unknown outcome and
// does not run it, even with -tool-auto-approve.
func TestUnrecordedPendingToolIsNotRunAgainIntegration(t *testing.T) {
	binPath := getLmcBinary(t)

	tempDir := t.TempDir()
	sessionsDir := filepath.Join(tempDir, "sessions")
	logDir := filepath.Join(tempDir, "logs")
	if err := os.MkdirAll(logDir, constants.DirPerm); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(tempDir, "ran")

	ms := mockserver.NewMockServer()
	ms.SetResponseFunc(func(req *http.Request) (interface{}, int, error) {
		return map[string]interface{}{
			"content": []map[string]interface{}{{"type": "text", "text": "Understood."}},
		}, 200, nil
	})
	defer ms.Close()

	store := session.Open(sessionsDir)
	sess := newTestSession(t, store, textEntry(core.RoleUser, "Touch the marker"))
	calls := []core.ToolCall{{
		ID:   "call-unrecorded",
		Name: "universal_command",
		Args: json.RawMessage(fmt.Sprintf(`{"command": ["touch", %q]}`, marker)),
	}}
	core.AssignInvocationIDs(calls)
	saveToolCalls(t, sess, "Touching it:", calls)

	args := []string{
		"-provider", "anthropic",
		"-provider-url", ms.Server.URL + "/messages",
		"-model", "claude-3-opus-20240229",
		"-resume", sess.Number,
		"-sessions-dir", sessionsDir,
		"-log-dir", logDir,
		"-tool",
		"-tool-auto-approve",
	}
	_, stderr, err := runLmcCommand(t, binPath, args, "continuing", WithLogDir(logDir))
	if err != nil {
		t.Fatalf("Failed to run continuation: %v\nStderr: %s", err, stderr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the unrecorded pending call ran again, marker stat err = %v\nstderr: %s", err, stderr)
	}
	h := historyAt(t, store, session.Ref{Session: sess.Number, Message: newestMessage(t, store, sess.Number)})
	var sawUnknown bool
	for _, c := range h.Contents {
		if c.Tools == nil {
			continue
		}
		for _, result := range c.Tools.Results {
			if result.ID == "call-unrecorded" && result.Code == "OUTCOME_UNKNOWN" {
				sawUnknown = true
			}
		}
	}
	if !sawUnknown {
		t.Fatalf("no unknown outcome recorded for the call\nstderr: %s", stderr)
	}
}
