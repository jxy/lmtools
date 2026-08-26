//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func writeOpenAIResponseWithUsage(t *testing.T, w http.ResponseWriter, message map[string]interface{}, finishReason string, promptTokens, completionTokens int) {
	t.Helper()
	resp := map[string]interface{}{
		"id":      "chatcmpl-usage",
		"object":  "chat.completion",
		"created": 1,
		"model":   "gpt-5",
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]int{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

// TestTokenUsageNoteOnPlainChat covers the operator-visible note end to end:
// a response carrying usage produces exactly one stderr note rendered from the
// provider's counts, and stdout stays the model's text alone.
func TestTokenUsageNoteOnPlainChat(t *testing.T) {
	lmcBin := getLmcBinary(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeOpenAIResponseWithUsage(t, w,
			map[string]interface{}{"role": "assistant", "content": "plain answer"},
			"stop", 100, 7)
	}))
	defer server.Close()

	apiKeyFile := writeTestAPIKeyFile(t, "test-openai-key")
	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{
			"-provider", "openai",
			"-provider-url", server.URL + "/v1",
			"-api-key-file", apiKeyFile,
			"-model", "gpt-5",
			"-no-session",
		},
		"say something")
	if err != nil {
		t.Fatalf("lmc failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	if stdout != "plain answer" {
		t.Fatalf("stdout = %q, want the model text alone", stdout)
	}
	wantNote := "Note: Token usage: input 100, output 7, total 107"
	if got := strings.Count(stderr, "Note: Token usage:"); got != 1 {
		t.Fatalf("stderr carries %d usage notes, want 1:\n%s", got, stderr)
	}
	if !strings.Contains(stderr, wantNote) {
		t.Fatalf("stderr missing %q:\n%s", wantNote, stderr)
	}
}

// TestTokenUsageNoteOnAnthropicStream covers the streaming path, where the
// counts arrive split across events: message_start reports the input tokens,
// message_delta the final output and thinking tokens, and the note renders
// their merge once the stream has been handled.
func TestTokenUsageNoteOnAnthropicStream(t *testing.T) {
	lmcBin := getLmcBinary(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`event: message_start
data: {"type":"message_start","message":{"id":"msg_1","role":"assistant","usage":{"input_tokens":100,"output_tokens":1,"output_tokens_details":{"thinking_tokens":3}}}}`,
			`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"streamed answer"}}`,
			`event: content_block_stop
data: {"type":"content_block_stop","index":0}`,
			`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":19,"output_tokens_details":{"thinking_tokens":12}}}`,
			`event: message_stop
data: {"type":"message_stop"}`,
		}
		for _, event := range events {
			fmt.Fprintf(w, "%s\n\n", event)
		}
	}))
	defer server.Close()

	apiKeyFile := writeTestAPIKeyFile(t, "test-anthropic-key")
	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{
			"-provider", "anthropic",
			"-provider-url", server.URL,
			"-api-key-file", apiKeyFile,
			"-model", "claude-sonnet-5",
			"-stream",
			"-no-session",
		},
		"say something")
	if err != nil {
		t.Fatalf("lmc failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, "streamed answer") {
		t.Fatalf("stdout missing streamed text: %q", stdout)
	}
	wantNote := "Note: Token usage: input 100, output 19, reasoning 12"
	if !strings.Contains(stderr, wantNote) {
		t.Fatalf("stderr missing %q:\n%s", wantNote, stderr)
	}
}

// TestTokenUsageNotePrecedesToolReview pins the placement the note exists
// for: it reaches the operator as soon as the response is handled, before the
// command review and execution that response triggers, and again for the
// follow-up response of each round.
func TestTokenUsageNotePrecedesToolReview(t *testing.T) {
	lmcBin := getLmcBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	var mu sync.Mutex
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		mu.Lock()
		requestCount++
		requestNumber := requestCount
		mu.Unlock()

		switch requestNumber {
		case 1:
			writeOpenAIResponseWithUsage(t, w,
				map[string]interface{}{
					"role":    "assistant",
					"content": "running the command",
					"tool_calls": []map[string]interface{}{
						{
							"id":   "call_usage",
							"type": "function",
							"function": map[string]interface{}{
								"name":      "universal_command",
								"arguments": `{"command":["echo","usage-note-ok"]}`,
							},
						},
					},
				},
				"tool_calls", 120, 30)
		case 2:
			writeOpenAIResponseWithUsage(t, w,
				map[string]interface{}{"role": "assistant", "content": "final answer"},
				"stop", 180, 12)
		default:
			http.Error(w, fmt.Sprintf("unexpected request %d", requestNumber), http.StatusBadRequest)
		}
	}))
	defer server.Close()

	apiKeyFile := writeTestAPIKeyFile(t, "test-openai-key")
	whitelistFile := writeToolListFile(t, tmpHome, "tool-whitelist.txt", "echo")

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
		},
		"run the usage note command")
	if err != nil {
		t.Fatalf("lmc failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "final answer") {
		t.Fatalf("stdout missing final response: %q", stdout)
	}

	firstNote := strings.Index(stderr, "Note: Token usage: input 120, output 30, total 150")
	review := strings.Index(stderr, ">>> Tools requested: 1")
	running := strings.Index(stderr, ">>> Running 1 command")
	results := strings.Index(stderr, ">>> Results:")
	secondNote := strings.Index(stderr, "Note: Token usage: input 180, output 12, total 192")
	for name, index := range map[string]int{
		"first usage note":  firstNote,
		"tool review":       review,
		"execution banner":  running,
		"results banner":    results,
		"second usage note": secondNote,
	} {
		if index < 0 {
			t.Fatalf("stderr missing %s:\n%s", name, stderr)
		}
	}

	if firstNote > review {
		t.Fatalf("first usage note (%d) must precede the tool review (%d):\n%s", firstNote, review, stderr)
	}
	if review > running || running > results {
		t.Fatalf("tool transcript out of order (review %d, running %d, results %d):\n%s", review, running, results, stderr)
	}
	if secondNote < results {
		t.Fatalf("follow-up usage note (%d) must follow the results banner (%d):\n%s", secondNote, results, stderr)
	}
}
