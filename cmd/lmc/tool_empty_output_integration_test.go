//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A command whose whole job is to produce no output must still complete the
// tool loop. The provider here rejects a contentless non-assistant message the
// way OpenAI does, so a dropped `content` key fails this test with the same
// error the CLI reported rather than passing quietly.
func TestToolLoopSurvivesCommandWithNoOutput(t *testing.T) {
	lmcBin := getLmcBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	var mu sync.Mutex
	var followupBody map[string]interface{}
	requests := 0

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
		requests++
		requestNumber := requests
		if requestNumber == 2 {
			followupBody = body
		}
		mu.Unlock()

		if msg, ok := firstContentlessNonAssistantMessage(body); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":{"code":400,"message":"All non-assistant messages must contain 'content' (%s)","type":"invalid_request_error"}}`, msg)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch requestNumber {
		case 1:
			writeOpenAIToolCallResponse(t, w, "call_silent",
				"Running a command that prints nothing.", `{"command":["true"]}`)
		case 2:
			writeOpenAITextResponse(t, w, "final saw the silent command")
		default:
			http.Error(w, fmt.Sprintf("unexpected request %d", requestNumber), http.StatusBadRequest)
		}
	}))
	defer server.Close()

	apiKeyFile := writeTestAPIKeyFile(t, "test-openai-key")
	whitelistFile := writeToolListFile(t, tmpHome, "tool-whitelist.txt", "true")

	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{
			"-provider", "openai",
			"-provider-url", server.URL + "/v1",
			"-api-key-file", apiKeyFile,
			"-model", "gpt-5",
			"-tool",
			"-tool-whitelist", whitelistFile,
			"-tool-auto-approve",
			"-sessions-dir", filepath.Join(tmpHome, "sessions"),
		},
		"run a command that prints nothing", WithLogDir(filepath.Join(tmpHome, "logs")))
	if err != nil {
		t.Fatalf("lmc failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "final saw the silent command") {
		t.Fatalf("stdout missing final response: %q", stdout)
	}

	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	toolMsg, ok := lastMessageWithRole(followupBody, "tool")
	if !ok {
		t.Fatalf("follow-up carried no tool message: %#v", followupBody["messages"])
	}
	content, present := toolMsg["content"]
	if !present {
		t.Fatalf("tool message = %#v, want an explicit content field", toolMsg)
	}
	if content != "" {
		t.Fatalf("content = %#v, want the empty string for a command with no output", content)
	}
}

// firstContentlessNonAssistantMessage applies the provider-side rule the CLI
// tripped over: every message except an assistant turn must carry content.
func firstContentlessNonAssistantMessage(body map[string]interface{}) (string, bool) {
	messages, _ := body["messages"].([]interface{})
	for i, raw := range messages {
		msg, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if msg["role"] == "assistant" {
			continue
		}
		if _, present := msg["content"]; !present {
			return fmt.Sprintf("messages[%d].role=%v", i, msg["role"]), true
		}
	}
	return "", false
}

func lastMessageWithRole(body map[string]interface{}, role string) (map[string]interface{}, bool) {
	messages, _ := body["messages"].([]interface{})
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]interface{})
		if ok && msg["role"] == role {
			return msg, true
		}
	}
	return nil, false
}
