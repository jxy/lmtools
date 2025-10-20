package core

// Test helper functions for message marshaling
// These helpers reduce duplication in test files by providing common conversion utilities

// TestMarshalTypedMessages converts TypedMessage to []interface{} for test request bodies
// This helper is used in tests to avoid repeating the conversion logic
func TestMarshalTypedMessages(messages []TypedMessage) []interface{} {
	result := make([]interface{}, 0, len(messages))
	for _, msg := range messages {
		msgMap := map[string]interface{}{
			"role": msg.Role,
		}

		// Convert blocks to content
		if len(msg.Blocks) == 1 {
			if textBlock, ok := msg.Blocks[0].(TextBlock); ok {
				msgMap["content"] = textBlock.Text
			}
		} else if len(msg.Blocks) > 0 {
			contentArray := make([]interface{}, 0, len(msg.Blocks))
			for _, block := range msg.Blocks {
				switch b := block.(type) {
				case TextBlock:
					contentArray = append(contentArray, map[string]interface{}{
						"type": "text",
						"text": b.Text,
					})
				case ToolUseBlock:
					contentArray = append(contentArray, map[string]interface{}{
						"type":  "tool_use",
						"id":    b.ID,
						"name":  b.Name,
						"input": b.Input,
					})
				case ToolResultBlock:
					contentArray = append(contentArray, map[string]interface{}{
						"type":        "tool_result",
						"tool_use_id": b.ToolUseID,
						"content":     b.Content,
						"is_error":    b.IsError,
					})
				}
			}
			msgMap["content"] = contentArray
		}

		result = append(result, msgMap)
	}
	return result
}

// TestMarshalOpenAIMessages converts OpenAI messages to []interface{} for test request bodies
// This is a test-specific wrapper around MarshalOpenAIMessagesForRequest
func TestMarshalOpenAIMessages(messages []OpenAIMessage) []interface{} {
	return MarshalOpenAIMessagesForRequest(messages)
}

// TestMarshalAnthropicMessages converts Anthropic messages to []interface{} for test request bodies
// This is a test-specific wrapper around MarshalAnthropicMessagesForRequest
func TestMarshalAnthropicMessages(messages []AnthropicMessage) []interface{} {
	return MarshalAnthropicMessagesForRequest(messages)
}

// TestMarshalGoogleMessages converts Google messages to []interface{} for test request bodies
// This is a test-specific wrapper around MarshalGoogleMessagesForRequest
func TestMarshalGoogleMessages(messages []GoogleMessage) []interface{} {
	return MarshalGoogleMessagesForRequest(messages)
}
