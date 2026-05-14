package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/core"
	"strings"
	"testing"
)

func TestConvertAnthropicToOpenAI_OmitsZeroMaxCompletionTokens(t *testing.T) {
	converter := &Converter{}
	req := &AnthropicRequest{
		Model: "gpt-5.4-nano",
		Messages: []AnthropicMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"Hello"`),
			},
		},
	}

	openAIReq, err := converter.ConvertAnthropicToOpenAI(context.Background(), req)
	if err != nil {
		t.Fatalf("ConvertAnthropicToOpenAI() error = %v", err)
	}
	if openAIReq.MaxCompletionTokens != nil {
		t.Fatalf("MaxCompletionTokens = %v, want nil", openAIReq.MaxCompletionTokens)
	}
}

func TestConvertAnthropicResponseToOpenAIUsesCustomToolRegistry(t *testing.T) {
	converter := &Converter{}
	registry := responseToolNameRegistryFromCoreTools([]core.ToolDefinition{{
		Type: "custom",
		Name: "apply_patch",
	}})
	resp := &AnthropicResponse{
		ID:         "msg_1",
		Type:       "message",
		Role:       core.RoleAssistant,
		Model:      "claude-test",
		StopReason: "tool_use",
		Content: []AnthropicContentBlock{{
			Type:  "tool_use",
			ID:    "toolu_1",
			Name:  "apply_patch",
			Input: map[string]interface{}{core.CustomToolInputField: "raw patch"},
		}},
	}

	got := converter.ConvertAnthropicResponseToOpenAIWithToolNameRegistry(resp, "gpt-public", registry)
	calls := got.Choices[0].Message.ToolCalls
	if len(calls) != 1 || calls[0].Type != "custom" || calls[0].Custom == nil {
		t.Fatalf("tool calls = %+v, want one custom call", calls)
	}
	if calls[0].Custom.Name != "apply_patch" || calls[0].Custom.Input != "raw patch" {
		t.Fatalf("custom call = %+v", calls[0].Custom)
	}
}

func TestConvertAnthropicToOpenAI_LoggingRegression(t *testing.T) {
	SetupTestLogger(t)

	tests := []struct {
		name    string
		request *AnthropicRequest
	}{
		{
			name: "metadata omission logs valid JSON",
			request: &AnthropicRequest{
				Model:     "gpt-4o",
				MaxTokens: 100,
				Messages: []AnthropicMessage{
					{
						Role:    "user",
						Content: json.RawMessage(`"test message"`),
					},
				},
				Metadata: map[string]interface{}{
					"user_id": "12345",
					"session": "abc-def-ghi",
					"nested": map[string]interface{}{
						"key": "value",
					},
				},
			},
		},
		{
			name: "thinking block dropping logs valid JSON",
			request: &AnthropicRequest{
				Model:     "gpt-4o",
				MaxTokens: 100,
				Messages: []AnthropicMessage{
					{
						Role:    "assistant",
						Content: json.RawMessage(`[{"type":"thinking","thinking":"internal thoughts"},{"type":"text","text":"response"}]`),
					},
				},
			},
		},
		{
			name: "top_k omission logs correctly",
			request: &AnthropicRequest{
				Model:     "gpt-4o",
				MaxTokens: 100,
				TopK:      intPtrTest(10),
				Messages: []AnthropicMessage{
					{
						Role:    "user",
						Content: json.RawMessage(`"test message"`),
					},
				},
			},
		},
	}

	converter := &Converter{}
	ctx := context.Background()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Run the conversion
			result, err := converter.ConvertAnthropicToOpenAI(ctx, tt.request)
			// Verify no error
			if err != nil {
				t.Errorf("ConvertAnthropicToOpenAI() error = %v", err)
			}
			if result == nil {
				t.Error("ConvertAnthropicToOpenAI() returned nil result")
			}

			// The main test is that it doesn't panic and doesn't produce %!s(MISSING)
			// In a real test with log capture, we'd verify the log output
		})
	}
}

func TestConvertAnthropicToOpenAI_ReasoningBlock(t *testing.T) {
	SetupTestLogger(t)

	converter := &Converter{}
	ctx := context.Background()

	// Request with Anthropic reasoning content
	req := &AnthropicRequest{
		Model:     "gpt-4o",
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{
				Role: "assistant",
				Content: json.RawMessage(`[
					{"type":"thinking","thinking":"Let me think about this..."},
					{"type":"text","text":"Here is my response"}
				]`),
			},
		},
	}

	result, err := converter.ConvertAnthropicToOpenAI(ctx, req)
	if err != nil {
		t.Fatalf("ConvertAnthropicToOpenAI() error = %v", err)
	}
	if result == nil {
		t.Fatal("ConvertAnthropicToOpenAI() returned nil")
	}

	// Verify the reasoning block was dropped from the OpenAI Chat output
	if len(result.Messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(result.Messages))
	}
	if result.Messages[0].Content != "Here is my response" {
		t.Errorf("Expected content 'Here is my response', got %v", result.Messages[0].Content)
	}
}

func TestConvertAnthropicToOpenAI_PreservesValidContent(t *testing.T) {
	SetupTestLogger(t)

	converter := &Converter{}
	ctx := context.Background()

	// Request with various content types
	req := &AnthropicRequest{
		Model:     "gpt-4o",
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"What is 2+2?"`),
			},
			{
				Role: "assistant",
				Content: json.RawMessage(`[
					{"type":"text","text":"2+2 equals 4"}
				]`),
			},
			{
				Role: "user",
				Content: json.RawMessage(`[
					{"type":"text","text":"What about 3+3?"}
				]`),
			},
		},
		Tools: []AnthropicTool{
			{
				Name:        "calculator",
				Description: "Performs calculations",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"expression": map[string]interface{}{
							"type": "string",
						},
					},
				},
			},
		},
	}

	result, err := converter.ConvertAnthropicToOpenAI(ctx, req)
	if err != nil {
		t.Fatalf("ConvertAnthropicToOpenAI() error = %v", err)
	}
	if result == nil {
		t.Fatal("ConvertAnthropicToOpenAI() returned nil")
	}

	// Verify messages were converted correctly
	if len(result.Messages) != 3 {
		t.Errorf("Expected 3 messages, got %d", len(result.Messages))
	}
	if result.Messages[0].Content != "What is 2+2?" {
		t.Errorf("Message 0: expected 'What is 2+2?', got %v", result.Messages[0].Content)
	}
	if result.Messages[1].Content != "2+2 equals 4" {
		t.Errorf("Message 1: expected '2+2 equals 4', got %v", result.Messages[1].Content)
	}
	if result.Messages[2].Content != "What about 3+3?" {
		t.Errorf("Message 2: expected 'What about 3+3?', got %v", result.Messages[2].Content)
	}

	// Verify tools were converted
	if len(result.Tools) != 1 {
		t.Errorf("Expected 1 tool, got %d", len(result.Tools))
	}
	if result.Tools[0].Function.Name != "calculator" {
		t.Errorf("Expected tool name 'calculator', got %s", result.Tools[0].Function.Name)
	}
}

func TestMetadataLoggingFormat(t *testing.T) {
	// This test verifies that metadata is logged as JSON, not as Go map format
	SetupTestLogger(t)

	converter := &Converter{}
	ctx := context.Background()

	metadata := map[string]interface{}{
		"key1": "value1",
		"key2": 123,
		"nested": map[string]interface{}{
			"innerKey": "innerValue",
		},
	}

	req := &AnthropicRequest{
		Model:     "gpt-4o",
		MaxTokens: 100,
		Metadata:  metadata,
		Messages: []AnthropicMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"test"`),
			},
		},
	}

	_, err := converter.ConvertAnthropicToOpenAI(ctx, req)
	if err != nil {
		t.Fatalf("ConvertAnthropicToOpenAI() error = %v", err)
	}

	// The main test is that it uses DebugJSON and doesn't panic
	// In a real test with log capture, we would verify:
	// - The log contains valid JSON
	// - The log does NOT contain "map[" which would indicate Go's map format
	// - The log does NOT contain "%!s(MISSING)"
}

func TestThinkingConversion(t *testing.T) {
	SetupTestLogger(t)

	converter := &Converter{}
	ctx := context.Background()

	// Test thinking conversion for GPT models
	req := &AnthropicRequest{
		Model:     "gpt-4o",
		MaxTokens: 100,
		Thinking: &AnthropicThinking{
			Type:         "enabled",
			BudgetTokens: 1000,
		},
		Messages: []AnthropicMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"test"`),
			},
		},
	}

	result, err := converter.ConvertAnthropicToOpenAI(ctx, req)
	if err != nil {
		t.Fatalf("ConvertAnthropicToOpenAI() error = %v", err)
	}

	// Should convert to reasoning_effort for GPT models
	if result.ReasoningEffort != "high" {
		t.Errorf("Expected reasoning_effort='high', got %s", result.ReasoningEffort)
	}
}

func TestToolConversion(t *testing.T) {
	SetupTestLogger(t)

	converter := &Converter{}
	ctx := context.Background()

	req := &AnthropicRequest{
		Model:     "gpt-4o",
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"test"`),
			},
		},
		Tools: []AnthropicTool{
			{
				Name:        "test_tool",
				Description: "A test tool",
				InputSchema: map[string]interface{}{
					"$schema": "http://json-schema.org/draft-07/schema#",
					"type":    "object",
					"properties": map[string]interface{}{
						"param1": map[string]interface{}{
							"type": "string",
						},
					},
				},
			},
		},
		ToolChoice: &AnthropicToolChoice{
			Type: "tool",
			Name: "test_tool",
		},
	}

	result, err := converter.ConvertAnthropicToOpenAI(ctx, req)
	if err != nil {
		t.Fatalf("ConvertAnthropicToOpenAI() error = %v", err)
	}

	// Verify tool was converted
	if len(result.Tools) != 1 {
		t.Errorf("Expected 1 tool, got %d", len(result.Tools))
	}
	if result.Tools[0].Function.Name != "test_tool" {
		t.Errorf("Expected tool name 'test_tool', got %s", result.Tools[0].Function.Name)
	}

	// Verify $schema was filtered out
	params := result.Tools[0].Function.Parameters
	if paramsMap, ok := params.(map[string]interface{}); ok {
		if _, hasSchema := paramsMap["$schema"]; hasSchema {
			t.Error("$schema should be filtered out from parameters")
		}
	}

	// Verify tool_choice was converted
	toolChoice, ok := result.ToolChoice.(map[string]interface{})
	if !ok {
		t.Fatalf("ToolChoice should be a map, got %T", result.ToolChoice)
	}
	if toolChoice["type"] != "function" {
		t.Errorf("Expected tool_choice type='function', got %v", toolChoice["type"])
	}
	funcMap, ok := toolChoice["function"].(map[string]string)
	if !ok {
		t.Fatalf("tool_choice.function should be a map, got %T", toolChoice["function"])
	}
	if funcMap["name"] != "test_tool" {
		t.Errorf("Expected tool_choice function name='test_tool', got %s", funcMap["name"])
	}
}

func TestToolCallConversion(t *testing.T) {
	SetupTestLogger(t)

	converter := &Converter{}
	ctx := context.Background()

	// Test converting assistant message with tool calls
	req := &AnthropicRequest{
		Model:     "gpt-4o",
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{
				Role: "assistant",
				Content: json.RawMessage(`[
					{"type":"text","text":"I'll help you with that."},
					{"type":"tool_use","id":"call_123","name":"calculator","input":{"expression":"2+2"}}
				]`),
			},
			{
				Role: "user",
				Content: json.RawMessage(`[
					{"type":"tool_result","tool_use_id":"call_123","content":"4"}
				]`),
			},
		},
	}

	result, err := converter.ConvertAnthropicToOpenAI(ctx, req)
	if err != nil {
		t.Fatalf("ConvertAnthropicToOpenAI() error = %v", err)
	}

	if len(result.Messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d", len(result.Messages))
	}

	msg := result.Messages[0]
	if msg.Content != nil {
		t.Error("Content should be nil when tool_calls are present")
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(msg.ToolCalls))
	}

	toolCall := msg.ToolCalls[0]
	if toolCall.ID != "call_123" {
		t.Errorf("Expected tool call ID 'call_123', got %s", toolCall.ID)
	}
	if toolCall.Function.Name != "calculator" {
		t.Errorf("Expected tool call name 'calculator', got %s", toolCall.Function.Name)
	}
	if !strings.Contains(toolCall.Function.Arguments, "expression") {
		t.Errorf("Tool call arguments should contain 'expression', got %s", toolCall.Function.Arguments)
	}
	if result.Messages[1].Role != "tool" || result.Messages[1].ToolCallID != "call_123" {
		t.Fatalf("Expected matching tool message after tool_calls, got %#v", result.Messages[1])
	}
}

// Helper function to create int pointer
func intPtrTest(i int) *int {
	return &i
}
