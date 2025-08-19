//go:build integration
// +build integration

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAnthropicStreamingMode tests Anthropic SSE streaming functionality
func TestAnthropicStreamingMode(t *testing.T) {
	lmcBin := buildLmcBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	
	// Create a mock Anthropic streaming server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request path
		if r.URL.Path != "/v1/messages" {
			t.Errorf("Unexpected path: %s", r.URL.Path)
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		
		// Set SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		
		// Send Anthropic SSE format chunks
		events := []struct {
			event string
			data  string
		}{
			{"message_start", `{"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[],"model":"claude-opus-4-1-20250805","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}`},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" from"}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Anthropic"}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" streaming!"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`},
			{"message_stop", `{"type":"message_stop"}`},
		}
		
		for _, evt := range events {
			fmt.Fprintf(w, "event: %s\n", evt.event)
			fmt.Fprintf(w, "data: %s\n\n", evt.data)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer server.Close()
	
	// Create a temporary API key file
	apiKeyFile := filepath.Join(tmpHome, "anthropic-key")
	if err := os.WriteFile(apiKeyFile, []byte("test-anthropic-key"), 0600); err != nil {
		t.Fatalf("Failed to create API key file: %v", err)
	}
	
	// Disable sessions for streaming tests
	stdout, stderr, logDir, err := runLmcCommandWithLogDir(t, lmcBin,
		[]string{
			"-provider", "anthropic",
			"-provider-url", server.URL + "/v1",
			"-api-key-file", apiKeyFile,
			"-stream",
			"-model", "claude-opus-4-1-20250805",
			"-no-session",
		},
		"Test Anthropic streaming")
	
	if err != nil {
		t.Fatalf("Failed to run Anthropic streaming command: %v\nStderr: %s", err, stderr)
	}
	
	// Verify the streamed output
	expectedOutput := "Hello from Anthropic streaming!"
	if !strings.Contains(stdout, expectedOutput) {
		t.Errorf("Expected streaming output to contain %q, got: %s", expectedOutput, stdout)
	}
	
	// Check for stream_chat_output log file
	if !assertRecentLogFiles(t, logDir, "_stream_chat_output", ".log") {
		t.Error("stream_chat_output log file not found")
	}
}