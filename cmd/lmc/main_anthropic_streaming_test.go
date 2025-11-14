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

// TestAnthropicStreamingMode tests Anthropic SSE streaming functionality
func TestAnthropicStreamingMode(t *testing.T) {
	lmcBin := getLmcBinary(t)
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
		}
	}))
	defer server.Close()

	// Create a temporary API key file
	apiKeyFile := filepath.Join(tmpHome, "anthropic-key")
	if err := os.WriteFile(apiKeyFile, []byte("test-anthropic-key"), constants.FilePerm); err != nil {
		t.Fatalf("Failed to create API key file: %v", err)
	}

	// Disable sessions for streaming tests
	logDir := t.TempDir()
	_, stderr, err := runLmcCommand(t, lmcBin,
		[]string{
			"-provider", "anthropic",
			"-provider-url", server.URL + "/v1",
			"-api-key-file", apiKeyFile,
			"-stream",
			"-model", "claude-opus-4-1-20250805",
			"-no-session",
		},
		"Test Anthropic streaming", WithLogDir(logDir))
	if err != nil {
		t.Fatalf("Failed to run Anthropic streaming command: %v\nStderr: %s", err, stderr)
	}

	// Stronger validation: compare SSE data frames structurally using the stream_chat_output log
	// lmc prints only text to stdout; detailed SSE frames are written to the log file.
	// Read latest stream_chat_output log and validate the data frames in order.
	// Find log file
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
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-4-1-20250805\",\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}",
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" from\"}}",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" Anthropic\"}}",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" streaming!\"}}",
		"data: {\"type\":\"content_block_stop\",\"index\":0}",
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":5}}",
		"data: {\"type\":\"message_stop\"}",
	})

	// Check for stream_chat_output log file
	if !assertRecentLogFiles(t, logDir, "_stream_chat_output", ".log") {
		t.Error("stream_chat_output log file not found")
	}
}
