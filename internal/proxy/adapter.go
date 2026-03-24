package proxy

import (
	"encoding/json"
	"lmtools/internal/core"
	"time"
)

// ARCHITECTURAL PRINCIPLE: TypedRequest is our canonical internal representation.
// All provider-specific formats (OpenAI, Anthropic, Google, Argo) are converted
// to/from TypedRequest at API boundaries. This ensures:
// 1. Single source of truth for message handling and business logic
// 2. Provider-specific details are isolated to edge converters
// 3. Core logic remains provider-agnostic
//
// Conversion flow:
//   Incoming: ProviderFormat -> TypedRequest -> Internal Processing
//   Outgoing: Internal Processing -> TypedRequest -> ProviderFormat
//
// NEVER convert directly between provider formats. Always go through TypedRequest.
// This ensures consistency and maintainability.

// TypedRequest represents a provider-agnostic request structure
type TypedRequest struct {
	System          string
	Messages        []core.TypedMessage
	Tools           []core.ToolDefinition
	ToolChoice      *core.ToolChoice
	MaxTokens       *int
	Temperature     *float64
	TopP            *float64
	Stop            []string
	Stream          bool
	ReasoningEffort string // for OpenAI o1 models
}

// OpenAIRequestToTyped converts an OpenAI request to TypedRequest
func OpenAIRequestToTyped(req *OpenAIRequest) TypedRequest {
	typed := TypedRequest{
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		Stop:            req.Stop,
		Stream:          req.Stream,
		ReasoningEffort: req.ReasoningEffort,
	}

	// Convert OpenAI messages to typed OpenAI messages first
	openAITypedMessages := make([]core.OpenAIMessage, len(req.Messages))
	for i, msg := range req.Messages {
		openAITypedMessages[i] = core.OpenAIMessage{
			Role:       string(msg.Role),
			ToolCallID: msg.ToolCallID,
		}

		// Handle content - need to convert []interface{} to []core.OpenAIContent if needed
		switch content := msg.Content.(type) {
		case string:
			openAITypedMessages[i].Content = core.OpenAIContentUnion{
				Text:     &content,
				Contents: nil,
			}
		case []interface{}:
			// Convert to []core.OpenAIContent
			openAIContent := make([]core.OpenAIContent, 0, len(content))
			for _, item := range content {
				if contentMap, ok := item.(map[string]interface{}); ok {
					contentType := ""
					if t, ok := contentMap["type"].(string); ok {
						contentType = t
					}

					switch contentType {
					case "text":
						text := ""
						if t, ok := contentMap["text"].(string); ok {
							text = t
						}
						openAIContent = append(openAIContent, core.OpenAIContent{
							Type: "text",
							Text: text,
						})
					case "image_url":
						if imageURLMap, ok := contentMap["image_url"].(map[string]interface{}); ok {
							url := ""
							detail := "auto"
							if u, ok := imageURLMap["url"].(string); ok {
								url = u
							}
							if d, ok := imageURLMap["detail"].(string); ok {
								detail = d
							}
							openAIContent = append(openAIContent, core.OpenAIContent{
								Type: "image_url",
								ImageURL: &core.OpenAIImageURL{
									URL:    url,
									Detail: detail,
								},
							})
						}
					case "input_audio":
						if audioMap, ok := contentMap["input_audio"].(map[string]interface{}); ok {
							audioData := &core.AudioData{
								ID:       core.GetString(audioMap, "id"),
								Format:   core.GetString(audioMap, "format"),
								Data:     core.GetString(audioMap, "data"),
								URL:      core.GetString(audioMap, "url"),
								Duration: core.GetInt(audioMap, "duration"),
							}
							// Ensure audio format defaults to "wav" if not specified
							if audioData.Format == "" && audioData.Data != "" {
								audioData.Format = "wav"
							}
							openAIContent = append(openAIContent, core.OpenAIContent{
								Type:       "input_audio",
								InputAudio: audioData,
							})
						}
					case "file":
						if fileMap, ok := contentMap["file"].(map[string]interface{}); ok {
							fileData := &core.FileData{
								FileID:   core.GetString(fileMap, "file_id"),
								Name:     core.GetString(fileMap, "name"),
								MimeType: core.GetString(fileMap, "mime_type"),
								Data:     core.GetString(fileMap, "data"),
								URL:      core.GetString(fileMap, "url"),
								Size:     core.GetInt64(fileMap, "size"),
							}
							openAIContent = append(openAIContent, core.OpenAIContent{
								Type: "file",
								File: fileData,
							})
						}
					}
				}
			}
			openAITypedMessages[i].Content = core.OpenAIContentUnion{
				Text:     nil,
				Contents: openAIContent,
			}
		default:
			// For any other type, omit content entirely
			openAITypedMessages[i].Content = core.OpenAIContentUnion{
				Text:     nil,
				Contents: nil,
			}
		}

		// Handle tool calls
		if len(msg.ToolCalls) > 0 {
			openAITypedMessages[i].ToolCalls = make([]core.OpenAIToolCall, len(msg.ToolCalls))
			for j, tc := range msg.ToolCalls {
				openAITypedMessages[i].ToolCalls[j] = core.OpenAIToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: core.OpenAIFunctionCall{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				}
			}
		}
	}

	// Convert messages using typed function
	typedMessages := core.FromOpenAITyped(openAITypedMessages)

	// Extract system message if present
	for i, msg := range typedMessages {
		if msg.Role == "system" {
			// Extract system message text
			for _, block := range msg.Blocks {
				if textBlock, ok := block.(core.TextBlock); ok {
					typed.System = textBlock.Text
					break
				}
			}
			// Remove system message from messages array
			typed.Messages = append(typedMessages[:i], typedMessages[i+1:]...)
			break
		}
	}

	// If no system message was extracted, use all messages
	if typed.System == "" && typed.Messages == nil {
		typed.Messages = typedMessages
	}

	// Convert tools
	if len(req.Tools) > 0 {
		typed.Tools = make([]core.ToolDefinition, len(req.Tools))
		for i, tool := range req.Tools {
			typed.Tools[i] = core.ToolDefinition{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				InputSchema: tool.Function.Parameters,
			}
		}
	}

	// Convert tool choice
	if req.ToolChoice != nil {
		switch tc := req.ToolChoice.(type) {
		case string:
			if tc == "auto" || tc == "none" {
				typed.ToolChoice = &core.ToolChoice{
					Type: tc,
				}
			}
		case map[string]interface{}:
			if tc["type"] == "function" {
				if function, ok := tc["function"].(map[string]interface{}); ok {
					if name, ok := function["name"].(string); ok {
						typed.ToolChoice = &core.ToolChoice{
							Type: "tool",
							Name: name,
						}
					}
				}
			}
		}
	}

	return typed
}

// AnthropicRequestToTyped converts an Anthropic request to TypedRequest
func AnthropicRequestToTyped(req *AnthropicRequest) TypedRequest {
	typed := TypedRequest{
		MaxTokens:   &req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.StopSequences,
		Stream:      req.Stream,
	}

	// Handle system message
	if req.System != nil {
		var systemContent string
		// Try to unmarshal as string
		if err := json.Unmarshal(req.System, &systemContent); err == nil {
			typed.System = systemContent
		} else {
			// If not a string, use raw JSON
			typed.System = string(req.System)
		}
	}

	// Convert Anthropic messages to typed Anthropic messages first
	typed.Messages = make([]core.TypedMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		typedMsg := core.TypedMessage{
			Role: string(msg.Role),
		}

		if len(msg.Content) > 0 {
			text, blocks, err := parseAnthropicMessageContent(msg.Content)
			if err == nil {
				if text != nil && *text != "" {
					typedMsg.Blocks = []core.Block{core.TextBlock{Text: *text}}
				} else if len(blocks) > 0 {
					typedMsg.Blocks = AnthropicBlocksToCore(blocks)
				}
			}
		}
		typed.Messages = append(typed.Messages, typedMsg)
	}

	// Convert tools
	if len(req.Tools) > 0 {
		typed.Tools = make([]core.ToolDefinition, len(req.Tools))
		for i, tool := range req.Tools {
			typed.Tools[i] = core.ToolDefinition{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: tool.InputSchema,
			}
		}
	}

	// Convert tool choice
	if req.ToolChoice != nil {
		typed.ToolChoice = &core.ToolChoice{
			Type: req.ToolChoice.Type,
			Name: req.ToolChoice.Name,
		}
	}

	return typed
}

// TypedToOpenAIResponse converts a typed response to OpenAI format
func TypedToOpenAIResponse(typed TypedRequest, content string, toolCalls []core.ToolCall, usage *OpenAIUsage, model string, finishReason string) *OpenAIResponse {
	response := &OpenAIResponse{
		ID:      generateUUID("chatcmpl-"),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Usage:   usage,
	}

	// Build the message
	message := OpenAIMessage{
		Role:    core.RoleAssistant,
		Content: content,
	}

	// Convert tool calls if present
	if len(toolCalls) > 0 {
		message.ToolCalls = make([]ToolCall, len(toolCalls))
		for i, tc := range toolCalls {
			message.ToolCalls[i] = ToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: FunctionCall{
					Name:      tc.Name,
					Arguments: string(tc.Args),
				},
			}
		}
	}

	// Add single choice
	response.Choices = []OpenAIChoice{
		{
			Index:        0,
			Message:      message,
			FinishReason: finishReason,
		},
	}

	return response
}

// TypedToAnthropicResponse converts a typed response to Anthropic format
func TypedToAnthropicResponse(typed TypedRequest, content []AnthropicContentBlock, usage *AnthropicUsage, model string, stopReason string) *AnthropicResponse {
	return &AnthropicResponse{
		ID:         generateUUID("msg_"),
		Type:       "message",
		Role:       core.RoleAssistant,
		Content:    content,
		Model:      model,
		StopReason: stopReason,
		Usage:      usage,
	}
}
