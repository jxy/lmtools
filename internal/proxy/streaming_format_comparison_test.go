package proxy

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStreamTerminationFormats verifies the different stream termination behaviors
// between OpenAI (sends [DONE]) and Anthropic (doesn't send [DONE])
func TestStreamTerminationFormats(t *testing.T) {
	t.Run("OpenAI sends [DONE]", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx := context.Background()

		writer, err := NewOpenAIStreamWriter(recorder, "gpt-4", ctx)
		if err != nil {
			t.Fatalf("Failed to create OpenAI writer: %v", err)
		}

		// Write some content and finish the stream
		_ = writer.WriteInitialAssistantTextDelta()
		_ = writer.WriteDelta("Hello world", nil, nil)
		_ = writer.WriteFinish("stop", nil)

		// Check that [DONE] was sent
		body := recorder.Body.String()
		if !strings.Contains(body, "data: [DONE]") {
			t.Error("OpenAI stream should end with [DONE]")
		}

		// Verify the order: content -> finish_reason -> [DONE]
		lines := strings.Split(body, "\n")
		var foundFinish, foundDone bool
		var finishIdx, doneIdx int

		for i, line := range lines {
			if strings.Contains(line, "\"finish_reason\":\"stop\"") {
				foundFinish = true
				finishIdx = i
			}
			if line == "data: [DONE]" {
				foundDone = true
				doneIdx = i
			}
		}

		if !foundFinish {
			t.Error("OpenAI stream should contain finish_reason before [DONE]")
		}
		if !foundDone {
			t.Error("OpenAI stream should end with [DONE]")
		}
		if foundFinish && foundDone && doneIdx <= finishIdx {
			t.Errorf("Expected [DONE] after finish_reason, but got indices: finish=%d, done=%d", finishIdx, doneIdx)
		}
	})

	t.Run("Anthropic doesn't send [DONE]", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx := context.Background()

		handler, err := NewAnthropicStreamHandler(recorder, "claude-3-opus", ctx)
		if err != nil {
			t.Fatalf("Failed to create Anthropic handler: %v", err)
		}

		// Start text stream and send content
		_ = handler.SendMessageStart()
		_ = handler.SendContentBlockStart(0, "text")
		_ = handler.SendTextDelta("Hello world")

		// Finish the stream (Anthropic style)
		_ = handler.FinishStream("end_turn", nil)

		// Check that [DONE] was NOT sent
		body := recorder.Body.String()
		if strings.Contains(body, "[DONE]") {
			t.Error("Anthropic stream should NOT send [DONE]")
		}

		// Verify it ends with message_stop
		if !strings.Contains(body, "event: message_stop") {
			t.Error("Anthropic stream should end with message_stop event")
		}

		// Verify the order: message_start -> content -> message_delta -> message_stop
		lines := strings.Split(body, "\n")
		var foundStart, foundDelta, foundStop bool
		var startIdx, deltaIdx, stopIdx int

		for i, line := range lines {
			if line == "event: message_start" {
				foundStart = true
				startIdx = i
			}
			if line == "event: message_delta" {
				foundDelta = true
				deltaIdx = i
			}
			if line == "event: message_stop" {
				foundStop = true
				stopIdx = i
			}
		}

		if !foundStart || !foundDelta || !foundStop {
			t.Errorf("Anthropic stream missing events: start=%v, delta=%v, stop=%v", foundStart, foundDelta, foundStop)
		}
		if foundStart && foundDelta && foundStop {
			if startIdx >= deltaIdx || deltaIdx >= stopIdx {
				t.Errorf("Expected order message_start < message_delta < message_stop, got indices: %d, %d, %d", startIdx, deltaIdx, stopIdx)
			}
		}
	})
}

// TestStreamWithUsageComparison verifies usage reporting differences between formats
func TestStreamWithUsageComparison(t *testing.T) {
	usage := &AnthropicUsage{
		InputTokens:  10,
		OutputTokens: 20,
	}

	t.Run("OpenAI usage format", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx := context.Background()

		writer, err := NewOpenAIStreamWriter(recorder, "gpt-4", ctx, WithIncludeUsage(true))
		if err != nil {
			t.Fatalf("Failed to create OpenAI writer: %v", err)
		}

		// Convert Usage to OpenAIUsage
		openAIUsage := &OpenAIUsage{
			PromptTokens:     usage.InputTokens,
			CompletionTokens: usage.OutputTokens,
			TotalTokens:      usage.InputTokens + usage.OutputTokens,
		}

		// Write content with usage
		_ = writer.WriteInitialAssistantTextDelta()
		_ = writer.WriteDelta("Test", nil, nil)
		_ = writer.WriteFinish("stop", openAIUsage)

		body := recorder.Body.String()

		// OpenAI should have usage in a separate chunk after finish_reason
		if !strings.Contains(body, "\"usage\":") {
			t.Error("OpenAI stream should include usage when requested")
		}
		if !strings.Contains(body, "\"prompt_tokens\":10") {
			t.Error("OpenAI usage should have prompt_tokens")
		}
		if !strings.Contains(body, "\"completion_tokens\":20") {
			t.Error("OpenAI usage should have completion_tokens")
		}
		if !strings.Contains(body, "\"total_tokens\":30") {
			t.Error("OpenAI usage should have total_tokens")
		}
	})

	t.Run("Anthropic usage format", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx := context.Background()

		handler, err := NewAnthropicStreamHandler(recorder, "claude-3-opus", ctx)
		if err != nil {
			t.Fatalf("Failed to create Anthropic handler: %v", err)
		}

		// Start and finish with usage
		_ = handler.SendMessageStart()
		_ = handler.SendContentBlockStart(0, "text")
		_ = handler.SendTextDelta("Test")
		_ = handler.FinishStream("end_turn", usage)

		body := recorder.Body.String()

		// Anthropic should have usage in message_delta event
		if !strings.Contains(body, "\"usage\":") {
			t.Error("Anthropic stream should include usage in message_delta")
		}
		if !strings.Contains(body, "\"input_tokens\":10") {
			t.Error("Anthropic usage should have input_tokens")
		}
		if !strings.Contains(body, "\"output_tokens\":20") {
			t.Error("Anthropic usage should have output_tokens")
		}
	})
}

// TestMixedContentStreaming tests streaming with both text and tool calls
func TestMixedContentStreaming(t *testing.T) {
	t.Run("OpenAI mixed content", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx := context.Background()

		writer, err := NewOpenAIStreamWriter(recorder, "gpt-4", ctx)
		if err != nil {
			t.Fatalf("Failed to create OpenAI writer: %v", err)
		}

		// Stream text followed by tool call
		_ = writer.WriteInitialAssistantTextDelta()
		_ = writer.WriteDelta("Let me help you with that. ", nil, nil)

		// Create tool call
		toolCall := &ToolCallDelta{
			Index: 0,
			ID:    "call_123",
			Type:  "function",
			Function: &FunctionCallDelta{
				Name:      "get_weather",
				Arguments: "",
			},
		}

		// Write tool call with name
		_ = writer.WriteToolCallDelta(0, toolCall, nil, nil)

		// Write tool call arguments in chunks
		toolCallArgs1 := &ToolCallDelta{
			Index: 0,
			Function: &FunctionCallDelta{
				Arguments: "{\"location\":",
			},
		}
		_ = writer.WriteToolCallDelta(0, toolCallArgs1, nil, nil)

		toolCallArgs2 := &ToolCallDelta{
			Index: 0,
			Function: &FunctionCallDelta{
				Arguments: "\"NYC\"}",
			},
		}
		_ = writer.WriteToolCallDelta(0, toolCallArgs2, nil, nil)

		_ = writer.WriteFinish("tool_calls", nil)

		body := recorder.Body.String()

		// Verify both text and tool calls are present
		if !strings.Contains(body, "Let me help you with that") {
			t.Error("OpenAI stream should contain text content")
		}
		if !strings.Contains(body, "\"tool_calls\":") {
			t.Error("OpenAI stream should contain tool_calls")
		}
		if !strings.Contains(body, "\"function\":{\"name\":\"get_weather\"") {
			t.Error("OpenAI stream should contain function name")
		}
		if !strings.Contains(body, "[DONE]") {
			t.Error("OpenAI mixed content stream should still end with [DONE]")
		}
	})

	t.Run("Anthropic mixed content", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx := context.Background()

		handler, err := NewAnthropicStreamHandler(recorder, "claude-3-opus", ctx)
		if err != nil {
			t.Fatalf("Failed to create Anthropic handler: %v", err)
		}

		// Send message start
		_ = handler.SendMessageStart()

		// Stream text content
		_ = handler.SendContentBlockStart(0, "text")
		_ = handler.SendTextDelta("Let me help you with that. ")
		_ = handler.SendContentBlockStop(0)

		// Stream tool use
		_ = handler.SendToolUseStart(1, "toolu_123", "get_weather")
		_ = handler.SendToolInputDelta(1, "") // Empty first delta per Anthropic format
		_ = handler.SendToolInputDelta(1, "{\"location\": \"NYC\"}")
		_ = handler.SendContentBlockStop(1)

		// Finish
		_ = handler.FinishStream("tool_use", nil)

		body := recorder.Body.String()

		// Verify both text and tool use are present
		if !strings.Contains(body, "Let me help you with that") {
			t.Error("Anthropic stream should contain text content")
		}
		if !strings.Contains(body, `"type":"tool_use"`) {
			t.Error("Anthropic stream should contain tool_use type")
		}
		if !strings.Contains(body, `"name":"get_weather"`) {
			t.Error("Anthropic stream should contain tool name")
		}
		if strings.Contains(body, "[DONE]") {
			t.Error("Anthropic mixed content stream should NOT send [DONE]")
		}
		if !strings.Contains(body, "event: message_stop") {
			t.Error("Anthropic mixed content stream should end with message_stop")
		}
	})
}
