//go:build integration
// +build integration

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGoogleStreamingMode tests Google AI SSE streaming functionality
func TestGoogleStreamingMode(t *testing.T) {
	lmcBin := buildLmcBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	
	// Create a mock Google streaming server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request path contains model and method
		if !strings.Contains(r.URL.Path, "/models/") || !strings.Contains(r.URL.Path, ":streamGenerateContent") {
			t.Errorf("Unexpected path: %s", r.URL.Path)
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		
		// Verify API key in query
		if r.URL.Query().Get("key") == "" {
			t.Error("Missing API key in query")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		
		// Set SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		
		// Send Google AI SSE format chunks
		chunks := []map[string]interface{}{
			{
				"candidates": []map[string]interface{}{
					{
						"content": map[string]interface{}{
							"parts": []map[string]interface{}{
								{"text": "Hello"},
							},
						},
					},
				},
			},
			{
				"candidates": []map[string]interface{}{
					{
						"content": map[string]interface{}{
							"parts": []map[string]interface{}{
								{"text": " from"},
							},
						},
					},
				},
			},
			{
				"candidates": []map[string]interface{}{
					{
						"content": map[string]interface{}{
							"parts": []map[string]interface{}{
								{"text": " Google"},
							},
						},
					},
				},
			},
			{
				"candidates": []map[string]interface{}{
					{
						"content": map[string]interface{}{
							"parts": []map[string]interface{}{
								{"text": " streaming!"},
							},
						},
						"finishReason": "STOP",
					},
				},
			},
		}
		
		for _, chunk := range chunks {
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", data)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer server.Close()
	
	// Create a temporary API key file
	apiKeyFile := filepath.Join(tmpHome, "google-key")
	if err := os.WriteFile(apiKeyFile, []byte("test-google-key"), 0600); err != nil {
		t.Fatalf("Failed to create API key file: %v", err)
	}
	
	// Disable sessions for streaming tests
	stdout, stderr, logDir, err := runLmcCommandWithLogDir(t, lmcBin,
		[]string{
			"-provider", "google",
			"-provider-url", server.URL + "/v1beta",
			"-api-key-file", apiKeyFile,
			"-stream",
			"-model", "gemini-2.5-pro",
			"-no-session",
		},
		"Test Google streaming")
	
	if err != nil {
		t.Fatalf("Failed to run Google streaming command: %v\nStderr: %s", err, stderr)
	}
	
	// Verify the streamed output
	expectedOutput := "Hello from Google streaming!"
	if !strings.Contains(stdout, expectedOutput) {
		t.Errorf("Expected streaming output to contain %q, got: %s", expectedOutput, stdout)
	}
	
	// Check for stream_chat_output log file
	if !assertRecentLogFiles(t, logDir, "_stream_chat_output", ".log") {
		t.Error("stream_chat_output log file not found")
	}
}