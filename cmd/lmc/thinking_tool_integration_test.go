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

func TestAnthropicShowThinkingAcrossToolRounds(t *testing.T) {
	lmcBin := getLmcBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	var mu sync.Mutex
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		mu.Lock()
		requestCount++
		requestNumber := requestCount
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch requestNumber {
		case 1:
			_, _ = fmt.Fprint(w, `{"id":"msg_tool_1","type":"message","role":"assistant","model":"claude-opus-4-7","content":[{"type":"thinking","thinking":"First tool summary.","signature":"sig_tool_first"},{"type":"tool_use","id":"tool_1","name":"universal_command","input":{"command":["echo","thinking-loop-ok"]}}],"stop_reason":"tool_use"}`)
		case 2:
			encoded, err := json.Marshal(body["messages"])
			if err != nil {
				t.Errorf("marshal follow-up messages: %v", err)
			}
			followup := string(encoded)
			for _, want := range []string{
				`"type":"thinking"`,
				`"thinking":"First tool summary."`,
				`"signature":"sig_tool_first"`,
				`"type":"tool_result"`,
				"thinking-loop-ok",
			} {
				if !strings.Contains(followup, want) {
					t.Errorf("follow-up messages missing %q: %s", want, followup)
				}
			}
			_, _ = fmt.Fprint(w, `{"id":"msg_tool_2","type":"message","role":"assistant","model":"claude-opus-4-7","content":[{"type":"thinking","thinking":"Second tool summary.","signature":"sig_tool_second"},{"type":"text","text":"Final tool answer"}],"stop_reason":"end_turn"}`)
		default:
			http.Error(w, fmt.Sprintf("unexpected request %d", requestNumber), http.StatusBadRequest)
		}
	}))
	defer server.Close()

	apiKeyFile := writeTestAPIKeyFile(t, "test-anthropic-key")
	whitelistFile := writeToolListFile(t, tmpHome, "tool-whitelist.txt", "echo")
	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{
			"-provider", "anthropic",
			"-provider-url", server.URL + "/v1",
			"-api-key-file", apiKeyFile,
			"-model", "claude-opus-4-7",
			"-effort", "high",
			"-show-thinking",
			"-tool",
			"-tool-whitelist", whitelistFile,
			"-tool-auto-approve",
			"-no-session",
			"-sessions-dir", filepath.Join(tmpHome, "sessions"),
		},
		"Run the tool", WithLogDir(filepath.Join(tmpHome, "logs")))
	if err != nil {
		t.Fatalf("lmc tool loop failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if stdout != "Final tool answer" {
		t.Fatalf("stdout = %q, want final answer only", stdout)
	}
	first := strings.Index(stderr, "--- thinking ---\nFirst tool summary.\n--- end thinking ---\n\n")
	second := strings.Index(stderr, "--- thinking ---\nSecond tool summary.\n--- end thinking ---\n\n")
	if first < 0 || second <= first {
		t.Fatalf("stderr does not contain both tool-round summaries in order:\n%s", stderr)
	}
	for _, signature := range []string{"sig_tool_first", "sig_tool_second"} {
		if strings.Contains(stderr, signature) {
			t.Fatalf("stderr exposed thinking signature %q:\n%s", signature, stderr)
		}
	}

	mu.Lock()
	gotRequests := requestCount
	mu.Unlock()
	if gotRequests != 2 {
		t.Fatalf("provider requests = %d, want 2", gotRequests)
	}
}
