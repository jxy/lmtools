//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/session"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestOpenAIResponsesToolLoopAccumulatesInput(t *testing.T) {
	const rounds = 2
	tests := []struct {
		name      string
		noSession bool
	}{
		{name: "persistent"},
		{name: "no_session", noSession: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lmcBin := getLmcBinary(t)
			tmpHome := t.TempDir()
			t.Setenv("HOME", tmpHome)

			var mu sync.Mutex
			requestCount := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/responses" {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				var body map[string]interface{}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					writeResponsesTestError(w, http.StatusBadRequest, err)
					return
				}

				mu.Lock()
				requestCount++
				requestNumber := requestCount
				mu.Unlock()

				if err := validateResponsesToolHistory(body, requestNumber-1, rounds); err != nil {
					writeResponsesTestError(w, http.StatusBadRequest, err)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				if requestNumber <= rounds {
					writeResponsesToolCall(w, requestNumber)
					return
				}
				if requestNumber == rounds+1 {
					writeResponsesFinalText(w, fmt.Sprintf("responses final after %d round(s)", rounds))
					return
				}
				writeResponsesTestError(w, http.StatusBadRequest, fmt.Errorf("unexpected request %d", requestNumber))
			}))
			defer server.Close()

			apiKeyFile := writeTestAPIKeyFile(t, "test-openai-key")
			whitelistFile := writeToolListFile(t, tmpHome, "tool-whitelist.txt", "echo")

			sessionsDir := filepath.Join(tmpHome, "sessions")
			logDir := filepath.Join(tmpHome, "logs")
			args := []string{
				"-provider", "openai",
				"-provider-url", server.URL + "/v1",
				"-api-key-file", apiKeyFile,
				"-model", "gpt-5",
				"-openai-responses",
				"-tool",
				"-tool-whitelist", whitelistFile,
				"-tool-auto-approve",
				"-sessions-dir", sessionsDir,
			}
			if tt.noSession {
				args = append(args, "-no-session")
			}
			prompt := fmt.Sprintf("run %d responses tool round(s)", rounds)
			stdout, stderr, err := runLmcCommand(t, lmcBin, args, prompt, WithLogDir(logDir))
			if err != nil {
				t.Fatalf("lmc failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
			}
			wantFinal := fmt.Sprintf("responses final after %d round(s)", rounds)
			if !strings.Contains(stdout, wantFinal) {
				t.Fatalf("stdout = %q, want %q", stdout, wantFinal)
			}

			mu.Lock()
			gotRequests := requestCount
			mu.Unlock()
			if gotRequests != rounds+1 {
				t.Fatalf("provider requests = %d, want %d", gotRequests, rounds+1)
			}
			if tt.noSession {
				assertNoSessionFiles(t, sessionsDir)
				return
			}
			assertStoredResponsesToolLoop(t, sessionsDir, rounds)
		})
	}
}

func validateResponsesToolHistory(body map[string]interface{}, completedRounds, totalRounds int) error {
	if _, exists := body["messages"]; exists {
		return fmt.Errorf("Responses request unexpectedly contains messages")
	}
	tools, ok := body["tools"].([]interface{})
	if !ok || len(tools) == 0 {
		return fmt.Errorf("tools missing from Responses request")
	}
	input, ok := body["input"].([]interface{})
	if !ok {
		return fmt.Errorf("input has type %T", body["input"])
	}

	prompt := fmt.Sprintf("run %d responses tool round(s)", totalRounds)
	foundPrompt := false
	sequence := make([]string, 0, completedRounds*2)
	outputs := make(map[string]string, completedRounds)
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]interface{})
		if !ok {
			return fmt.Errorf("input item has type %T", rawItem)
		}
		switch item["type"] {
		case "message":
			if item["role"] == "user" && strings.Contains(fmt.Sprint(item["content"]), prompt) {
				foundPrompt = true
			}
		case "function_call":
			id, _ := item["call_id"].(string)
			if item["name"] != "universal_command" {
				return fmt.Errorf("function call %s name = %v", id, item["name"])
			}
			sequence = append(sequence, "call:"+id)
		case "function_call_output":
			id, _ := item["call_id"].(string)
			sequence = append(sequence, "result:"+id)
			outputs[id] = fmt.Sprint(item["output"])
		}
	}
	return verifyToolCallSequence(sequence, foundPrompt, completedRounds, outputs,
		func(round int) string { return fmt.Sprintf("response-call-%d", round) },
		func(round int) string { return fmt.Sprintf("responses-round-%d", round) })
}

func writeResponsesToolCall(w http.ResponseWriter, round int) {
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id":     fmt.Sprintf("resp-tool-%d", round),
		"object": "response",
		"status": "completed",
		"output": []map[string]interface{}{
			{
				"type":      "function_call",
				"call_id":   fmt.Sprintf("response-call-%d", round),
				"name":      "universal_command",
				"arguments": fmt.Sprintf(`{"command":["echo","responses-round-%d"]}`, round),
			},
		},
	})
}

func writeResponsesFinalText(w http.ResponseWriter, text string) {
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id":     "resp-final",
		"object": "response",
		"status": "completed",
		"output": []map[string]interface{}{
			{
				"type": "message",
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "output_text", "text": text},
				},
			},
		},
	})
}

func writeResponsesTestError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"type":    "invalid_request_error",
			"message": err.Error(),
		},
	})
}

func assertStoredResponsesToolLoop(t *testing.T, sessionsDir string, rounds int) {
	t.Helper()
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		t.Fatalf("read sessions directory: %v", err)
	}
	var sessionDirs []os.DirEntry
	for _, entry := range entries {
		if entry.IsDir() {
			sessionDirs = append(sessionDirs, entry)
		}
	}
	if len(sessionDirs) != 1 {
		t.Fatalf("session directories = %v from entries %v, want one", sessionDirs, entries)
	}

	manager := session.NewManager(sessionsDir)
	sessionPath := filepath.Join(sessionsDir, sessionDirs[0].Name())
	messages, err := session.BuildMessagesWithToolInteractionsWithManager(context.Background(), manager, sessionPath)
	if err != nil {
		t.Fatalf("build stored messages: %v", err)
	}
	if len(messages) != 2*rounds+3 {
		t.Fatalf("stored messages = %d, want %d", len(messages), 2*rounds+3)
	}
	if messages[0].Role != string(core.RoleSystem) || messages[1].Role != string(core.RoleUser) {
		t.Fatalf("stored leading roles = [%s %s]", messages[0].Role, messages[1].Role)
	}
	for round := 1; round <= rounds; round++ {
		assistant := messages[round*2]
		resultMessage := messages[round*2+1]
		if assistant.Role != string(core.RoleAssistant) || resultMessage.Role != string(core.RoleUser) {
			t.Fatalf("round %d roles = [%s %s]", round, assistant.Role, resultMessage.Role)
		}
		call, ok := assistant.Blocks[len(assistant.Blocks)-1].(core.ToolUseBlock)
		if !ok || call.ID != fmt.Sprintf("response-call-%d", round) {
			t.Fatalf("round %d stored call = %#v", round, assistant.Blocks)
		}
		result, ok := resultMessage.Blocks[0].(core.ToolResultBlock)
		if !ok || result.ToolUseID != call.ID || !strings.Contains(result.Content, fmt.Sprintf("responses-round-%d", round)) {
			t.Fatalf("round %d stored result = %#v", round, resultMessage.Blocks)
		}
	}
	final := messages[len(messages)-1]
	if final.Role != string(core.RoleAssistant) {
		t.Fatalf("final role = %s", final.Role)
	}
}
