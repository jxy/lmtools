package proxy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestConvertAnthropicToOpenAI_DropsThinking(t *testing.T) {
	c := &Converter{}
	ctx := context.Background()

	req := &AnthropicRequest{
		Model:     "gpt-4",
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"Hello"`),
			},
			{
				Role: "assistant",
				Content: json.RawMessage(`[
					{"type": "thinking", "thinking": "Let me think about this greeting...", "signature": "sig_fixture"},
					{"type": "text", "text": "Hello! How can I help you?"}
				]`),
			},
			{
				Role:    "user",
				Content: json.RawMessage(`"What's 2+2?"`),
			},
		},
	}

	openAIReq, err := c.ConvertAnthropicToOpenAI(ctx, req)
	if err != nil {
		t.Fatalf("Failed to convert: %v", err)
	}

	// Check that we have the right number of messages
	if len(openAIReq.Messages) != 3 {
		t.Errorf("Expected 3 messages, got %d", len(openAIReq.Messages))
	}

	// Check the assistant message content
	for i, msg := range openAIReq.Messages {
		if msg.Role == "assistant" {
			content, ok := msg.Content.(string)
			if !ok {
				t.Errorf("Expected string content for assistant message, got %T", msg.Content)
				continue
			}

			// Should only contain the text content, not thinking
			if strings.Contains(content, "thinking") || strings.Contains(content, "Let me think") {
				t.Errorf("Assistant message at index %d still contains thinking content: %s", i, content)
			}

			if content != "Hello! How can I help you?" {
				t.Errorf("Expected assistant content 'Hello! How can I help you?', got: %s", content)
			}
		}
	}
}

func TestConvertAnthropicToArgo_DropsThinkingForOpenAI(t *testing.T) {
	c := &Converter{}
	ctx := context.Background()

	req := &AnthropicRequest{
		Model:     "gpt-4",
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"Hello"`),
			},
			{
				Role: "assistant",
				Content: json.RawMessage(`[
					{"type": "thinking", "thinking": "Let me think about this greeting...", "signature": "sig_fixture"},
					{"type": "text", "text": "Hello! How can I help you?"}
				]`),
			},
		},
	}

	argoReq, err := c.ConvertAnthropicToArgo(ctx, req, "testuser")
	if err != nil {
		t.Fatalf("Failed to convert: %v", err)
	}

	// Check that we have the right number of messages
	if len(argoReq.Messages) != 2 {
		t.Errorf("Expected 2 messages, got %d", len(argoReq.Messages))
	}

	// Check the assistant message content
	for i, msg := range argoReq.Messages {
		if msg.Role == "assistant" {
			content, ok := msg.Content.(string)
			if !ok {
				// For OpenAI provider, content should be a string
				t.Errorf("Expected string content for assistant message with GPT model, got %T", msg.Content)
				continue
			}

			// Should only contain the text content, not thinking
			if strings.Contains(content, "thinking") || strings.Contains(content, "Let me think") {
				t.Errorf("Assistant message at index %d still contains thinking content: %s", i, content)
			}

			if content != "Hello! How can I help you?" {
				t.Errorf("Expected assistant content 'Hello! How can I help you?', got: %s", content)
			}
		}
	}
}

func TestConvertAnthropicToArgo_PreservesThinkingForClaude(t *testing.T) {
	c := &Converter{}
	ctx := context.Background()

	req := &AnthropicRequest{
		Model:     "claude-3-opus",
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"Hello"`),
			},
			{
				Role: "assistant",
				Content: json.RawMessage(`[
					{"type": "thinking", "thinking": "Let me think about this greeting...", "signature": "sig_fixture"},
					{"type": "text", "text": "Hello! How can I help you?"}
				]`),
			},
		},
	}

	argoReq, err := c.ConvertAnthropicToArgo(ctx, req, "testuser")
	if err != nil {
		t.Fatalf("Failed to convert: %v", err)
	}

	// For Claude models, the content array should be preserved as-is
	for _, msg := range argoReq.Messages {
		if msg.Role == "assistant" {
			contentArray, ok := msg.Content.([]AnthropicContentBlock)
			if !ok {
				t.Errorf("Expected []AnthropicContentBlock for Claude model, got %T", msg.Content)
				continue
			}

			// Should contain both thinking and text blocks
			if len(contentArray) != 2 {
				t.Errorf("Expected 2 content blocks for Claude model, got %d", len(contentArray))
			}

			hasThinking := false
			hasText := false
			for _, block := range contentArray {
				if block.Type == "thinking" {
					hasThinking = true
				}
				if block.Type == "text" {
					hasText = true
				}
			}

			if !hasThinking {
				t.Error("Claude model should preserve thinking blocks")
			}
			if !hasText {
				t.Error("Claude model should preserve text blocks")
			}
		}
	}
}
