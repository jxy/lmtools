package core

import (
	"encoding/json"
)

// ARCHITECTURAL NOTE: These conversion functions use strongly typed structures
// instead of map[string]interface{}. This ensures type safety and makes the
// code more maintainable. All conversions go through TypedMessage as the
// canonical internal representation.

// ToAnthropicTyped converts TypedMessage to strongly typed Anthropic format
func ToAnthropicTyped(messages []TypedMessage) []AnthropicMessage {
	result := make([]AnthropicMessage, 0, len(messages))

	for _, msg := range messages {
		anthMsg := AnthropicMessage{
			Role: msg.Role,
		}

		// Check if we have only a single text block
		if len(msg.Blocks) == 1 {
			if textBlock, ok := msg.Blocks[0].(TextBlock); ok {
				anthMsg.Content = textBlock.Text
				result = append(result, anthMsg)
				continue
			}
		}

		// Multiple blocks or non-text blocks need content array
		content := make([]AnthropicContent, 0, len(msg.Blocks))
		for _, block := range msg.Blocks {
			switch b := block.(type) {
			case TextBlock:
				content = append(content, AnthropicContent{
					Type: "text",
					Text: b.Text,
				})
			case ImageBlock:
				content = append(content, AnthropicContent{
					Type: "image",
					Source: &AnthropicImageSource{
						Type:      "url",
						URL:       b.URL,
						MediaType: DetectImageMediaType(b.URL),
					},
				})
			case AudioBlock:
				audioContent := AnthropicContent{
					Type: "input_audio",
				}
				inputAudioMap := make(map[string]interface{})

				if b.Data != "" {
					inputAudioMap["data"] = b.Data
					if b.Format != "" {
						inputAudioMap["format"] = b.Format
					} else {
						inputAudioMap["format"] = "wav"
					}
				} else if b.ID != "" {
					inputAudioMap["id"] = b.ID
				}

				if len(inputAudioMap) > 0 {
					audioContent.InputAudio = inputAudioMap
					content = append(content, audioContent)
				}
			case FileBlock:
				// Use proper Anthropic file block format
				content = append(content, AnthropicContent{
					Type: "file",
					File: map[string]interface{}{
						"file_id": b.FileID,
					},
				})
			case ToolUseBlock:
				content = append(content, AnthropicContent{
					Type:  "tool_use",
					ID:    b.ID,
					Name:  b.Name,
					Input: b.Input,
				})
			case ToolResultBlock:
				content = append(content, AnthropicContent{
					Type:      "tool_result",
					ToolUseID: b.ToolUseID,
					Content:   b.Content,
					IsError:   b.IsError,
				})
			}
		}

		if len(content) > 0 {
			anthMsg.Content = content
		}
		result = append(result, anthMsg)
	}

	return result
}

// ToOpenAITyped converts TypedMessage to strongly typed OpenAI format
func ToOpenAITyped(messages []TypedMessage) []OpenAIMessage {
	result := make([]OpenAIMessage, 0, len(messages))

	for _, msg := range messages {
		openAIMsg := OpenAIMessage{
			Role: msg.Role,
		}

		// Handle different roles
		switch msg.Role {
		case string(RoleAssistant):
			content, toolCalls := ConvertBlocksToOpenAITyped(msg.Blocks)
			openAIMsg.Content = content
			openAIMsg.ToolCalls = toolCalls

		case string(RoleUser):
			// Handle tool results specially
			var hasToolResults bool
			for _, block := range msg.Blocks {
				if _, ok := block.(ToolResultBlock); ok {
					hasToolResults = true
					break
				}
			}

			if hasToolResults {
				// Create separate messages for tool results
				for _, block := range msg.Blocks {
					switch b := block.(type) {
					case ToolResultBlock:
						toolMsg := OpenAIMessage{
							Role:       "tool",
							ToolCallID: b.ToolUseID,
							Content:    b.Content,
						}
						result = append(result, toolMsg)
					case TextBlock:
						// Add any text blocks as a user message
						userMsg := OpenAIMessage{
							Role:    "user",
							Content: b.Text,
						}
						result = append(result, userMsg)
					}
				}
				// Skip the regular append since we've handled it
				continue
			} else {
				// Regular user message
				content, _ := ConvertBlocksToOpenAITyped(msg.Blocks)
				if content == nil {
					openAIMsg.Content = ""
				} else {
					openAIMsg.Content = content
				}
			}

		default:
			// System and other roles - just extract text
			for _, block := range msg.Blocks {
				if textBlock, ok := block.(TextBlock); ok {
					openAIMsg.Content = textBlock.Text
					break
				}
			}
		}

		result = append(result, openAIMsg)
	}

	return result
}

// ConvertBlocksToOpenAITyped converts blocks to OpenAI content and tool calls using typed structures
// This function delegates to the single source of truth (ConvertBlocksToOpenAIContent) to avoid duplication
func ConvertBlocksToOpenAITyped(blocks []Block) (content interface{}, toolCalls []OpenAIToolCall) {
	// Use the single source of truth for OpenAI content conversion
	return ConvertBlocksToOpenAIContent(blocks)
}

// ToGoogleTyped converts TypedMessage to strongly typed Google format
func ToGoogleTyped(messages []TypedMessage) []GoogleMessage {
	return toGoogleTypedInternal(messages, false)
}

// ToGoogleForArgoTyped converts TypedMessage to Google format for Argo (keeps system messages)
func ToGoogleForArgoTyped(messages []TypedMessage) []GoogleMessage {
	return toGoogleTypedInternal(messages, true)
}

// toGoogleTypedInternal converts TypedMessage to strongly typed Google format
// This is a pure function with no side effects (no logging)
func toGoogleTypedInternal(messages []TypedMessage, keepSystem bool) []GoogleMessage {
	result := make([]GoogleMessage, 0, len(messages))

	for _, msg := range messages {
		// Google uses "model" for assistant, "user" for user
		role := msg.Role
		if role == "assistant" {
			role = "model"
		}

		// Skip system messages for Google (handle separately) unless keepSystem is true
		if msg.Role == string(RoleSystem) && !keepSystem {
			continue
		}

		googleMsg := GoogleMessage{
			Role: role,
		}

		// Convert blocks to parts
		parts := make([]GooglePart, 0, len(msg.Blocks))
		for _, block := range msg.Blocks {
			switch b := block.(type) {
			case TextBlock:
				parts = append(parts, GooglePart{
					Text: b.Text,
				})
			case ImageBlock:
				// Google supports images via inline data or URL
				// For now, we'll convert URL to text representation
				parts = append(parts, GooglePart{
					Text: "[Image: " + b.URL + "]",
				})
			case AudioBlock:
				// Convert audio to text representation for Google
				audioText := "[Audio content"
				if b.ID != "" {
					audioText += ": " + b.ID
				} else if b.Data != "" {
					audioText += " (data)"
				}
				audioText += "]"
				parts = append(parts, GooglePart{
					Text: audioText,
				})
			case FileBlock:
				// Convert file to text representation for Google
				parts = append(parts, GooglePart{
					Text: "[File content: " + b.FileID + "]",
				})
			case ToolUseBlock:
				parts = append(parts, GooglePart{
					FunctionCall: &GoogleFunctionCall{
						Name: b.Name,
						Args: b.Input,
					},
				})
			case ToolResultBlock:
				functionName := b.Name
				if functionName == "" {
					// TODO: Implement proper mapping from tool_use_id to function name
					functionName = b.ToolUseID
				}
				parts = append(parts, GooglePart{
					FunctionResponse: &GoogleFunctionResponse{
						Name: functionName,
						Response: GoogleResponseContent{
							Content: b.Content,
							Error:   b.IsError,
						},
					},
				})
			}
		}

		if len(parts) > 0 {
			googleMsg.Parts = parts
			result = append(result, googleMsg)
		}
	}

	return result
}

// ConvertToolsToOpenAITyped converts tool definitions to strongly typed OpenAI format
func ConvertToolsToOpenAITyped(tools []ToolDefinition) []OpenAITool {
	openAITools := make([]OpenAITool, 0, len(tools))
	for _, tool := range tools {
		// Handle nil or invalid InputSchema
		var parameters map[string]interface{}
		if tool.InputSchema != nil {
			// Safe type assertion with fallback
			if params, ok := tool.InputSchema.(map[string]interface{}); ok {
				parameters = params
			} else {
				// Provide default schema if type assertion fails
				parameters = map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				}
			}
		} else {
			// Provide default schema for nil InputSchema
			parameters = map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}
		}

		openAITools = append(openAITools, OpenAITool{
			Type: "function",
			Function: OpenAIToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  parameters,
			},
		})
	}
	return openAITools
}

// ConvertToolsToAnthropicTyped converts tool definitions to strongly typed Anthropic format
func ConvertToolsToAnthropicTyped(tools []ToolDefinition) []AnthropicTool {
	anthropicTools := make([]AnthropicTool, 0, len(tools))
	for _, tool := range tools {
		// Handle nil or invalid InputSchema
		var inputSchema map[string]interface{}
		if tool.InputSchema != nil {
			// Safe type assertion with fallback
			if schema, ok := tool.InputSchema.(map[string]interface{}); ok {
				inputSchema = schema
			} else {
				// Provide default schema if type assertion fails
				inputSchema = map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				}
			}
		} else {
			// Provide default schema for nil InputSchema
			inputSchema = map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}
		}

		anthropicTools = append(anthropicTools, AnthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: inputSchema,
		})
	}
	return anthropicTools
}

// ConvertToolsToGoogleTyped converts tool definitions to strongly typed Google format
func ConvertToolsToGoogleTyped(tools []ToolDefinition) []GoogleTool {
	if len(tools) == 0 {
		return []GoogleTool{}
	}

	// Google uses a single tool object with multiple function declarations
	declarations := make([]GoogleFunctionDeclaration, 0, len(tools))
	for _, tool := range tools {
		// Handle nil or invalid InputSchema
		var params map[string]interface{}
		if tool.InputSchema != nil {
			// Convert schema to Google format
			converted := ConvertSchemaToGoogleFormat(tool.InputSchema)
			// Safe type assertion with fallback
			if p, ok := converted.(map[string]interface{}); ok {
				params = p
			} else {
				// Provide default schema if type assertion fails
				params = map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				}
			}
		} else {
			// Provide default schema for nil InputSchema
			params = map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}
		}

		declarations = append(declarations, GoogleFunctionDeclaration{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  params,
		})
	}

	return []GoogleTool{
		{
			FunctionDeclarations: declarations,
		},
	}
}

// FromOpenAITyped converts strongly typed OpenAI messages to TypedMessage
func FromOpenAITyped(messages []OpenAIMessage) []TypedMessage {
	result := make([]TypedMessage, 0, len(messages))

	for _, msg := range messages {
		typed := TypedMessage{Role: msg.Role}

		switch msg.Role {
		case "tool":
			// Tool result message in OpenAI format
			typed.Role = "user"
			typed.Blocks = []Block{
				ToolResultBlock{
					ToolUseID: msg.ToolCallID,
					Content:   msg.Content.(string),
					IsError:   false,
				},
			}

		default:
			// System, user, or assistant message
			// Handle content (can be string or array)
			switch content := msg.Content.(type) {
			case string:
				if content != "" {
					typed.Blocks = append(typed.Blocks, TextBlock{Text: content})
				}
			case []OpenAIContent:
				// Handle multimodal content
				for _, item := range content {
					switch item.Type {
					case "text":
						if item.Text != "" {
							typed.Blocks = append(typed.Blocks, TextBlock{Text: item.Text})
						}
					case "image_url":
						if item.ImageURL != nil {
							typed.Blocks = append(typed.Blocks, ImageBlock{
								URL:    item.ImageURL.URL,
								Detail: item.ImageURL.Detail,
							})
						}
					case "input_audio":
						if item.InputAudio != nil {
							audioBlock := AudioBlock{}
							if id, ok := item.InputAudio["id"].(string); ok {
								audioBlock.ID = id
							}
							if data, ok := item.InputAudio["data"].(string); ok {
								audioBlock.Data = data
							}
							if format, ok := item.InputAudio["format"].(string); ok {
								audioBlock.Format = format
							}
							if audioBlock.ID != "" || audioBlock.Data != "" {
								typed.Blocks = append(typed.Blocks, audioBlock)
							}
						}
					case "file":
						if item.File != nil {
							if fileID, ok := item.File["file_id"].(string); ok && fileID != "" {
								typed.Blocks = append(typed.Blocks, FileBlock{FileID: fileID})
							}
						}
					}
				}
			}

			// Handle tool_calls for assistant messages
			for _, tc := range msg.ToolCalls {
				typed.Blocks = append(typed.Blocks, ToolUseBlock{
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: json.RawMessage(tc.Function.Arguments),
				})
			}
		}

		result = append(result, typed)
	}

	return result
}

// MarshalAnthropicMessagesForRequest converts typed Anthropic messages to []interface{} for request bodies
// This centralizes the conversion logic and reduces duplication across the codebase
func MarshalAnthropicMessagesForRequest(messages []AnthropicMessage) []interface{} {
	result := make([]interface{}, 0, len(messages))
	for _, msg := range messages {
		msgMap := map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		}
		result = append(result, msgMap)
	}
	return result
}

// MarshalOpenAIMessagesForRequest converts typed OpenAI messages to []interface{} for request bodies
// This centralizes the conversion logic and reduces duplication across the codebase
func MarshalOpenAIMessagesForRequest(messages []OpenAIMessage) []interface{} {
	result := make([]interface{}, 0, len(messages))
	for _, msg := range messages {
		msgMap := map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		}
		if len(msg.ToolCalls) > 0 {
			msgMap["tool_calls"] = msg.ToolCalls
		}
		if msg.ToolCallID != "" {
			msgMap["tool_call_id"] = msg.ToolCallID
		}
		result = append(result, msgMap)
	}
	return result
}

// MarshalGoogleMessagesForRequest converts typed Google messages to []interface{} for request bodies
// This centralizes the conversion logic and reduces duplication across the codebase
func MarshalGoogleMessagesForRequest(messages []GoogleMessage) []interface{} {
	result := make([]interface{}, 0, len(messages))
	for _, msg := range messages {
		msgMap := map[string]interface{}{
			"role":  msg.Role,
			"parts": msg.Parts,
		}
		result = append(result, msgMap)
	}
	return result
}

// FromAnthropicTyped converts strongly typed Anthropic messages to TypedMessage
func FromAnthropicTyped(messages []AnthropicMessage) []TypedMessage {
	result := make([]TypedMessage, 0, len(messages))

	for _, msg := range messages {
		typed := TypedMessage{Role: msg.Role}

		// Handle content - can be string or array of blocks
		switch content := msg.Content.(type) {
		case string:
			typed.Blocks = []Block{TextBlock{Text: content}}
		case []AnthropicContent:
			for _, block := range content {
				switch block.Type {
				case "text":
					typed.Blocks = append(typed.Blocks, TextBlock{Text: block.Text})
				case "tool_use":
					typed.Blocks = append(typed.Blocks, ToolUseBlock{
						ID:    block.ID,
						Name:  block.Name,
						Input: block.Input,
					})
				case "tool_result":
					typed.Blocks = append(typed.Blocks, ToolResultBlock{
						ToolUseID: block.ToolUseID,
						Content:   block.Content,
						IsError:   block.IsError,
					})
				case "image":
					if block.Source != nil {
						typed.Blocks = append(typed.Blocks, ImageBlock{
							URL:    block.Source.URL,
							Detail: "auto",
						})
					}
				case "input_audio":
					if block.InputAudio != nil {
						audioBlock := AudioBlock{}
						if id, ok := block.InputAudio["id"].(string); ok {
							audioBlock.ID = id
						}
						if data, ok := block.InputAudio["data"].(string); ok {
							audioBlock.Data = data
						}
						if format, ok := block.InputAudio["format"].(string); ok {
							audioBlock.Format = format
						}
						if audioBlock.ID != "" || audioBlock.Data != "" {
							typed.Blocks = append(typed.Blocks, audioBlock)
						}
					}
				case "file":
					if block.File != nil {
						if fileID, ok := block.File["file_id"].(string); ok && fileID != "" {
							typed.Blocks = append(typed.Blocks, FileBlock{FileID: fileID})
						}
					}
				}
			}
		}

		result = append(result, typed)
	}

	return result
}
