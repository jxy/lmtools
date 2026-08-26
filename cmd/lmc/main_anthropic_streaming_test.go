//go:build integration

package main

import (
	"encoding/json"
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

func TestAnthropicAdaptiveThinkingNonStreaming(t *testing.T) {
	lmcBin := getLmcBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Thinking struct {
				Type    string `json:"type"`
				Display string `json:"display"`
			} `json:"thinking"`
			OutputConfig struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if request.Thinking.Type != "adaptive" || request.Thinking.Display != "summarized" {
			t.Errorf("thinking = %+v, want adaptive summarized", request.Thinking)
		}
		if request.OutputConfig.Effort != "high" {
			t.Errorf("output_config.effort = %q, want high", request.OutputConfig.Effort)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-7","content":[{"type":"thinking","thinking":"I should answer clearly.","signature":"sig_json"},{"type":"text","text":"Final answer"}],"stop_reason":"end_turn"}`)
	}))
	defer server.Close()

	apiKeyFile := filepath.Join(tmpHome, "anthropic-key")
	if err := os.WriteFile(apiKeyFile, []byte("test-anthropic-key"), constants.FilePerm); err != nil {
		t.Fatalf("write API key: %v", err)
	}

	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{
			"-provider", "anthropic",
			"-provider-url", server.URL + "/v1",
			"-api-key-file", apiKeyFile,
			"-model", "claude-opus-4-7",
			"-effort", "high",
			"-no-session",
		},
		"Test Anthropic non-streaming", WithTempLogDir(t))
	if err != nil {
		t.Fatalf("lmc non-streaming failed: %v\nstderr: %s", err, stderr)
	}
	if stdout != "Final answer" {
		t.Fatalf("stdout = %q, want final answer text", stdout)
	}
	for _, hidden := range []string{"--- thinking ---", "I should answer clearly.", "sig_json"} {
		if strings.Contains(stderr, hidden) {
			t.Fatalf("default stderr unexpectedly contains %q: %s", hidden, stderr)
		}
	}

	stdout, stderr, err = runLmcCommand(t, lmcBin,
		[]string{
			"-provider", "anthropic",
			"-provider-url", server.URL + "/v1",
			"-api-key-file", apiKeyFile,
			"-model", "claude-opus-4-7",
			"-effort", "high",
			"-show-thinking",
			"-no-session",
		},
		"Test Anthropic non-streaming", WithTempLogDir(t))
	if err != nil {
		t.Fatalf("lmc non-streaming with thinking failed: %v\nstderr: %s", err, stderr)
	}
	if stdout != "Final answer" {
		t.Fatalf("stdout with -show-thinking = %q, want final answer text only", stdout)
	}
	if !strings.Contains(stderr, "--- thinking ---\nI should answer clearly.\n--- end thinking ---\n\n") {
		t.Fatalf("stderr missing thinking summary: %s", stderr)
	}
	if strings.Contains(stderr, "sig_json") {
		t.Fatalf("stderr exposed Anthropic thinking signature: %s", stderr)
	}
}

func TestAnthropicShowThinkingPreservesJSONStdout(t *testing.T) {
	lmcBin := getLmcBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_json","type":"message","role":"assistant","model":"claude-opus-4-7","content":[{"type":"thinking","thinking":"Build valid JSON.","signature":"sig_json_mode"},{"type":"text","text":"{\"answer\":\"ok\"}"}],"stop_reason":"end_turn"}`)
	}))
	defer server.Close()

	apiKeyFile := filepath.Join(tmpHome, "anthropic-key")
	if err := os.WriteFile(apiKeyFile, []byte("test-anthropic-key"), constants.FilePerm); err != nil {
		t.Fatalf("write API key: %v", err)
	}

	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{
			"-provider", "anthropic",
			"-provider-url", server.URL + "/v1",
			"-api-key-file", apiKeyFile,
			"-model", "claude-opus-4-7",
			"-effort", "high",
			"-json",
			"-show-thinking",
			"-no-session",
		},
		"Return JSON", WithTempLogDir(t))
	if err != nil {
		t.Fatalf("lmc JSON response failed: %v\nstderr: %s", err, stderr)
	}
	if !json.Valid([]byte(stdout)) || stdout != `{"answer":"ok"}` {
		t.Fatalf("stdout = %q, want valid answer JSON only", stdout)
	}
	if !strings.Contains(stderr, "--- thinking ---\nBuild valid JSON.\n--- end thinking ---\n\n") {
		t.Fatalf("stderr missing JSON-mode thinking summary: %s", stderr)
	}
	if strings.Contains(stderr, "sig_json_mode") {
		t.Fatalf("stderr exposed JSON-mode thinking signature: %s", stderr)
	}
}

func TestAnthropicShowThinkingReportsWhenAdaptiveThinkingReturnsNoBlock(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "non-streaming"
		if stream {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			lmcBin := getLmcBinary(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Thinking struct {
						Type    string `json:"type"`
						Display string `json:"display"`
					} `json:"thinking"`
					OutputConfig struct {
						Effort string `json:"effort"`
					} `json:"output_config"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}
				if request.Thinking.Type != "adaptive" || request.Thinking.Display != "summarized" || request.OutputConfig.Effort != "high" {
					t.Errorf("thinking request = %+v, output_config = %+v; want adaptive summarized at high effort", request.Thinking, request.OutputConfig)
				}

				if !stream {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"id":"msg_no_thinking","type":"message","role":"assistant","model":"claude-opus-4-7","content":[{"type":"text","text":"No-thinking answer"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":3}}`)
					return
				}

				w.Header().Set("Content-Type", "text/event-stream")
				events := []struct {
					event string
					data  string
				}{
					{"message_start", `{"type":"message_start","message":{"id":"msg_no_thinking","role":"assistant","usage":{"input_tokens":10,"output_tokens":0}}}`},
					{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
					{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"No-thinking answer"}}`},
					{"content_block_stop", `{"type":"content_block_stop","index":0}`},
					{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`},
					{"message_stop", `{"type":"message_stop"}`},
				}
				for _, evt := range events {
					fmt.Fprintf(w, "event: %s\n", evt.event)
					fmt.Fprintf(w, "data: %s\n\n", evt.data)
				}
			}))
			defer server.Close()

			apiKeyFile := writeTestAPIKeyFile(t, "test-anthropic-key")
			args := []string{
				"-provider", "anthropic",
				"-provider-url", server.URL + "/v1",
				"-api-key-file", apiKeyFile,
				"-model", "claude-opus-4-7",
				"-effort", "high",
				"-show-thinking",
				"-no-session",
			}
			if stream {
				args = append(args, "-stream")
			}
			stdout, stderr, err := runLmcCommand(t, lmcBin, args, "Simple question")
			if err != nil {
				t.Fatalf("lmc failed: %v\nstderr: %s", err, stderr)
			}
			if stdout != "No-thinking answer" {
				t.Fatalf("stdout = %q, want answer only", stdout)
			}
			if got := strings.Count(stderr, "No visible thinking summary was returned for this response."); got != 1 {
				t.Fatalf("missing-thinking notes = %d, want 1:\n%s", got, stderr)
			}
			if strings.Contains(stderr, thinkingStartMarker) {
				t.Fatalf("stderr rendered a thinking block the provider did not return:\n%s", stderr)
			}
		})
	}
}

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
		var request struct {
			Thinking struct {
				Type    string `json:"type"`
				Display string `json:"display"`
			} `json:"thinking"`
			OutputConfig struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if request.Thinking.Type != "adaptive" || request.Thinking.Display != "summarized" {
			t.Errorf("thinking = %+v, want adaptive summarized", request.Thinking)
		}
		if request.OutputConfig.Effort != "high" {
			t.Errorf("output_config.effort = %q, want high", request.OutputConfig.Effort)
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
			{"message_start", `{"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[],"model":"claude-opus-4-7","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}`},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"I should answer clearly."}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_stream"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello"}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":" from"}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":" Anthropic"}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":" streaming!"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":1}`},
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
	stdout, stderr, err := runLmcCommand(t, lmcBin,
		[]string{
			"-provider", "anthropic",
			"-provider-url", server.URL + "/v1",
			"-api-key-file", apiKeyFile,
			"-stream",
			"-model", "claude-opus-4-7",
			"-effort", "high",
			"-show-thinking",
			"-no-session",
		},
		"Test Anthropic streaming", WithLogDir(logDir))
	if err != nil {
		t.Fatalf("Failed to run Anthropic streaming command: %v\nStderr: %s", err, stderr)
	}
	if stdout != "Hello from Anthropic streaming!" {
		t.Fatalf("streaming stdout = %q, want answer text only", stdout)
	}
	if !strings.Contains(stderr, "--- thinking ---\nI should answer clearly.\n--- end thinking ---\n\n") {
		t.Fatalf("streaming stderr missing thinking summary: %s", stderr)
	}
	if strings.Contains(stderr, "sig_stream") {
		t.Fatalf("streaming stderr exposed thinking signature: %s", stderr)
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
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-4-7\",\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}",
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"I should answer clearly.\"}}",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig_stream\"}}",
		"data: {\"type\":\"content_block_stop\",\"index\":0}",
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}",
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\" from\"}}",
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\" Anthropic\"}}",
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\" streaming!\"}}",
		"data: {\"type\":\"content_block_stop\",\"index\":1}",
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":5}}",
		"data: {\"type\":\"message_stop\"}",
	})

	// Check for stream_chat_output log file
	if !assertRecentLogFiles(t, logDir, "_stream_chat_output", ".log") {
		t.Error("stream_chat_output log file not found")
	}
}
