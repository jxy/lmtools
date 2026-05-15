package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestStreamingErrorHandling tests error conditions during streaming
func TestStreamingErrorHandling(t *testing.T) {
	t.Run("Context cancellation during OpenAI stream", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, cancel := context.WithCancel(context.Background())

		writer, err := NewOpenAIStreamWriter(recorder, "gpt-4", ctx)
		if err != nil {
			t.Fatalf("Failed to create OpenAI writer: %v", err)
		}

		// Start streaming
		_ = writer.WriteInitialAssistantTextDelta()
		_ = writer.WriteDelta("Starting...", nil, nil)

		// Cancel context
		cancel()

		// Try to write after cancellation
		err = writer.WriteDelta("This should fail", nil, nil)
		if err == nil {
			t.Error("Expected error when writing after context cancellation")
		}
	})

	t.Run("Context cancellation during Anthropic stream", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, cancel := context.WithCancel(context.Background())

		handler, err := NewAnthropicStreamHandler(recorder, "claude-3-opus", ctx)
		if err != nil {
			t.Fatalf("Failed to create Anthropic handler: %v", err)
		}

		// Start streaming
		_ = handler.SendMessageStart()
		_ = handler.SendContentBlockStart(0, "text")
		_ = handler.SendTextDelta("Starting...")

		// Cancel context
		cancel()

		// Try to write after cancellation
		err = handler.SendTextDelta("This should fail")
		if err == nil {
			t.Error("Expected error when writing after context cancellation")
		}
	})

	t.Run("Invalid tool call index", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx := context.Background()

		handler, err := NewAnthropicStreamHandler(recorder, "claude-3-opus", ctx)
		if err != nil {
			t.Fatalf("Failed to create Anthropic handler: %v", err)
		}

		// Start message
		_ = handler.SendMessageStart()

		// Try to send tool input delta without starting tool use
		err = handler.SendToolInputDelta(999, "{\"test\": \"data\"}")
		// This might not error depending on implementation, but check the output
		body := recorder.Body.String()
		if err == nil && strings.Contains(body, "999") {
			// If no error, at least verify the index is handled
			t.Log("Tool input delta with invalid index was sent")
		}
	})

	t.Run("Empty content streaming", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx := context.Background()

		writer, err := NewOpenAIStreamWriter(recorder, "gpt-4", ctx)
		if err != nil {
			t.Fatalf("Failed to create OpenAI writer: %v", err)
		}

		// Stream with empty content
		_ = writer.WriteInitialAssistantTextDelta()
		_ = writer.WriteDelta("", nil, nil) // Empty delta
		_ = writer.WriteFinish("stop", nil)

		body := recorder.Body.String()
		// Should still have proper structure
		if !strings.Contains(body, "[DONE]") {
			t.Error("Empty content stream should still end with [DONE]")
		}
		if !strings.Contains(body, "\"finish_reason\":\"stop\"") {
			t.Error("Empty content stream should still have finish_reason")
		}
	})

	t.Run("Malformed tool arguments streaming", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx := context.Background()

		writer, err := NewOpenAIStreamWriter(recorder, "gpt-4", ctx)
		if err != nil {
			t.Fatalf("Failed to create OpenAI writer: %v", err)
		}

		// Stream tool call with malformed JSON arguments
		_ = writer.WriteInitialAssistantTextDelta()

		toolCall := &ToolCallDelta{
			Index: 0,
			ID:    "call_bad",
			Type:  "function",
			Function: &FunctionCallDelta{
				Name:      "test_func",
				Arguments: "",
			},
		}
		_ = writer.WriteToolCallDelta(0, toolCall, nil, nil)

		// Send malformed JSON in chunks
		toolCallArgs := &ToolCallDelta{
			Index: 0,
			Function: &FunctionCallDelta{
				Arguments: "{\"broken: json", // Malformed JSON
			},
		}
		_ = writer.WriteToolCallDelta(0, toolCallArgs, nil, nil)

		_ = writer.WriteFinish("tool_calls", nil)

		body := recorder.Body.String()
		// Should still complete the stream properly
		if !strings.Contains(body, "[DONE]") {
			t.Error("Stream with malformed tool args should still end with [DONE]")
		}
	})
}

// TestStreamingTimeout tests timeout behavior during streaming
func TestStreamingTimeout(t *testing.T) {
	t.Run("Context timeout during OpenAI stream", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		writer, err := NewOpenAIStreamWriter(recorder, "gpt-4", ctx)
		if err != nil {
			t.Fatalf("Failed to create OpenAI writer: %v", err)
		}

		// Start streaming
		_ = writer.WriteInitialAssistantTextDelta()
		_ = writer.WriteDelta("Starting...", nil, nil)

		// Wait for timeout
		time.Sleep(100 * time.Millisecond)

		// Try to write after timeout
		err = writer.WriteDelta("This should fail", nil, nil)
		if err == nil {
			t.Error("Expected error when writing after context timeout")
		}
	})

	t.Run("Context timeout during Anthropic stream", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		handler, err := NewAnthropicStreamHandler(recorder, "claude-3-opus", ctx)
		if err != nil {
			t.Fatalf("Failed to create Anthropic handler: %v", err)
		}

		// Start streaming
		_ = handler.SendMessageStart()
		_ = handler.SendContentBlockStart(0, "text")
		_ = handler.SendTextDelta("Starting...")

		// Wait for timeout
		time.Sleep(100 * time.Millisecond)

		// Try to write after timeout
		err = handler.SendTextDelta("This should fail")
		if err == nil {
			t.Error("Expected error when writing after context timeout")
		}
	})
}

// TestStreamingRecovery tests recovery from errors during streaming
func TestStreamingRecovery(t *testing.T) {
	t.Run("Error event in OpenAI stream", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx := context.Background()

		writer, err := NewOpenAIStreamWriter(recorder, "gpt-4", ctx)
		if err != nil {
			t.Fatalf("Failed to create OpenAI writer: %v", err)
		}

		// Start streaming
		_ = writer.WriteInitialAssistantTextDelta()
		_ = writer.WriteDelta("Processing...", nil, nil)

		// Send error
		err = writer.WriteError("rate_limit_error", "Rate limit exceeded")
		if err != nil {
			t.Fatalf("Failed to write error: %v", err)
		}

		body := recorder.Body.String()
		if !strings.Contains(body, "rate_limit_error") {
			t.Error("Error event should contain error type")
		}
		if !strings.Contains(body, "Rate limit exceeded") {
			t.Error("Error event should contain error message")
		}
	})

	t.Run("Error event in Anthropic stream", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx := context.Background()

		handler, err := NewAnthropicStreamHandler(recorder, "claude-3-opus", ctx)
		if err != nil {
			t.Fatalf("Failed to create Anthropic handler: %v", err)
		}

		// Start streaming
		_ = handler.SendMessageStart()
		_ = handler.SendContentBlockStart(0, "text")
		_ = handler.SendTextDelta("Processing...")

		// Send error event
		err = handler.SendStreamError("System overloaded")
		if err != nil {
			t.Fatalf("Failed to send error: %v", err)
		}

		body := recorder.Body.String()
		if !strings.Contains(body, "event: error") {
			t.Error("Should have error event")
		}
		if !strings.Contains(body, "System overloaded") {
			t.Error("Error event should contain error message")
		}
	})
}

// TestStreamingEdgeCases tests various edge cases in streaming
func TestStreamingEdgeCases(t *testing.T) {
	t.Run("Multiple finish reasons", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx := context.Background()

		writer, err := NewOpenAIStreamWriter(recorder, "gpt-4", ctx)
		if err != nil {
			t.Fatalf("Failed to create OpenAI writer: %v", err)
		}

		// Try to send multiple finish reasons (should only use the first)
		_ = writer.WriteInitialAssistantTextDelta()
		_ = writer.WriteDelta("Test", nil, nil)
		stop := "stop"
		_ = writer.WriteDelta("", nil, &stop) // First finish
		length := "length"
		err = writer.WriteDelta("", nil, &length) // Second finish (should work but be ignored)
		if err != nil {
			t.Log("Second finish reason write returned error:", err)
		}

		body := recorder.Body.String()
		// Should have the first finish reason
		if !strings.Contains(body, "\"finish_reason\":\"stop\"") {
			t.Error("Should have the first finish reason")
		}
	})

	t.Run("Very long content streaming", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx := context.Background()

		handler, err := NewAnthropicStreamHandler(recorder, "claude-3-opus", ctx)
		if err != nil {
			t.Fatalf("Failed to create Anthropic handler: %v", err)
		}

		// Stream very long content
		_ = handler.SendMessageStart()
		_ = handler.SendContentBlockStart(0, "text")
		longContent := strings.Repeat("This is a very long piece of content. ", 1000)
		err = handler.SendTextDelta(longContent)
		if err != nil {
			t.Fatalf("Failed to send long content: %v", err)
		}
		_ = handler.FinishStream("end_turn", nil)

		body := recorder.Body.String()
		// Should contain at least part of the long content
		if !strings.Contains(body, "This is a very long piece of content") {
			t.Error("Should contain the long content")
		}
		if !strings.Contains(body, "event: message_stop") {
			t.Error("Should properly terminate even with long content")
		}
	})

	t.Run("Unicode and special characters in stream", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx := context.Background()

		writer, err := NewOpenAIStreamWriter(recorder, "gpt-4", ctx)
		if err != nil {
			t.Fatalf("Failed to create OpenAI writer: %v", err)
		}

		// Stream content with unicode and special characters
		_ = writer.WriteInitialAssistantTextDelta()
		unicodeContent := "Hello 世界! 🌍 Special chars: \n\t\"\\ End."
		_ = writer.WriteDelta(unicodeContent, nil, nil)
		_ = writer.WriteFinish("stop", nil)

		body := recorder.Body.String()
		// Should properly handle unicode
		if !strings.Contains(body, "世界") {
			t.Error("Should contain Chinese characters")
		}
		if !strings.Contains(body, "🌍") {
			t.Error("Should contain emoji")
		}
		// Special characters should be escaped in JSON
		if !strings.Contains(body, "\\n") || !strings.Contains(body, "\\t") {
			t.Error("Should properly escape special characters in JSON")
		}
	})
}

// MockErrorWriter simulates write errors
type MockErrorWriter struct {
	failAfter int
	writes    int
}

func (m *MockErrorWriter) Write(p []byte) (n int, err error) {
	m.writes++
	if m.writes > m.failAfter {
		return 0, errors.New("mock write error")
	}
	return len(p), nil
}

func (m *MockErrorWriter) Header() http.Header {
	return make(http.Header)
}

func (m *MockErrorWriter) WriteHeader(statusCode int) {}

func (m *MockErrorWriter) Flush() {}

// TestStreamingWriteErrors tests handling of write errors during streaming
func TestStreamingWriteErrors(t *testing.T) {
	t.Run("Write error during OpenAI stream", func(t *testing.T) {
		mockWriter := &MockErrorWriter{failAfter: 2}
		ctx := context.Background()

		writer, err := NewOpenAIStreamWriter(mockWriter, "gpt-4", ctx)
		if err != nil {
			t.Fatalf("Failed to create OpenAI writer: %v", err)
		}

		// Start streaming
		_ = writer.WriteInitialAssistantTextDelta()
		_ = writer.WriteDelta("First", nil, nil)

		// This should fail
		err = writer.WriteDelta("This should fail", nil, nil)
		if err == nil {
			t.Error("Expected error when underlying writer fails")
		}
	})
}
