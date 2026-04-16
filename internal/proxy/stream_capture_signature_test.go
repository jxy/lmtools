package proxy

import (
	"context"
	"lmtools/internal/apifixtures"
	"strings"
	"testing"
)

type openAIStreamSignature struct {
	initialHasRole      bool
	initialContentKind  string
	initialHasToolCalls bool
	finishReason        string
	hasDone             bool
	hasToolArgChunk     bool
}

type anthropicStreamSignature struct {
	firstBlockType        string
	firstToolInputIsEmpty bool
	stopReason            string
	hasMessageStop        bool
}

func TestSimulatedOpenAIStreamMatchesLiveOpenAIShape(t *testing.T) {
	suite, err := apifixtures.LoadSuite()
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}

	t.Run("text", func(t *testing.T) {
		actualRaw, err := apifixtures.ReadCaseFile(suite.Root, "anthropic-messages-basic-text", "captures/openai-stream.stream.txt")
		if err != nil {
			t.Fatalf("ReadCaseFile() error = %v", err)
		}

		recorder := newFlushableRecorder()
		writer, err := NewOpenAIStreamWriter(recorder, "gpt-5.4-nano", context.Background())
		if err != nil {
			t.Fatalf("NewOpenAIStreamWriter() error = %v", err)
		}
		if err := streamSimulatedContentBlocks(context.Background(), []AnthropicContentBlock{
			{Type: "text", Text: "Hello from simulated streaming."},
		}, &openAISimulatedContentEmitter{writer: writer}); err != nil {
			t.Fatalf("streamSimulatedContentBlocks() error = %v", err)
		}
		if err := writer.WriteFinish("stop", nil); err != nil {
			t.Fatalf("WriteFinish() error = %v", err)
		}

		actualSig := projectOpenAISignature(projectOpenAIStream(string(actualRaw)))
		simSig := projectOpenAISignature(projectOpenAIStream(recorder.Body.String()))

		if simSig.initialContentKind != actualSig.initialContentKind {
			t.Fatalf("initial content kind = %q, want %q", simSig.initialContentKind, actualSig.initialContentKind)
		}
		if simSig.initialHasToolCalls != actualSig.initialHasToolCalls {
			t.Fatalf("initial has_tool_calls = %v, want %v", simSig.initialHasToolCalls, actualSig.initialHasToolCalls)
		}
		if simSig.finishReason != actualSig.finishReason {
			t.Fatalf("finish reason = %q, want %q", simSig.finishReason, actualSig.finishReason)
		}
		if !simSig.initialHasRole || !simSig.hasDone {
			t.Fatalf("simulated signature = %+v, want role-bearing start and [DONE]", simSig)
		}
	})

	t.Run("tool", func(t *testing.T) {
		actualRaw, err := apifixtures.ReadCaseFile(suite.Root, "openai-tool-followup", "captures/openai-stream.stream.txt")
		if err != nil {
			t.Fatalf("ReadCaseFile() error = %v", err)
		}

		recorder := newFlushableRecorder()
		writer, err := NewOpenAIStreamWriter(recorder, "gpt-5.4-nano", context.Background())
		if err != nil {
			t.Fatalf("NewOpenAIStreamWriter() error = %v", err)
		}
		if err := streamSimulatedContentBlocks(context.Background(), []AnthropicContentBlock{
			{
				Type:  "tool_use",
				ID:    "call_simulated_weather",
				Name:  "get_weather",
				Input: map[string]interface{}{"location": "Chicago", "unit": "fahrenheit"},
			},
		}, &openAISimulatedContentEmitter{writer: writer}); err != nil {
			t.Fatalf("streamSimulatedContentBlocks() error = %v", err)
		}
		if err := writer.WriteFinish("tool_calls", nil); err != nil {
			t.Fatalf("WriteFinish() error = %v", err)
		}

		actualSig := projectOpenAISignature(projectOpenAIStream(string(actualRaw)))
		simSig := projectOpenAISignature(projectOpenAIStream(recorder.Body.String()))

		if simSig.initialContentKind != actualSig.initialContentKind {
			t.Fatalf("initial content kind = %q, want %q", simSig.initialContentKind, actualSig.initialContentKind)
		}
		if simSig.initialHasToolCalls != actualSig.initialHasToolCalls {
			t.Fatalf("initial has_tool_calls = %v, want %v", simSig.initialHasToolCalls, actualSig.initialHasToolCalls)
		}
		if simSig.finishReason != actualSig.finishReason {
			t.Fatalf("finish reason = %q, want %q", simSig.finishReason, actualSig.finishReason)
		}
		if !simSig.hasToolArgChunk || !simSig.hasDone {
			t.Fatalf("simulated signature = %+v, want tool-argument chunks and [DONE]", simSig)
		}
	})
}

func TestSimulatedAnthropicStreamMatchesLiveAnthropicShape(t *testing.T) {
	suite, err := apifixtures.LoadSuite()
	if err != nil {
		t.Fatalf("LoadSuite() error = %v", err)
	}

	t.Run("text", func(t *testing.T) {
		actualRaw, err := apifixtures.ReadCaseFile(suite.Root, "anthropic-messages-basic-text", "captures/anthropic-stream.stream.txt")
		if err != nil {
			t.Fatalf("ReadCaseFile() error = %v", err)
		}

		recorder := newFlushableRecorder()
		handler, err := NewAnthropicStreamHandler(recorder, "claude-haiku-4.5", context.Background())
		if err != nil {
			t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
		}
		if err := handler.SendMessageStart(); err != nil {
			t.Fatalf("SendMessageStart() error = %v", err)
		}
		if err := streamSimulatedContentBlocks(context.Background(), []AnthropicContentBlock{
			{Type: "text", Text: "Hello from simulated streaming."},
		}, anthropicSimulatedContentEmitter{ctx: context.Background(), handler: handler}); err != nil {
			t.Fatalf("streamSimulatedContentBlocks() error = %v", err)
		}
		if err := handler.FinishStream("end_turn", nil); err != nil {
			t.Fatalf("FinishStream() error = %v", err)
		}

		actualSig := projectAnthropicSignature(projectAnthropicStream(string(actualRaw)))
		simSig := projectAnthropicSignature(projectAnthropicStream(recorder.Body.String()))

		if simSig.firstBlockType != actualSig.firstBlockType {
			t.Fatalf("first block type = %q, want %q", simSig.firstBlockType, actualSig.firstBlockType)
		}
		if simSig.stopReason != actualSig.stopReason {
			t.Fatalf("stop reason = %q, want %q", simSig.stopReason, actualSig.stopReason)
		}
		if !simSig.hasMessageStop {
			t.Fatalf("simulated signature = %+v, want message_stop", simSig)
		}
	})

	t.Run("tool", func(t *testing.T) {
		actualRaw, err := apifixtures.ReadCaseFile(suite.Root, "openai-tool-followup", "captures/anthropic-stream.stream.txt")
		if err != nil {
			t.Fatalf("ReadCaseFile() error = %v", err)
		}

		recorder := newFlushableRecorder()
		handler, err := NewAnthropicStreamHandler(recorder, "claude-haiku-4.5", context.Background())
		if err != nil {
			t.Fatalf("NewAnthropicStreamHandler() error = %v", err)
		}
		if err := handler.SendMessageStart(); err != nil {
			t.Fatalf("SendMessageStart() error = %v", err)
		}
		if err := streamSimulatedContentBlocks(context.Background(), []AnthropicContentBlock{
			{
				Type:  "tool_use",
				ID:    "toolu_simulated_weather",
				Name:  "get_weather",
				Input: map[string]interface{}{"location": "Chicago", "unit": "fahrenheit"},
			},
		}, anthropicSimulatedContentEmitter{ctx: context.Background(), handler: handler}); err != nil {
			t.Fatalf("streamSimulatedContentBlocks() error = %v", err)
		}
		if err := handler.FinishStream("tool_use", nil); err != nil {
			t.Fatalf("FinishStream() error = %v", err)
		}

		actualSig := projectAnthropicSignature(projectAnthropicStream(string(actualRaw)))
		simSig := projectAnthropicSignature(projectAnthropicStream(recorder.Body.String()))

		if simSig.firstBlockType != actualSig.firstBlockType {
			t.Fatalf("first block type = %q, want %q", simSig.firstBlockType, actualSig.firstBlockType)
		}
		if simSig.firstToolInputIsEmpty != actualSig.firstToolInputIsEmpty {
			t.Fatalf("first tool input empty = %v, want %v", simSig.firstToolInputIsEmpty, actualSig.firstToolInputIsEmpty)
		}
		if simSig.stopReason != actualSig.stopReason {
			t.Fatalf("stop reason = %q, want %q", simSig.stopReason, actualSig.stopReason)
		}
		if !simSig.hasMessageStop {
			t.Fatalf("simulated signature = %+v, want message_stop", simSig)
		}
	})
}

func projectOpenAISignature(projected []map[string]interface{}) openAIStreamSignature {
	sig := openAIStreamSignature{}
	if len(projected) == 0 {
		return sig
	}

	first := projected[0]
	if delta, ok := first["delta"].(map[string]interface{}); ok {
		_, sig.initialHasRole = delta["role"]
		if toolCalls, ok := delta["tool_calls"].([]interface{}); ok && len(toolCalls) > 0 {
			sig.initialHasToolCalls = true
		}
		if content, ok := delta["content"]; ok {
			switch v := content.(type) {
			case nil:
				sig.initialContentKind = "null"
			case string:
				if v == "" {
					sig.initialContentKind = "empty"
				} else {
					sig.initialContentKind = "text"
				}
			default:
				sig.initialContentKind = "other"
			}
		} else {
			sig.initialContentKind = "absent"
		}
	}

	for _, entry := range projected {
		if done, _ := entry["done"].(bool); done {
			sig.hasDone = true
		}
		if finishReason, _ := entry["finish_reason"].(string); finishReason != "" {
			sig.finishReason = finishReason
		}
		delta, _ := entry["delta"].(map[string]interface{})
		toolCalls, _ := delta["tool_calls"].([]interface{})
		for _, rawToolCall := range toolCalls {
			toolCall, _ := rawToolCall.(map[string]interface{})
			function, _ := toolCall["function"].(map[string]interface{})
			if args, _ := function["arguments"].(string); strings.TrimSpace(args) != "" {
				sig.hasToolArgChunk = true
			}
		}
	}

	return sig
}

func projectAnthropicSignature(projected []map[string]interface{}) anthropicStreamSignature {
	sig := anthropicStreamSignature{}
	for _, entry := range projected {
		if sig.firstBlockType == "" {
			if blockType, _ := entry["block_type"].(string); blockType != "" {
				sig.firstBlockType = blockType
			}
		}
		if entry["event"] == "content_block_delta" && entry["delta_type"] == "input_json_delta" && entry["partial_json"] == "" {
			sig.firstToolInputIsEmpty = true
		}
		if entry["event"] == "message_delta" {
			if stopReason, _ := entry["stop_reason"].(string); stopReason != "" {
				sig.stopReason = stopReason
			}
		}
		if entry["event"] == "message_stop" {
			sig.hasMessageStop = true
		}
	}
	return sig
}
