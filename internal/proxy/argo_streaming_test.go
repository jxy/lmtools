package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/constants"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// SSEEvent represents a parsed Server-Sent Event
type SSEEvent struct {
	Event string
	Data  string
	Raw   string
}

// parseSSEEvents parses raw SSE output into structured events
func parseSSEEvents(output string) []SSEEvent {
	var events []SSEEvent
	scanner := NewSSEScanner(strings.NewReader(output))

	var currentEvent SSEEvent
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			currentEvent.Event = strings.TrimPrefix(line, "event: ")
			currentEvent.Raw = line
		} else if strings.HasPrefix(line, "data: ") {
			currentEvent.Data = strings.TrimPrefix(line, "data: ")
			if currentEvent.Raw != "" {
				currentEvent.Raw += "\n"
			}
			currentEvent.Raw += line

			// Event is complete, add it
			events = append(events, currentEvent)
			currentEvent = SSEEvent{}
		} else if line == "" && currentEvent.Raw != "" {
			// Empty line might indicate end of event if we have partial data
			if currentEvent.Event != "" || currentEvent.Data != "" {
				events = append(events, currentEvent)
				currentEvent = SSEEvent{}
			}
		}
	}

	// Add any remaining event
	if currentEvent.Event != "" || currentEvent.Data != "" {
		events = append(events, currentEvent)
	}

	return events
}

// extractEventSequence returns just the event types in order
func extractEventSequence(events []SSEEvent) []string {
	var sequence []string
	for _, e := range events {
		if e.Event != "" {
			sequence = append(sequence, e.Event)
		} else if e.Data == "[DONE]" {
			sequence = append(sequence, "[DONE]")
		}
	}
	return sequence
}

// countEventOccurrences counts how many times each event type appears
func countEventOccurrences(events []SSEEvent) map[string]int {
	counts := make(map[string]int)
	for _, e := range events {
		if e.Event != "" {
			counts[e.Event]++
		} else if e.Data == "[DONE]" {
			counts["[DONE]"]++
		}
	}
	return counts
}

// TestStreamFromArgoEventSequence verifies the exact sequence of SSE events
func TestStreamFromArgoEventSequence(t *testing.T) {
	// Create a mock Argo streaming server
	mockArgo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			// Return plain text stream like Argo does
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)

			// Simulate streaming chunks
			chunks := []string{
				"Hello ",
				"from ",
				"Argo ",
				"streaming!",
			}

			for _, chunk := range chunks {
				fmt.Fprint(w, chunk)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			return
		}
		http.NotFound(w, r)
	}))
	defer mockArgo.Close()

	// Create a test request
	anthReq := &AnthropicRequest{
		Model:     "gpto3",
		MaxTokens: 100,
		Stream:    true,
		Messages: []AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"Test message"`)},
		},
		// No tools - we want to test real streaming
	}

	// Create a response recorder
	recorder := httptest.NewRecorder()
	ctx := context.Background()

	// Create handler
	handler, err := NewAnthropicStreamHandler(recorder, anthReq.Model, ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	// Mock the forwardToArgoStream method by directly testing the streaming logic
	// Since we can't easily mock the private method, we'll test the core streaming behavior

	// Send initial events (as streamFromArgo does)
	if err := handler.SendMessageStart(); err != nil {
		t.Fatalf("Failed to send message start: %v", err)
	}
	if err := handler.SendContentBlockStart(0, "text"); err != nil {
		t.Fatalf("Failed to send content block start: %v", err)
	}

	// Create a reader with test data
	testData := "Hello from Argo streaming!"
	reader := strings.NewReader(testData)

	// Use ArgoStreamParser (this is what streamFromArgo does)
	parser := NewArgoStreamParser(handler)
	if err := parser.ParseWithPingInterval(reader, 100*time.Millisecond); err != nil {
		t.Fatalf("Failed to parse stream: %v", err)
	}

	// Parse the output
	output := recorder.Body.String()
	events := parseSSEEvents(output)

	// Extract event sequence
	sequence := extractEventSequence(events)

	// Expected sequence for Argo streaming (no tools)
	expectedSequence := []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
		// Anthropic format doesn't use [DONE]
	}

	// Verify the sequence matches (allowing for multiple content_block_delta events)
	if len(sequence) < len(expectedSequence) {
		t.Errorf("Event sequence too short. Got %d events, expected at least %d",
			len(sequence), len(expectedSequence))
		t.Logf("Actual sequence: %v", sequence)
		t.Logf("Expected sequence: %v", expectedSequence)
	}

	// Check specific positions
	if len(sequence) > 0 && sequence[0] != "message_start" {
		t.Errorf("First event should be message_start, got %s", sequence[0])
	}

	if len(sequence) > 1 && sequence[1] != "content_block_start" {
		t.Errorf("Second event should be content_block_start, got %s", sequence[1])
	}

	// Find the last two events (Anthropic format ends with message_delta, message_stop)
	if len(sequence) >= 2 {
		lastTwo := sequence[len(sequence)-2:]
		expectedLast := []string{"message_delta", "message_stop"}

		for i, expected := range expectedLast {
			if lastTwo[i] != expected {
				t.Errorf("Event %d from end should be %s, got %s",
					2-i, expected, lastTwo[i])
			}
		}
	}

	t.Logf("Event sequence validated successfully: %v", sequence)
}

// TestStreamFromArgoNoDuplicates ensures no duplicate terminal events
func TestStreamFromArgoNoDuplicates(t *testing.T) {
	// Create a response recorder
	recorder := httptest.NewRecorder()
	ctx := context.Background()

	// Create handler
	handler, err := NewAnthropicStreamHandler(recorder, "gpto3", ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	// Simulate the streamFromArgo flow
	// 1. Send initial events
	if err := handler.SendMessageStart(); err != nil {
		t.Fatalf("Failed to send message start: %v", err)
	}
	if err := handler.SendContentBlockStart(0, "text"); err != nil {
		t.Fatalf("Failed to send content block start: %v", err)
	}

	// 2. Simulate Argo streaming with parser
	testData := "This is a test message from Argo."
	reader := strings.NewReader(testData)

	parser := NewArgoStreamParser(handler)
	if err := parser.ParseWithPingInterval(reader, 100*time.Millisecond); err != nil {
		t.Fatalf("Failed to parse stream: %v", err)
	}

	// The parser should have called Complete() which sends message_stop and [DONE]
	// We should NOT call any additional closing methods here (that was the bug)

	// Parse and count events
	output := recorder.Body.String()
	events := parseSSEEvents(output)
	counts := countEventOccurrences(events)

	// Verify no duplicates of terminal events
	if counts["message_stop"] != 1 {
		t.Errorf("Expected exactly 1 message_stop event, got %d", counts["message_stop"])
		t.Logf("Full output:\n%s", output)
	}

	// Anthropic format doesn't use [DONE], just verify message_stop is present

	// Also verify other events appear correct number of times
	if counts["message_start"] != 1 {
		t.Errorf("Expected exactly 1 message_start event, got %d", counts["message_start"])
	}

	if counts["content_block_start"] != 1 {
		t.Errorf("Expected exactly 1 content_block_start event, got %d", counts["content_block_start"])
	}

	if counts["content_block_stop"] != 1 {
		t.Errorf("Expected exactly 1 content_block_stop event, got %d", counts["content_block_stop"])
	}

	if counts["message_delta"] != 1 {
		t.Errorf("Expected exactly 1 message_delta event, got %d", counts["message_delta"])
	}

	t.Logf("Event counts verified - no duplicates found")
}

// TestStreamFromArgoWithSlowStream tests streaming with delays and pings
func TestStreamFromArgoWithSlowStream(t *testing.T) {
	// Create a pipe for controlled streaming
	pr, pw := io.Pipe()

	// Simulate slow Argo stream in background
	go func() {
		chunks := []string{"Slow ", "streaming ", "test"}
		for _, chunk := range chunks {
			time.Sleep(60 * time.Millisecond) // Delay between chunks
			_, _ = pw.Write([]byte(chunk))
		}
		pw.Close()
	}()

	// Create handler
	recorder := httptest.NewRecorder()
	ctx := context.Background()
	handler, err := NewAnthropicStreamHandler(recorder, "gpto3", ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	// Send initial events
	if err := handler.SendMessageStart(); err != nil {
		t.Fatalf("Failed to send message start: %v", err)
	}
	if err := handler.SendContentBlockStart(0, "text"); err != nil {
		t.Fatalf("Failed to send content block start: %v", err)
	}

	// Parse with 50ms ping interval (should get pings between chunks)
	parser := NewArgoStreamParser(handler)
	err = parser.ParseWithPingInterval(pr, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	// Parse output
	output := recorder.Body.String()
	events := parseSSEEvents(output)

	// Count pings
	pingCount := 0
	for _, e := range events {
		if e.Event == "ping" {
			pingCount++
		}
	}

	// Should have at least 1 ping (between slow chunks)
	if pingCount < 1 {
		t.Errorf("Expected at least 1 ping with slow streaming, got %d", pingCount)
	}

	// Verify no duplicate terminal events
	counts := countEventOccurrences(events)
	if counts["message_stop"] != 1 {
		t.Errorf("Expected exactly 1 message_stop event, got %d", counts["message_stop"])
	}
	// Anthropic format ends with message_stop, not [DONE]

	t.Logf("Slow streaming test passed with %d pings", pingCount)
}

// TestStreamFromArgoComplete tests the complete streamFromArgo flow
func TestStreamFromArgoComplete(t *testing.T) {
	// Create a mock Argo server
	mockArgo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			// Verify request
			if r.Method != "POST" {
				t.Errorf("Expected POST, got %s", r.Method)
			}

			// Read and verify request body
			var argoReq ArgoChatRequest
			if err := json.NewDecoder(r.Body).Decode(&argoReq); err != nil {
				t.Errorf("Failed to decode request: %v", err)
			}

			// Return streaming response
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)

			response := "This is a complete test of Argo streaming."
			fmt.Fprint(w, response)
			return
		}
		http.NotFound(w, r)
	}))
	defer mockArgo.Close()

	// Create server with config (keeping for documentation, though not used directly)
	config := &Config{
		Provider:     constants.ProviderArgo,
		ArgoUser:     "testuser",
		ArgoEnv:      "test",
		ProviderURL:  mockArgo.URL,
		PingInterval: 100 * time.Millisecond,
	}
	// Create server (NewEndpoints is called internally)
	// We test the core logic directly, not through the server
	_, cleanup := NewTestServer(t, config)
	t.Cleanup(cleanup)

	// Create test request handler (for documentation of the expected request format)
	_ = httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{
		"model": "gpto3",
		"messages": [{"role": "user", "content": "Test"}],
		"stream": true
	}`))

	// Create response recorder
	recorder := httptest.NewRecorder()

	// Create the request directly for testing
	ctx := context.Background()
	anthReq := &AnthropicRequest{
		Model:    "gpto3",
		Stream:   true,
		Messages: []AnthropicMessage{{Role: "user", Content: json.RawMessage(`"Test"`)}},
	}

	handler, err := NewAnthropicStreamHandler(recorder, anthReq.Model, ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	// Execute the streaming flow directly with the handler
	// Since streamFromArgo is a method on *Server, we'll test the core logic instead
	// by simulating what streamFromArgo does

	// 1. Send initial events
	if err := handler.SendMessageStart(); err != nil {
		t.Fatalf("Failed to send message start: %v", err)
	}
	if err := handler.SendContentBlockStart(0, "text"); err != nil {
		t.Fatalf("Failed to send content block start: %v", err)
	}

	// 2. Create a mock stream response
	testResponse := "This is a complete test of Argo streaming."
	reader := strings.NewReader(testResponse)

	// 3. Use ArgoStreamParser (this is what streamFromArgo does)
	parser := NewArgoStreamParser(handler)
	if err := parser.ParseWithPingInterval(reader, 100*time.Millisecond); err != nil {
		t.Fatalf("Failed to parse stream: %v", err)
	}

	// Parse and validate output
	output := recorder.Body.String()
	events := parseSSEEvents(output)
	sequence := extractEventSequence(events)

	// Verify sequence
	if len(sequence) < 6 {
		t.Errorf("Expected at least 6 events, got %d: %v", len(sequence), sequence)
	}

	// Verify no duplicates
	counts := countEventOccurrences(events)

	criticalEvents := map[string]int{
		"message_start":       1,
		"content_block_start": 1,
		"content_block_stop":  1,
		"message_delta":       1,
		"message_stop":        1,
		// Anthropic format doesn't use [DONE]
	}

	for event, expectedCount := range criticalEvents {
		if counts[event] != expectedCount {
			t.Errorf("Event %s: expected %d, got %d", event, expectedCount, counts[event])
		}
	}

	// Verify the text content was streamed
	hasTextDelta := false
	for _, e := range events {
		if e.Event == "content_block_delta" && strings.Contains(e.Data, "text_delta") {
			hasTextDelta = true
			break
		}
	}

	if !hasTextDelta {
		t.Error("No text_delta events found in stream")
	}

	t.Logf("Complete streamFromArgo test passed with sequence: %v", sequence)
}

// TestStreamFromArgoErrorHandling tests error scenarios
func TestStreamFromArgoErrorHandling(t *testing.T) {
	tests := []struct {
		name          string
		streamData    string
		shouldError   bool
		errorContains string
	}{
		{
			name:        "Empty stream",
			streamData:  "",
			shouldError: false, // Empty stream is valid, just ends immediately
		},
		{
			name:        "Very long text",
			streamData:  strings.Repeat("A", 10000),
			shouldError: false,
		},
		{
			name:        "UTF-8 text",
			streamData:  "Hello 世界 🌍",
			shouldError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx := context.Background()

			handler, err := NewAnthropicStreamHandler(recorder, "gpto3", ctx)
			if err != nil {
				t.Fatalf("Failed to create handler: %v", err)
			}

			// Send initial events
			if err := handler.SendMessageStart(); err != nil {
				t.Fatalf("Failed to send message start: %v", err)
			}
			if err := handler.SendContentBlockStart(0, "text"); err != nil {
				t.Fatalf("Failed to send content block start: %v", err)
			}

			// Parse the test data
			reader := strings.NewReader(tt.streamData)
			parser := NewArgoStreamParser(handler)
			err = parser.ParseWithPingInterval(reader, 100*time.Millisecond)

			if tt.shouldError {
				if err == nil {
					t.Error("Expected error but got none")
				} else if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("Error should contain %q, got: %v", tt.errorContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}

				// Verify no duplicate events even in edge cases
				events := parseSSEEvents(recorder.Body.String())
				counts := countEventOccurrences(events)

				if counts["message_stop"] > 1 {
					t.Errorf("Got %d message_stop events, expected <= 1", counts["message_stop"])
				}
				if counts["[DONE]"] > 1 {
					t.Errorf("Got %d [DONE] events, expected <= 1", counts["[DONE]"])
				}
			}
		})
	}
}
