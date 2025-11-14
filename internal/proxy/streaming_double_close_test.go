package proxy

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStreamingNoDoubleClose tests that content blocks are not closed twice
func TestStreamingNoDoubleClose(t *testing.T) {
	// Create a test response writer
	recorder := httptest.NewRecorder()

	// Create handler
	ctx := context.Background()
	handler, err := NewAnthropicStreamHandler(recorder, "claude-3-opus-20240229", ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	// Simulate what happens in simulateStreamingFromArgo

	// 1. Start text stream (combines message_start and content_block_start)
	if err := handler.SendMessageStart(); err != nil {
		t.Fatalf("Failed to send message start: %v", err)
	}
	if err := handler.SendContentBlockStart(0, "text"); err != nil {
		t.Fatalf("Failed to send content block start: %v", err)
	}

	// 3. Send ping
	if err := handler.SendPing(); err != nil {
		t.Fatalf("Failed to send ping: %v", err)
	}

	// 4. Send text
	if err := handler.SendTextDelta("I'll help you list the directory contents."); err != nil {
		t.Fatalf("Failed to send text delta: %v", err)
	}

	// 5. Close text block (this happens in simulateStreamingFromArgo)
	if err := handler.SendContentBlockStop(0); err != nil {
		t.Fatalf("Failed to send content_block_stop for text: %v", err)
	}
	handler.state.TextBlockClosed = true

	// 6. Send tool use block
	if err := handler.SendToolUseStart(1, "toolu_test", "LS"); err != nil {
		t.Fatalf("Failed to send tool_use start: %v", err)
	}

	// 7. Send tool input
	if err := handler.SendToolInputDelta(1, ""); err != nil {
		t.Fatalf("Failed to send empty input delta: %v", err)
	}
	if err := handler.SendToolInputDelta(1, `{"path":"/test"}`); err != nil {
		t.Fatalf("Failed to send input delta: %v", err)
	}

	// 8. Close tool block (this happens in simulateStreamingFromArgo)
	if err := handler.SendContentBlockStop(1); err != nil {
		t.Fatalf("Failed to send content_block_stop for tool: %v", err)
	}

	// 9. Now call Complete which might try to close blocks again
	if err := handler.Complete("tool_use"); err != nil {
		t.Fatalf("Failed to complete stream: %v", err)
	}

	// Check the output for duplicate content_block_stop events
	output := recorder.Body.String()
	lines := strings.Split(output, "\n")

	// Count content_block_stop events for each index
	blockStopCounts := make(map[int]int)
	for i := 0; i < len(lines)-1; i++ {
		if lines[i] == "event: content_block_stop" && strings.HasPrefix(lines[i+1], "data: ") {
			// Parse the data line
			dataLine := strings.TrimPrefix(lines[i+1], "data: ")
			if strings.Contains(dataLine, `"index":0`) {
				blockStopCounts[0]++
			} else if strings.Contains(dataLine, `"index":1`) {
				blockStopCounts[1]++
			}
		}
	}

	// Verify no double closing
	for index, count := range blockStopCounts {
		if count > 1 {
			t.Errorf("Block %d was closed %d times (expected 1)", index, count)
		}
	}

	// Should have exactly 2 content_block_stop events total
	totalStops := 0
	for _, count := range blockStopCounts {
		totalStops += count
	}
	if totalStops != 2 {
		t.Errorf("Expected 2 content_block_stop events total, got %d", totalStops)
	}

	t.Logf("Block stop counts: %v", blockStopCounts)
}

// TestStreamingDoubleCloseAttempt tests that attempting to close a block twice is handled gracefully
func TestStreamingDoubleCloseAttempt(t *testing.T) {
	// Create a test response writer
	recorder := httptest.NewRecorder()

	// Create handler
	ctx := context.Background()
	handler, err := NewAnthropicStreamHandler(recorder, "claude-3-opus-20240229", ctx)
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	// Send initial events
	_ = handler.SendMessageStart()
	_ = handler.SendContentBlockStart(0, "text")
	_ = handler.SendTextDelta("Test")

	// Close block 0
	if err := handler.SendContentBlockStop(0); err != nil {
		t.Fatalf("First close failed: %v", err)
	}

	// Try to close block 0 again - should be ignored
	if err := handler.SendContentBlockStop(0); err != nil {
		t.Fatalf("Second close failed: %v", err)
	}

	// Check output
	output := recorder.Body.String()
	// Look for content_block_stop events with index 0, regardless of field order
	stopCount := 0
	lines := strings.Split(output, "\n")
	for i := 0; i < len(lines)-1; i++ {
		if lines[i] == "event: content_block_stop" && strings.HasPrefix(lines[i+1], "data: ") {
			dataLine := strings.TrimPrefix(lines[i+1], "data: ")
			// Check if this is for index 0 (field order agnostic)
			if strings.Contains(dataLine, `"index":0`) && strings.Contains(dataLine, `"type":"content_block_stop"`) {
				stopCount++
			}
		}
	}

	if stopCount != 1 {
		t.Errorf("Expected 1 content_block_stop for index 0, found %d", stopCount)
	}
}
