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

// TestOpenAIStreamingMode tests OpenAI SSE streaming functionality
func TestOpenAIStreamingMode(t *testing.T) {
	lmcBin := buildLmcBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	
	// Create a mock OpenAI streaming server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request path
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("Unexpected path: %s", r.URL.Path)
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		
		// Set SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		
		// Send OpenAI SSE format chunks
		chunks := []string{
			`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-5","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-5","choices":[{"index":0,"delta":{"content":" from"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-5","choices":[{"index":0,"delta":{"content":" OpenAI"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-5","choices":[{"index":0,"delta":{"content":" streaming!"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
		}
		
		for _, chunk := range chunks {
			fmt.Fprintf(w, "%s\n\n", chunk)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer server.Close()
	
	// Create a temporary API key file
	apiKeyFile := filepath.Join(tmpHome, "openai-key")
	if err := os.WriteFile(apiKeyFile, []byte("test-openai-key"), 0600); err != nil {
		t.Fatalf("Failed to create API key file: %v", err)
	}
	
	// Disable sessions for streaming tests
	stdout, stderr, logDir, err := runLmcCommandWithLogDir(t, lmcBin,
		[]string{
			"-provider", "openai",
			"-provider-url", server.URL + "/v1",
			"-api-key-file", apiKeyFile,
			"-stream",
			"-model", "gpt-5",
			"-no-session",
		},
		"Test OpenAI streaming")
	
	if err != nil {
		t.Fatalf("Failed to run OpenAI streaming command: %v\nStderr: %s", err, stderr)
	}
	
	// Verify the streamed output
	expectedOutput := "Hello from OpenAI streaming!"
	if !strings.Contains(stdout, expectedOutput) {
		t.Errorf("Expected streaming output to contain %q, got: %s", expectedOutput, stdout)
	}
	
	// Check for stream_chat_output log file
	if !assertRecentLogFiles(t, logDir, "_stream_chat_output", ".log") {
		t.Error("stream_chat_output log file not found")
	}
}