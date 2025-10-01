package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"lmtools/internal/core"
	"net/http"
	"strings"
	"testing"
)

// TestSimulatedStreamingFormat verifies that simulated streaming matches Anthropic's format
func TestSimulatedStreamingFormat(t *testing.T) {
	// Add a test-specific timeout to prevent hanging
	t.Parallel()

	// Setup test server configured for Argo provider
	proxyServer, argoMock := SetupArgoTestServer(t)
	defer proxyServer.Close()
	defer argoMock.Close()

	// Test simulated streaming with Argo
	// Argo doesn't support real streaming with tools, so it will simulate streaming
	req := AnthropicRequest{
		Model:     "gpto3", // Argo model
		MaxTokens: 100,
		Stream:    true,
		Messages: []AnthropicMessage{
			{
				Role:    core.RoleUser,
				Content: json.RawMessage(`"Tell me a joke"`),
			},
		},
		// No tools - Argo will simulate streaming without tools
	}

	reqBody, _ := json.Marshal(req)
	resp, err := http.Post(
		proxyServer.URL+"/v1/messages",
		"application/json",
		bytes.NewReader(reqBody),
	)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Unexpected status %d: %s", resp.StatusCode, string(body))
	}

	// Check Content-Type
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Expected Content-Type text/event-stream, got %s", ct)
	}

	// Read the entire stream
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Error reading stream: %v", err)
	}

	streamOutput := string(body)

	// Verify proper SSE format
	lines := strings.Split(streamOutput, "\n")

	// Check for required event sequence
	// Note: With artificial delays removed, response completes before ping interval (1s)
	// So ping events may not appear in fast responses
	expectedEventSequence := []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
	}

	eventIndex := 0
	for _, line := range lines {
		if eventIndex < len(expectedEventSequence) && line == expectedEventSequence[eventIndex] {
			eventIndex++
		}
	}

	if eventIndex != len(expectedEventSequence) {
		t.Errorf("Expected event sequence not found. Got %d/%d events", eventIndex, len(expectedEventSequence))
		t.Logf("Stream output:\n%s", streamOutput)
	}

	// Verify SSE format rules
	for i, line := range lines {
		if strings.HasPrefix(line, "event:") {
			// Event line should be followed by data line
			if i+1 < len(lines) && !strings.HasPrefix(lines[i+1], "data:") {
				t.Errorf("Event line at %d not followed by data line", i)
			}
		}
		if strings.HasPrefix(line, "data:") && line != "data: [DONE]" {
			// Data lines should contain valid JSON
			jsonStr := strings.TrimPrefix(line, "data: ")
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
				t.Errorf("Invalid JSON in data line %d: %v", i, err)
			}
		}
	}

	// Check that stream ends with [DONE]
	if !strings.Contains(streamOutput, "data: [DONE]") {
		t.Error("Stream does not end with data: [DONE]")
	}

	// Verify content_block_delta events contain text
	if !strings.Contains(streamOutput, `"type":"text_delta"`) {
		t.Error("No text_delta events found in stream")
	}

	// Log first few lines for debugging
	t.Logf("First 10 lines of stream:")
	for i, line := range lines {
		if i >= 10 {
			break
		}
		t.Logf("%d: %s", i+1, line)
	}
}
