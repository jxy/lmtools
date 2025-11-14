// Package core provides type-safe conversions between different LLM provider formats.
//
// ARCHITECTURAL OVERVIEW:
// This package implements a strongly-typed message system that replaces generic
// map[string]interface{} usage throughout the codebase. All provider-specific
// formats are converted to/from a canonical TypedMessage representation.
//
// UNION TYPE PATTERN:
// The package defines content union types (OpenAIContentUnion, AnthropicContentUnion)
// that handle the polymorphic nature of LLM API content fields, which can be either
// a simple string or an array of content objects.
//
// KEY DESIGN DECISIONS:
//
// 1. Union types CANNOT be marshaled directly:
//   - They implement UnmarshalJSON for parsing provider responses
//   - They deliberately fail MarshalJSON to prevent accidental misuse
//   - This compile-time safety prevents runtime errors
//
// 2. Request building uses dedicated marshal functions:
//   - MarshalAnthropicMessagesForRequest: Builds Anthropic API format
//   - MarshalOpenAIMessagesForRequest: Builds OpenAI API format
//   - MarshalGoogleMessagesForRequest: Builds Google API format
//   - These functions handle union types correctly via ToMap() methods
//
// 3. Type safety throughout:
//   - No map[string]interface{} in business logic
//   - Compile-time type checking for all conversions
//   - Clear error messages when misused
//
// USAGE EXAMPLES:
//
// Correct usage for request building:
//
//	messages := []TypedMessage{...}
//	openAIMessages := ToOpenAITyped(messages)
//	requestBody := MarshalOpenAIMessagesForRequest(openAIMessages)
//	jsonBytes, _ := json.Marshal(requestBody) // Safe!
//
// Incorrect usage (will fail at runtime):
//
//	messages := []OpenAIMessage{...} // Has OpenAIContentUnion inside
//	jsonBytes, _ := json.Marshal(messages) // RUNTIME ERROR!
//
// BEST PRACTICES:
// - Always use TypedMessage for internal processing
// - Convert to provider format only at API boundaries
// - Use Marshal*ForRequest functions for building requests
// - Never attempt to marshal union types directly
// - Validate union types with ValidateForMarshal() when needed
package core

import (
	"encoding/json"
	"log"
	"strings"
)

// ARCHITECTURAL NOTE: These conversion functions use strongly typed structures
// instead of map[string]interface{}. This ensures type safety and makes the
// code more maintainable. All conversions go through TypedMessage as the
// canonical internal representation.
//
// UNION TYPE HANDLING:
// This package defines content union types (OpenAIContentUnion, AnthropicContentUnion)
// that represent the different ways content can be structured in API requests.
// These union types intentionally fail MarshalJSON to prevent accidental direct
// marshaling. They are internal representations that must be converted to the
// appropriate format using the Marshal*ForRequest functions.
//
// IMPORTANT: Never marshal union types directly. Always use:
//   - MarshalAnthropicMessagesForRequest for Anthropic API format
//   - MarshalOpenAIMessagesForRequest for OpenAI API format
//   - MarshalGoogleMessagesForRequest for Google API format
//
// These marshal functions handle the union types correctly and produce
// the exact JSON structure expected by each provider's API.

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
				text := textBlock.Text
				anthMsg.Content = AnthropicContentUnion{
					Text:     &text,
					Contents: nil,
				}
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
				// Properly map audio fields
				audioData := &AudioData{
					ID:       b.ID,
					Data:     b.Data,
					Format:   b.Format,
					URL:      b.URL,
					Duration: b.Duration,
				}
				// Default format if not specified
				ensureAudioFormat(audioData)
				audioContent.InputAudio = audioData
				content = append(content, audioContent)
			case FileBlock:
				// Use proper Anthropic file block format with FileID
				content = append(content, AnthropicContent{
					Type: "file",
					File: &FileData{
						FileID: b.FileID,
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
			anthMsg.Content = AnthropicContentUnion{
				Text:     nil,
				Contents: content,
			}
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
			content, toolCalls := ConvertBlocksToOpenAIContentTyped(msg.Blocks)
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
						content := b.Content
						toolMsg := OpenAIMessage{
							Role:       "tool",
							ToolCallID: b.ToolUseID,
							Content: OpenAIContentUnion{
								Text:     &content,
								Contents: nil,
							},
						}
						result = append(result, toolMsg)
					case TextBlock:
						// Add any text blocks as a user message
						text := b.Text
						userMsg := OpenAIMessage{
							Role: "user",
							Content: OpenAIContentUnion{
								Text:     &text,
								Contents: nil,
							},
						}
						result = append(result, userMsg)
					}
				}
				// Skip the regular append since we've handled it
				continue
			} else {
				// Regular user message
				content, _ := ConvertBlocksToOpenAIContentTyped(msg.Blocks)
				openAIMsg.Content = content
			}

		default:
			// System and other roles - just extract text
			for _, block := range msg.Blocks {
				if textBlock, ok := block.(TextBlock); ok {
					text := textBlock.Text
					openAIMsg.Content = OpenAIContentUnion{
						Text:     &text,
						Contents: nil,
					}
					break
				}
			}
		}

		result = append(result, openAIMsg)
	}

	return result
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
		// InputSchema can be map[string]interface{} or json.RawMessage
		var jsonParams json.RawMessage

		if tool.InputSchema != nil {
			switch schema := tool.InputSchema.(type) {
			case map[string]interface{}:
				// Most common case - convert map to JSON
				if data, err := json.Marshal(schema); err == nil {
					jsonParams = json.RawMessage(data)
				}
			case json.RawMessage:
				// Already in JSON format (used in tests)
				jsonParams = schema
			case []byte:
				// Raw bytes (convert to RawMessage)
				jsonParams = json.RawMessage(schema)
			default:
				// Fallback - provide default schema
				defaultSchema := map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				}
				if data, err := json.Marshal(defaultSchema); err == nil {
					jsonParams = json.RawMessage(data)
				}
			}
		} else {
			// Provide default schema for nil InputSchema
			defaultSchema := map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}
			if data, err := json.Marshal(defaultSchema); err == nil {
				jsonParams = json.RawMessage(data)
			}
		}

		openAITools = append(openAITools, OpenAITool{
			Type: "function",
			Function: OpenAIToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  jsonParams,
			},
		})
	}
	return openAITools
}

// ConvertToolsToAnthropicTyped converts tool definitions to strongly typed Anthropic format
func ConvertToolsToAnthropicTyped(tools []ToolDefinition) []AnthropicTool {
	anthropicTools := make([]AnthropicTool, 0, len(tools))
	for _, tool := range tools {
		// InputSchema can be map[string]interface{} or json.RawMessage
		var jsonSchema json.RawMessage

		if tool.InputSchema != nil {
			switch schema := tool.InputSchema.(type) {
			case map[string]interface{}:
				// Most common case - convert map to JSON
				if data, err := json.Marshal(schema); err == nil {
					jsonSchema = json.RawMessage(data)
				}
			case json.RawMessage:
				// Already in JSON format (used in tests)
				jsonSchema = schema
			case []byte:
				// Raw bytes (convert to RawMessage)
				jsonSchema = json.RawMessage(schema)
			default:
				// Fallback - provide default schema
				defaultSchema := map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				}
				if data, err := json.Marshal(defaultSchema); err == nil {
					jsonSchema = json.RawMessage(data)
				}
			}
		} else {
			// Provide default schema for nil InputSchema
			defaultSchema := map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}
			if data, err := json.Marshal(defaultSchema); err == nil {
				jsonSchema = json.RawMessage(data)
			}
		}

		anthropicTools = append(anthropicTools, AnthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: jsonSchema,
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
		// InputSchema can be map[string]interface{} or json.RawMessage
		var jsonParams json.RawMessage

		if tool.InputSchema != nil {
			// First convert to Google format if needed
			converted := ConvertSchemaToGoogleFormat(tool.InputSchema)

			switch schema := converted.(type) {
			case map[string]interface{}:
				// Most common case - convert map to JSON
				if data, err := json.Marshal(schema); err == nil {
					jsonParams = json.RawMessage(data)
				}
			case json.RawMessage:
				// Already in JSON format
				jsonParams = schema
			case []byte:
				// Raw bytes (convert to RawMessage)
				jsonParams = json.RawMessage(schema)
			default:
				// Fallback - provide default schema
				defaultSchema := map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				}
				if data, err := json.Marshal(defaultSchema); err == nil {
					jsonParams = json.RawMessage(data)
				}
			}
		} else {
			// Provide default schema for nil InputSchema
			defaultSchema := map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}
			if data, err := json.Marshal(defaultSchema); err == nil {
				jsonParams = json.RawMessage(data)
			}
		}

		declarations = append(declarations, GoogleFunctionDeclaration{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  jsonParams,
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
			// Extract content from ContentUnion
			var content string
			if msg.Content.Text != nil {
				content = *msg.Content.Text
			}
			typed.Blocks = []Block{
				ToolResultBlock{
					ToolUseID: msg.ToolCallID,
					Content:   content,
					IsError:   false,
				},
			}

		default:
			// System, user, or assistant message
			// Handle content from ContentUnion
			if msg.Content.Text != nil && *msg.Content.Text != "" {
				// Simple text content
				typed.Blocks = append(typed.Blocks, TextBlock{Text: *msg.Content.Text})
			} else if len(msg.Content.Contents) > 0 {
				// Array of content blocks
				for _, item := range msg.Content.Contents {
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
							audioBlock := AudioBlock{
								ID:       item.InputAudio.ID,
								Data:     item.InputAudio.Data,
								Format:   item.InputAudio.Format,
								URL:      item.InputAudio.URL,
								Duration: item.InputAudio.Duration,
							}
							// Only add if we have some audio content
							if audioBlock.ID != "" || audioBlock.Data != "" || audioBlock.URL != "" {
								typed.Blocks = append(typed.Blocks, audioBlock)
							}
						}
					case "file":
						if item.File != nil && item.File.FileID != "" {
							typed.Blocks = append(typed.Blocks, FileBlock{FileID: item.File.FileID})
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
		// Validate the content union before marshaling
		if err := msg.Content.ValidateForMarshal(); err != nil {
			log.Printf("Warning: Invalid AnthropicContentUnion in message: %v", err)
			// Continue processing despite validation error
		}

		msgMap := map[string]interface{}{
			"role": msg.Role,
		}

		// Extract the actual content value from ContentUnion
		if len(msg.Content.Contents) > 0 {
			contentArray := make([]interface{}, len(msg.Content.Contents))
			for i, c := range msg.Content.Contents {
				contentArray[i] = c.ToMap()
			}
			msgMap["content"] = contentArray
		} else if msg.Content.Text != nil && *msg.Content.Text != "" {
			// Only include content if non-empty string
			msgMap["content"] = *msg.Content.Text
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
		// Validate the content union before marshaling
		if err := msg.Content.ValidateForMarshal(); err != nil {
			log.Printf("Warning: Invalid OpenAIContentUnion in message: %v", err)
			// Continue processing despite validation error
		}

		msgMap := map[string]interface{}{
			"role": msg.Role,
		}

		// Extract the actual content value from ContentUnion
		if len(msg.Content.Contents) > 0 {
			// Convert []OpenAIContent to []interface{} via ToMap
			contentArray := make([]interface{}, len(msg.Content.Contents))
			for i, c := range msg.Content.Contents {
				contentArray[i] = c.ToMap()
			}
			msgMap["content"] = contentArray
		} else if msg.Content.Text != nil && *msg.Content.Text != "" {
			// Only include content if non-empty string
			msgMap["content"] = *msg.Content.Text
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
			"role": msg.Role,
		}

		// Convert parts to []map[string]interface{} using ToMap() method
		if len(msg.Parts) > 0 {
			partMaps := make([]map[string]interface{}, 0, len(msg.Parts))
			for _, part := range msg.Parts {
				partMaps = append(partMaps, part.ToMap())
			}
			msgMap["parts"] = partMaps
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

		// Handle content from ContentUnion
		if msg.Content.Text != nil && *msg.Content.Text != "" {
			// Simple text content
			typed.Blocks = []Block{TextBlock{Text: *msg.Content.Text}}
		} else if len(msg.Content.Contents) > 0 {
			// Array of content blocks
			for _, block := range msg.Content.Contents {
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
							Detail: "auto", // Don't conflate MediaType with Detail
						})
					}
				case "input_audio":
					if block.InputAudio != nil {
						typed.Blocks = append(typed.Blocks, AudioBlock{
							ID:       block.InputAudio.ID,
							Data:     block.InputAudio.Data,
							Format:   block.InputAudio.Format,
							URL:      block.InputAudio.URL,
							Duration: block.InputAudio.Duration,
						})
					}
				case "file":
					if block.File != nil && block.File.FileID != "" {
						typed.Blocks = append(typed.Blocks, FileBlock{
							FileID: block.File.FileID,
						})
					}
				}
			}
		}

		result = append(result, typed)
	}

	return result
}

// ConvertBlocksToOpenAIContentTyped converts blocks to strongly typed OpenAI content and tool calls.
// It builds OpenAIContentUnion directly without going through map[string]interface{} intermediates.
func ConvertBlocksToOpenAIContentTyped(blocks []Block) (OpenAIContentUnion, []OpenAIToolCall) {
	var union OpenAIContentUnion
	var toolCalls []OpenAIToolCall
	var parts []OpenAIContent
	var textBuilder strings.Builder
	hasNonText := false

	for _, block := range blocks {
		switch v := block.(type) {
		case TextBlock:
			// Accumulate text for potential string-only content
			textBuilder.WriteString(v.Text)
			// Also add to content parts for multimodal case
			parts = append(parts, OpenAIContent{
				Type: "text",
				Text: v.Text,
			})

		case ImageBlock:
			hasNonText = true
			imagePart := OpenAIContent{
				Type: "image_url",
				ImageURL: &OpenAIImageURL{
					URL: v.URL,
				},
			}
			// Add detail if specified
			if v.Detail != "" {
				imagePart.ImageURL.Detail = v.Detail
			}
			parts = append(parts, imagePart)

		case AudioBlock:
			hasNonText = true
			audioPart := OpenAIContent{
				Type: "input_audio",
			}
			inputAudio := &AudioData{}

			// Include data and format if available
			if v.Data != "" {
				inputAudio.Data = v.Data
				if v.Format != "" {
					inputAudio.Format = v.Format
				} else {
					inputAudio.Format = "wav" // Default format
				}
			} else if v.ID != "" {
				// Otherwise use ID if available
				inputAudio.ID = v.ID
			}

			// Only add audio part if we have data
			if inputAudio.Data != "" || inputAudio.ID != "" {
				audioPart.InputAudio = inputAudio
				parts = append(parts, audioPart)
			}

		case FileBlock:
			hasNonText = true
			parts = append(parts, OpenAIContent{
				Type: "file",
				File: &FileData{
					FileID: v.FileID,
				},
			})

		case ToolUseBlock:
			// Convert to OpenAI tool call format
			toolCall := OpenAIToolCall{
				ID:   v.ID,
				Type: "function",
				Function: OpenAIFunctionCall{
					Name: v.Name,
				},
			}
			// Add arguments if present
			if len(v.Input) > 0 {
				toolCall.Function.Arguments = string(v.Input)
			}
			toolCalls = append(toolCalls, toolCall)

		case ToolResultBlock:
			// Tool results are handled separately in ToOpenAI
			// This function only handles content that goes into the message content field
			// Tool results become separate "tool" role messages in OpenAI format
			continue
		}
	}

	// Determine the appropriate content format
	if hasNonText {
		// Multimodal content always requires array format
		union.Contents = parts
		return union, toolCalls
	}

	if len(toolCalls) > 0 {
		// When there are tool calls with text-only content, return the text
		if textBuilder.Len() > 0 {
			text := textBuilder.String()
			union.Text = &text
			return union, toolCalls
		}
		// Tool calls only, no content
		return union, toolCalls
	}

	// Text-only content can be a simple string
	if textBuilder.Len() > 0 {
		text := textBuilder.String()
		union.Text = &text
		return union, toolCalls
	}

	// No content
	return union, toolCalls
}
