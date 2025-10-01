package proxy

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStreamingEventSequence tests that we generate the correct sequence of SSE events
func TestStreamingEventSequence(t *testing.T) {
	// Create a test response writer
	recorder := httptest.NewRecorder()

	// Create handler
	ctx := context.Background()
	handler, err := NewAnthropicStreamHandler(recorder, "claude-3-opus-20240229", ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	// Send the sequence of events matching Anthropic's format

	// 1. message_start
	if err := handler.SendMessageStart(); err != nil {
		t.Fatalf("Failed to send message_start: %v", err)
	}

	// 2. content_block_start for text
	if err := handler.SendContentBlockStart(0, "text"); err != nil {
		t.Fatalf("Failed to send content_block_start: %v", err)
	}

	// 3. ping
	if err := handler.SendPing(); err != nil {
		t.Fatalf("Failed to send ping: %v", err)
	}

	// 4. Send text deltas
	textChunks := []string{"Okay", ",", " let", "'s", " check", " the", " weather", " for", " San", " Francisco", ",", " CA", ":"}
	for _, chunk := range textChunks {
		if err := handler.SendTextDelta(chunk); err != nil {
			t.Fatalf("Failed to send text delta %q: %v", chunk, err)
		}
	}

	// 5. content_block_stop for text
	if err := handler.SendContentBlockStop(0); err != nil {
		t.Fatalf("Failed to send content_block_stop: %v", err)
	}
	handler.state.TextBlockClosed = true

	// 6. content_block_start for tool_use
	if err := handler.SendToolUseStart(1, "toolu_01T1x1fJ34qAmk2tNTrN7Up6", "get_weather"); err != nil {
		t.Fatalf("Failed to send tool_use start: %v", err)
	}

	// 7. Send tool input deltas
	jsonChunks := []string{
		"",
		`{"location":`,
		` "San`,
		` Francisc`,
		`o,`,
		` CA"`,
		`, `,
		`"unit": "fah`,
		`renheit"}`,
	}

	for _, chunk := range jsonChunks {
		if err := handler.SendToolInputDelta(1, chunk); err != nil {
			t.Fatalf("Failed to send input delta %q: %v", chunk, err)
		}
	}

	// 8. content_block_stop for tool
	if err := handler.SendContentBlockStop(1); err != nil {
		t.Fatalf("Failed to send content_block_stop for tool: %v", err)
	}

	// 9. Update token counts
	handler.state.OutputTokens = 89

	// 10. Complete with message_delta and message_stop
	if err := handler.Complete("tool_use"); err != nil {
		t.Fatalf("Failed to complete stream: %v", err)
	}

	// Verify the output
	output := recorder.Body.String()

	// Check for required events
	requiredEvents := []string{
		"event: message_start",
		"event: content_block_start",
		"event: ping",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
		"data: [DONE]",
	}

	for _, event := range requiredEvents {
		if !strings.Contains(output, event) {
			t.Errorf("Missing required event: %s", event)
		}
	}

	// Check for specific data patterns
	expectedPatterns := []string{
		`"type":"message_start"`,
		`"role":"assistant"`,
		`"type":"text"`,
		`"type":"text_delta"`,
		`"type":"tool_use"`,
		`"name":"get_weather"`,
		`"type":"input_json_delta"`,
		`"partial_json":""`,
		`"partial_json":"{\"location\":`,
		`"stop_reason":"tool_use"`,
		`"output_tokens":89`,
	}

	for _, pattern := range expectedPatterns {
		if !strings.Contains(output, pattern) {
			t.Errorf("Missing expected pattern: %s", pattern)
		}
	}

	// Count events
	messageStartCount := strings.Count(output, "event: message_start")
	if messageStartCount != 1 {
		t.Errorf("Expected 1 message_start event, got %d", messageStartCount)
	}

	contentBlockStartCount := strings.Count(output, "event: content_block_start")
	if contentBlockStartCount != 2 { // One for text, one for tool
		t.Errorf("Expected 2 content_block_start events, got %d", contentBlockStartCount)
	}

	contentBlockStopCount := strings.Count(output, "event: content_block_stop")
	if contentBlockStopCount != 2 { // One for text, one for tool
		t.Errorf("Expected 2 content_block_stop events, got %d", contentBlockStopCount)
	}

	// Log all lines for debugging
	lines := strings.Split(output, "\n")
	t.Logf("Total lines: %d", len(lines))

	// Find lines with partial_json
	t.Logf("Lines containing partial_json:")
	for i, line := range lines {
		if strings.Contains(line, "partial_json") {
			t.Logf("%d: %s", i+1, line)
		}
	}
}
