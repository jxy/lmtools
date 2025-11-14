//go:build integration

package main

import (
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/proxy"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenAIStreamingMode tests OpenAI SSE streaming functionality
func TestOpenAIStreamingMode(t *testing.T) {
	lmcBin := getLmcBinary(t)
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
		}
	}))
	defer server.Close()

	// Create a temporary API key file
	apiKeyFile := filepath.Join(tmpHome, "openai-key")
	if err := os.WriteFile(apiKeyFile, []byte("test-openai-key"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create API key file: %v", err)
	}

	// Disable sessions for streaming tests
	logDir := t.TempDir()
	_, stderr, err := runLmcCommand(t, lmcBin,
		[]string{
			"-provider", "openai",
			"-provider-url", server.URL + "/v1",
			"-api-key-file", apiKeyFile,
			"-stream",
			"-model", "gpt-5",
			"-no-session",
		},
		"Test OpenAI streaming", WithLogDir(logDir))
	if err != nil {
		t.Fatalf("Failed to run OpenAI streaming command: %v\nStderr: %s", err, stderr)
	}

	// Stronger validation: read stream_chat_output log and compare SSE chunks structurally
	var logContent string
	{
		entries, err := os.ReadDir(logDir)
		if err != nil {
			t.Fatalf("Failed to read log dir: %v", err)
		}
		var latestName string
		var latestMod int64
		for _, e := range entries {
			if strings.Contains(e.Name(), "stream_chat_output") && strings.HasSuffix(e.Name(), ".log") {
				info, err := e.Info()
				if err == nil {
					if info.ModTime().UnixNano() > latestMod {
						latestMod = info.ModTime().UnixNano()
						latestName = e.Name()
					}
				}
			}
		}
		if latestName == "" {
			t.Fatalf("No stream_chat_output log found in %s", logDir)
		}
		data, err := os.ReadFile(filepath.Join(logDir, latestName))
		if err != nil {
			t.Fatalf("Failed to read stream_chat_output log: %v", err)
		}
		logContent = string(data)
	}

	proxy.ValidateOpenAIStreamOutput(t, logContent, []string{
		`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-5","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-5","choices":[{"index":0,"delta":{"content":" from"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-5","choices":[{"index":0,"delta":{"content":" OpenAI"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-5","choices":[{"index":0,"delta":{"content":" streaming!"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	})

	// Check for stream_chat_output log file
	if !assertRecentLogFiles(t, logDir, "_stream_chat_output", ".log") {
		t.Error("stream_chat_output log file not found")
	}
}
