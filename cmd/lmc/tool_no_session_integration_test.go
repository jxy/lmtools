//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestNoSessionToolLoopUsesMemoryStore(t *testing.T) {
	lmcBin := getLmcBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	var mu sync.Mutex
	var requestBodies []map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		mu.Lock()
		requestBodies = append(requestBodies, body)
		requestNumber := len(requestBodies)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch requestNumber {
		case 1:
			writeOpenAIToolCallResponse(t, w)
		case 2:
			assertOpenAIToolFollowupFromMemory(t, body)
			writeOpenAITextResponse(t, w, "final saw memory-loop-ok")
		default:
			http.Error(w, fmt.Sprintf("unexpected request %d", requestNumber), http.StatusBadRequest)
		}
	}))
	defer server.Close()

	apiKeyFile := filepath.Join(tmpHome, "openai-key")
	if err := os.WriteFile(apiKeyFile, []byte("test-openai-key"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create API key file: %v", err)
	}

	whitelistFile := filepath.Join(tmpHome, "tool-whitelist.txt")
	if err := os.WriteFile(whitelistFile, []byte("[\"echo\"]\n"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create whitelist: %v", err)
	}

	sessionsDir := filepath.Join(tmpHome, "sessions")
	logDir := filepath.Join(tmpHome, "logs")
	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{
			"-provider", "openai",
			"-provider-url", server.URL + "/v1",
			"-api-key-file", apiKeyFile,
			"-model", "gpt-5",
			"-tool",
			"-tool-whitelist", whitelistFile,
			"-tool-auto-approve",
			"-no-session",
			"-sessions-dir", sessionsDir,
		},
		"run the memory loop command", WithLogDir(logDir))
	if err != nil {
		t.Fatalf("lmc failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, "final saw memory-loop-ok") {
		t.Fatalf("stdout missing final response: %q", stdout)
	}

	mu.Lock()
	gotRequests := len(requestBodies)
	firstBody := requestBodies[0]
	mu.Unlock()
	if gotRequests != 2 {
		t.Fatalf("got %d provider requests, want 2", gotRequests)
	}
	assertInitialOpenAIRequestHasTools(t, firstBody)
	assertNoSessionFiles(t, sessionsDir)
}

func writeOpenAIToolCallResponse(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	resp := map[string]interface{}{
		"id":      "chatcmpl-tool",
		"object":  "chat.completion",
		"created": 1,
		"model":   "gpt-5",
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "I will run the command.",
					"tool_calls": []map[string]interface{}{
						{
							"id":   "call_memory",
							"type": "function",
							"function": map[string]interface{}{
								"name":      "universal_command",
								"arguments": `{"command":["echo","memory-loop-ok"]}`,
							},
						},
					},
				},
				"finish_reason": "tool_calls",
			},
		},
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		t.Fatalf("encode tool response: %v", err)
	}
}

func writeOpenAITextResponse(t *testing.T, w http.ResponseWriter, text string) {
	t.Helper()
	resp := map[string]interface{}{
		"id":      "chatcmpl-final",
		"object":  "chat.completion",
		"created": 2,
		"model":   "gpt-5",
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": text,
				},
				"finish_reason": "stop",
			},
		},
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		t.Fatalf("encode final response: %v", err)
	}
}

func assertInitialOpenAIRequestHasTools(t *testing.T, body map[string]interface{}) {
	t.Helper()
	if _, ok := body["tools"].([]interface{}); !ok {
		t.Fatalf("initial request missing tools: %#v", body)
	}
	if !openAIRequestContainsRole(body, "user", "run the memory loop command") {
		t.Fatalf("initial request missing user prompt: %#v", body["messages"])
	}
}

func assertOpenAIToolFollowupFromMemory(t *testing.T, body map[string]interface{}) {
	t.Helper()
	if !openAIRequestContainsRole(body, "user", "run the memory loop command") {
		t.Fatalf("follow-up request missing original user prompt: %#v", body["messages"])
	}
	if !openAIRequestContainsRole(body, "assistant", "I will run the command.") {
		t.Fatalf("follow-up request missing assistant tool-call text: %#v", body["messages"])
	}
	if !openAIRequestContainsRole(body, "tool", "memory-loop-ok") {
		t.Fatalf("follow-up request missing in-memory tool result: %#v", body["messages"])
	}
}

func openAIRequestContainsRole(body map[string]interface{}, role string, contentSubstr string) bool {
	messages, ok := body["messages"].([]interface{})
	if !ok {
		return false
	}
	for _, raw := range messages {
		msg, ok := raw.(map[string]interface{})
		if !ok || msg["role"] != role {
			continue
		}
		if strings.Contains(fmt.Sprint(msg["content"]), contentSubstr) {
			return true
		}
	}
	return false
}

func assertNoSessionFiles(t *testing.T, sessionsDir string) {
	t.Helper()
	entries, err := os.ReadDir(sessionsDir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("read sessions dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("-no-session wrote session entries under %s: %v", sessionsDir, entries)
	}
}
