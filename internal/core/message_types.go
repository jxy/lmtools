package core

import (
	"encoding/json"
)

// Block represents a content block in a message
type Block interface {
	isBlock()
}

// TextBlock represents a text content block
type TextBlock struct {
	Text string
}

func (TextBlock) isBlock() {}

// ToolUseBlock represents a tool use request block
type ToolUseBlock struct {
	ID    string
	Name  string
	Input json.RawMessage
}

func (ToolUseBlock) isBlock() {}

// ToolResultBlock represents a tool execution result block
type ToolResultBlock struct {
	ToolUseID string
	Content   string
	IsError   bool
}

func (ToolResultBlock) isBlock() {}

// TypedMessage represents a message in a conversation with typed blocks
type TypedMessage struct {
	Role   string  // "system", "user", or "assistant"
	Blocks []Block // Content blocks (text, tool use, tool results)
}

// NewTextMessage creates a TypedMessage with a single text block
func NewTextMessage(role, text string) TypedMessage {
	return TypedMessage{
		Role:   role,
		Blocks: []Block{TextBlock{Text: text}},
	}
}

// ToAnthropic converts chat messages to Anthropic API format
func ToAnthropic(messages []TypedMessage) []interface{} {
	result := make([]interface{}, 0, len(messages))

	for _, msg := range messages {
		anthropicMsg := map[string]interface{}{
			"role": msg.Role,
		}

		// Check if we have only a single text block
		if len(msg.Blocks) == 1 {
			if textBlock, ok := msg.Blocks[0].(TextBlock); ok {
				anthropicMsg["content"] = textBlock.Text
				result = append(result, anthropicMsg)
				continue
			}
		}

		// Multiple blocks or non-text blocks need content array
		content := make([]map[string]interface{}, 0, len(msg.Blocks))
		for _, block := range msg.Blocks {
			switch b := block.(type) {
			case TextBlock:
				content = append(content, map[string]interface{}{
					"type": "text",
					"text": b.Text,
				})
			case ToolUseBlock:
				toolBlock := map[string]interface{}{
					"type": "tool_use",
					"id":   b.ID,
					"name": b.Name,
				}
				if len(b.Input) > 0 {
					var input interface{}
					if err := json.Unmarshal(b.Input, &input); err == nil {
						toolBlock["input"] = input
					}
				}
				content = append(content, toolBlock)
			case ToolResultBlock:
				content = append(content, map[string]interface{}{
					"type":        "tool_result",
					"tool_use_id": b.ToolUseID,
					"content":     b.Content,
					"is_error":    b.IsError,
				})
			}
		}

		if len(content) > 0 {
			anthropicMsg["content"] = content
		}
		result = append(result, anthropicMsg)
	}

	return result
}

// ToOpenAI converts chat messages to OpenAI API format
func ToOpenAI(messages []TypedMessage) []interface{} {
	result := make([]interface{}, 0, len(messages))

	for _, msg := range messages {
		openAIMsg := map[string]interface{}{
			"role": msg.Role,
		}

		// OpenAI format differs for assistant messages with tool calls
		if msg.Role == string(RoleAssistant) {
			var textContent string
			var toolCalls []map[string]interface{}

			for _, block := range msg.Blocks {
				switch b := block.(type) {
				case TextBlock:
					textContent = b.Text
				case ToolUseBlock:
					toolCall := map[string]interface{}{
						"id":   b.ID,
						"type": "function",
						"function": map[string]interface{}{
							"name": b.Name,
						},
					}
					if len(b.Input) > 0 {
						toolCall["function"].(map[string]interface{})["arguments"] = string(b.Input)
					}
					toolCalls = append(toolCalls, toolCall)
				}
			}

			if textContent != "" {
				openAIMsg["content"] = textContent
			}
			if len(toolCalls) > 0 {
				openAIMsg["tool_calls"] = toolCalls
			}
		} else if msg.Role == string(RoleUser) {
			// Handle tool results in user messages
			var toolResults []ToolResultBlock
			var textBlocks []TextBlock

			for _, block := range msg.Blocks {
				if toolResult, ok := block.(ToolResultBlock); ok {
					toolResults = append(toolResults, toolResult)
				} else if textBlock, ok := block.(TextBlock); ok {
					textBlocks = append(textBlocks, textBlock)
				}
			}

			if len(toolResults) > 0 {
				// Create separate tool messages for each tool result
				for _, toolResult := range toolResults {
					toolMsg := map[string]interface{}{
						"role":         "tool",
						"tool_call_id": toolResult.ToolUseID,
						"content":      toolResult.Content,
					}
					result = append(result, toolMsg)
				}

				// If there are also text blocks, add a user message with the text
				if len(textBlocks) > 0 {
					userMsg := map[string]interface{}{
						"role":    "user",
						"content": textBlocks[0].Text,
					}
					result = append(result, userMsg)
				}

				// Skip the regular append since we've already added messages
				continue
			} else {
				// Regular user message with text content
				var textContent string
				for _, block := range msg.Blocks {
					if textBlock, ok := block.(TextBlock); ok {
						textContent = textBlock.Text
						break
					}
				}
				openAIMsg["content"] = textContent
			}
		} else {
			// System or other roles - simple text content
			for _, block := range msg.Blocks {
				if textBlock, ok := block.(TextBlock); ok {
					openAIMsg["content"] = textBlock.Text
					break
				}
			}
		}

		result = append(result, openAIMsg)
	}

	return result
}

// ToGoogle converts chat messages to Google AI API format
func ToGoogle(messages []TypedMessage) []interface{} {
	return toGoogleInternal(messages, false)
}

// ToGoogleForArgo converts chat messages to Google format for Argo (keeps system messages)
func ToGoogleForArgo(messages []TypedMessage) []interface{} {
	return toGoogleInternal(messages, true)
}

// toGoogleInternal converts chat messages to Google AI API format with option to keep system
func toGoogleInternal(messages []TypedMessage, keepSystem bool) []interface{} {
	result := make([]interface{}, 0, len(messages))

	for _, msg := range messages {
		// Google uses "model" for assistant, "user" for user
		role := msg.Role
		if role == "assistant" {
			role = "model"
		}

		googleMsg := map[string]interface{}{
			"role": role,
		}

		// Convert blocks to parts
		parts := make([]map[string]interface{}, 0, len(msg.Blocks))
		for _, block := range msg.Blocks {
			switch b := block.(type) {
			case TextBlock:
				parts = append(parts, map[string]interface{}{
					"text": b.Text,
				})
			case ToolUseBlock:
				functionCall := map[string]interface{}{
					"name": b.Name,
				}
				if len(b.Input) > 0 {
					var args interface{}
					if err := json.Unmarshal(b.Input, &args); err == nil {
						functionCall["args"] = args
					}
				}
				parts = append(parts, map[string]interface{}{
					"functionCall": functionCall,
				})
			case ToolResultBlock:
				parts = append(parts, map[string]interface{}{
					"functionResponse": map[string]interface{}{
						"name": b.ToolUseID, // Google uses name to match the call
						"response": map[string]interface{}{
							"content": b.Content,
							"error":   b.IsError,
						},
					},
				})
			}
		}

		if len(parts) > 0 {
			googleMsg["parts"] = parts
		}

		// Skip system messages for Google (handle separately) unless keepSystem is true
		if msg.Role != string(RoleSystem) || keepSystem {
			result = append(result, googleMsg)
		}
	}

	return result
}
