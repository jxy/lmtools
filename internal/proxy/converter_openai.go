package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"strings"
	"time"
)

// ConvertAnthropicToOpenAI converts an Anthropic request to OpenAI format
// ARCHITECTURAL NOTE: This conversion goes through TypedRequest as the
// canonical intermediate representation to ensure consistency.
// Flow: Anthropic -> TypedRequest -> OpenAI
func (c *Converter) ConvertAnthropicToOpenAI(ctx context.Context, req *AnthropicRequest) (*OpenAIRequest, error) {
	// Log omitted fields at DEBUG level
	if req.TopK != nil {
		logger.From(ctx).Debugf("Omitting top_k=%d from Anthropic request (not supported by OpenAI)", *req.TopK)
	}
	if len(req.Metadata) > 0 {
		logger.DebugJSON(logger.From(ctx), "Omitting metadata from Anthropic request (not supported by OpenAI)", req.Metadata)
	}

	openAIReq := &OpenAIRequest{
		Model:       req.Model,
		MaxTokens:   &req.MaxTokens, // Pass through max_tokens as-is
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stream:      req.Stream,
		Stop:        req.StopSequences,
	}

	// Handle thinking field for OpenAI models
	if req.Thinking != nil && req.Thinking.Type == "enabled" {
		modelLower := strings.ToLower(req.Model)
		if strings.HasPrefix(modelLower, "gpt") || strings.HasPrefix(modelLower, "o3") || strings.HasPrefix(modelLower, "o4") {
			// For GPT and O3/O4 models, convert to reasoning_effort
			openAIReq.ReasoningEffort = "high"
			logger.From(ctx).Debugf("Converting thinking.budget_tokens=%d to reasoning_effort=high for model %s", req.Thinking.BudgetTokens, req.Model)
		}
	}

	// Convert messages
	messages := []OpenAIMessage{}

	// Add system message if present
	if req.System != nil {
		systemContent, err := extractSystemContent(req.System)
		if err != nil {
			return nil, fmt.Errorf("failed to extract system content: %w", err)
		}
		if systemContent != "" {
			messages = append(messages, OpenAIMessage{
				Role:    core.RoleSystem,
				Content: systemContent,
			})
		}
	}

	// Convert conversation messages
	for _, msg := range req.Messages {
		openAIMsg, err := c.convertAnthropicMessageToOpenAI(ctx, msg)
		if err != nil {
			return nil, fmt.Errorf("failed to convert message: %w", err)
		}
		messages = append(messages, openAIMsg)
	}

	openAIReq.Messages = messages

	// Convert tools
	if len(req.Tools) > 0 {
		tools := []OpenAITool{}
		for _, tool := range req.Tools {
			// Filter out $schema metadata
			parameters := filterSchemaMetadata(tool.InputSchema)
			// Type assert to map[string]interface{} - this should always succeed for valid schemas
			paramsMap, _ := parameters.(map[string]interface{})

			openAITool := OpenAITool{
				Type: "function",
				Function: OpenAIFunc{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  paramsMap,
				},
			}
			tools = append(tools, openAITool)
		}
		openAIReq.Tools = tools
	}

	// Convert tool_choice
	if req.ToolChoice != nil {
		switch req.ToolChoice.Type {
		case "any", "auto":
			openAIReq.ToolChoice = req.ToolChoice.Type
		case "tool":
			if req.ToolChoice.Name != "" {
				openAIReq.ToolChoice = map[string]interface{}{
					"type": "function",
					"function": map[string]string{
						"name": req.ToolChoice.Name,
					},
				}
			}
		}
	}

	return openAIReq, nil
}

// convertAnthropicMessageToOpenAI converts a single Anthropic message to OpenAI format
func (c *Converter) convertAnthropicMessageToOpenAI(ctx context.Context, msg AnthropicMessage) (OpenAIMessage, error) {
	openAIMsg := OpenAIMessage{
		Role: msg.Role,
	}

	// Handle different content types - msg.Content is json.RawMessage
	// Try to parse as string first
	var str string
	if err := json.Unmarshal(msg.Content, &str); err == nil {
		openAIMsg.Content = str
		return openAIMsg, nil
	}

	// Try as content blocks
	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err == nil {
		return c.convertContentBlocksToOpenAI(ctx, string(msg.Role), blocks)
	}

	// Try as array of interfaces
	var items []interface{}
	if err := json.Unmarshal(msg.Content, &items); err == nil {
		// Convert to content blocks
		blocks := []AnthropicContentBlock{}
		for _, item := range items {
			if blockMap, ok := item.(map[string]interface{}); ok {
				block := AnthropicContentBlock{
					Type: blockMap["type"].(string),
				}
				if text, ok := blockMap["text"].(string); ok {
					block.Text = text
				}
				if name, ok := blockMap["name"].(string); ok {
					block.Name = name
				}
				if id, ok := blockMap["id"].(string); ok {
					block.ID = id
				}
				if input, ok := blockMap["input"].(map[string]interface{}); ok {
					block.Input = input
				}
				if source, ok := blockMap["source"].(map[string]interface{}); ok {
					block.Source = source
				}
				if inputAudio, ok := blockMap["input_audio"].(map[string]interface{}); ok {
					block.InputAudio = inputAudio
				}
				if file, ok := blockMap["file"].(map[string]interface{}); ok {
					block.File = file
				}
				if toolUseID, ok := blockMap["tool_use_id"].(string); ok {
					block.ToolUseID = toolUseID
				}
				if content := blockMap["content"]; content != nil {
					if contentBytes, err := json.Marshal(content); err == nil {
						block.Content = json.RawMessage(contentBytes)
					}
				}
				blocks = append(blocks, block)
			}
		}
		return c.convertContentBlocksToOpenAI(ctx, string(msg.Role), blocks)
	}

	// Fall back to string representation
	openAIMsg.Content = string(msg.Content)

	return openAIMsg, nil
}

// convertContentBlocksToOpenAI converts Anthropic content blocks to OpenAI message format
func (c *Converter) convertContentBlocksToOpenAI(ctx context.Context, role string, blocks []AnthropicContentBlock) (OpenAIMessage, error) {
	msg := OpenAIMessage{Role: core.Role(role)}

	// First, handle special cases that don't go through the unified converter
	for _, block := range blocks {
		if block.Type == "tool_result" {
			// Tool result becomes a separate message with "tool" role
			msg.Role = core.RoleTool
			msg.ToolCallID = block.ToolUseID
			// Extract content from json.RawMessage
			var content interface{}
			if err := json.Unmarshal(block.Content, &content); err == nil {
				if contentStr, ok := content.(string); ok {
					msg.Content = contentStr
				} else {
					msg.Content = string(block.Content)
				}
			} else {
				msg.Content = string(block.Content)
			}
			return msg, nil
		}
	}

	// Log thinking blocks at DEBUG level before filtering
	for _, block := range blocks {
		if block.Type == "thinking" {
			droppedBlock := map[string]interface{}{
				"type":     "thinking",
				"thinking": block.Thinking,
			}
			logger.From(ctx).Debugf("Dropping thinking content block (not supported by OpenAI): %+v", droppedBlock)
		}
	}

	// Convert AnthropicContentBlock to core.Block using centralized converter
	coreBlocks := AnthropicBlocksToCore(blocks)

	// Use the unified converter
	content, typedToolCalls := core.ConvertBlocksToOpenAIContent(coreBlocks)

	// Convert typed tool calls to ToolCall structs
	if len(typedToolCalls) > 0 {
		msg.ToolCalls = make([]ToolCall, len(typedToolCalls))
		for i, tc := range typedToolCalls {
			msg.ToolCalls[i] = ToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: FunctionCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			}
		}
		// OpenAI expects null content when tool_calls are present
		// unless it's multimodal content (array format)
		if _, isArray := content.([]interface{}); !isArray {
			msg.Content = nil
		} else {
			msg.Content = content
		}
	} else {
		msg.Content = content
	}
	return msg, nil
}

// ConvertOpenAIToAnthropic converts an OpenAI response to Anthropic format
func (c *Converter) ConvertOpenAIToAnthropic(resp *OpenAIResponse, originalModel string) *AnthropicResponse {
	if len(resp.Choices) == 0 {
		return &AnthropicResponse{
			Type:  "message",
			Model: originalModel,
		}
	}

	choice := resp.Choices[0]
	anthResp := &AnthropicResponse{
		ID:    resp.ID,
		Type:  "message",
		Model: originalModel,
		Role:  core.RoleAssistant,
		Usage: &AnthropicUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		},
	}

	// Convert message content
	content := []AnthropicContentBlock{}

	// Add text content if present
	if choice.Message.Content != nil {
		var text string
		switch v := choice.Message.Content.(type) {
		case string:
			text = v
		default:
			textBytes, _ := json.Marshal(v)
			text = string(textBytes)
		}

		if text != "" {
			content = append(content, AnthropicContentBlock{
				Type: "text",
				Text: text,
			})
		}
	}

	// Add tool calls if present
	if len(choice.Message.ToolCalls) > 0 {
		for _, toolCall := range choice.Message.ToolCalls {
			// Parse arguments back to map[string]interface{}
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
				// If parsing fails, create a map with the raw string
				args = map[string]interface{}{
					"raw_arguments": toolCall.Function.Arguments,
				}
			}

			block := AnthropicContentBlock{
				Type:  "tool_use",
				ID:    toolCall.ID,
				Name:  toolCall.Function.Name,
				Input: args,
			}
			content = append(content, block)
		}
	}

	anthResp.Content = content

	// Map finish reason
	switch choice.FinishReason {
	case "stop":
		anthResp.StopReason = "end_turn"
	case "length":
		anthResp.StopReason = "max_tokens"
	case "tool_calls":
		anthResp.StopReason = "tool_use"
	default:
		anthResp.StopReason = choice.FinishReason
	}

	return anthResp
}

// ConvertOpenAIRequestToAnthropic converts an OpenAI request to Anthropic format
// ARCHITECTURAL NOTE: This conversion ALWAYS goes through TypedRequest as the
// canonical intermediate representation. This ensures consistency and maintains
// a single source of truth for message handling logic.
// Flow: OpenAI -> TypedRequest -> Anthropic
func (c *Converter) ConvertOpenAIRequestToAnthropic(ctx context.Context, req *OpenAIRequest) (*AnthropicRequest, error) {
	// Use the typed conversion path as the single source of truth
	typed := OpenAIRequestToTyped(req)

	// Convert typed to Anthropic format
	anthReq := &AnthropicRequest{
		Model:         req.Model, // Use the original model from the request
		Stream:        typed.Stream,
		StopSequences: typed.Stop,
		Temperature:   typed.Temperature,
		TopP:          typed.TopP,
	}

	// Set max tokens if provided
	if typed.MaxTokens != nil {
		anthReq.MaxTokens = *typed.MaxTokens
	}

	// Set system message if present
	if typed.System != "" {
		systemJSON, _ := json.Marshal(typed.System)
		anthReq.System = json.RawMessage(systemJSON)
	}

	// Convert messages using core.ToAnthropicTyped
	typedAnthMessages := core.ToAnthropicTyped(typed.Messages)
	messages := make([]AnthropicMessage, 0, len(typedAnthMessages))
	for _, msg := range typedAnthMessages {
		// Convert core.AnthropicMessage to proxy.AnthropicMessage
		anthMsg := AnthropicMessage{
			Role: core.Role(msg.Role),
		}

		// Handle content conversion
		if msg.Content != nil {
			contentJSON, err := json.Marshal(msg.Content)
			if err != nil {
				// Log error but continue
				logger.From(ctx).Warnf("Failed to marshal content: %v", err)
				anthMsg.Content = json.RawMessage(`""`)
			} else {
				anthMsg.Content = json.RawMessage(contentJSON)
			}
		} else {
			// Empty content
			anthMsg.Content = json.RawMessage(`""`)
		}

		messages = append(messages, anthMsg)
	}
	anthReq.Messages = messages

	// Convert tools
	if len(typed.Tools) > 0 {
		tools := make([]AnthropicTool, len(typed.Tools))
		for i, tool := range typed.Tools {
			tools[i] = AnthropicTool{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: tool.InputSchema,
			}
		}
		anthReq.Tools = tools
	}

	// Convert tool choice
	if typed.ToolChoice != nil {
		anthReq.ToolChoice = &AnthropicToolChoice{
			Type: typed.ToolChoice.Type,
			Name: typed.ToolChoice.Name,
		}
	}

	return anthReq, nil
}

// ConvertAnthropicResponseToOpenAI converts an Anthropic response to OpenAI format
func (c *Converter) ConvertAnthropicResponseToOpenAI(resp *AnthropicResponse, originalModel string) *OpenAIResponse {
	// Generate ID if missing
	responseID := resp.ID
	if responseID == "" {
		responseID = generateUUID("chatcmpl-")
	} else if !strings.HasPrefix(responseID, "chatcmpl-") {
		// If we have an ID but it's not in OpenAI format, prepend the prefix
		responseID = "chatcmpl-" + responseID
	}

	// Convert Anthropic response to OpenAI response format
	openAIResp := &OpenAIResponse{
		ID:      responseID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   originalModel,
	}

	// Convert usage if present
	if resp.Usage != nil {
		openAIResp.Usage = AnthropicUsageToOpenAI(resp.Usage)
	}

	// Convert Anthropic blocks to core.Blocks using centralized converter
	coreBlocks := AnthropicBlocksToCore(resp.Content)

	// Use unified converter to get OpenAI content and tool calls
	content, toolCallMaps := core.ConvertBlocksToOpenAIContentMap(coreBlocks)

	// Build the message
	message := OpenAIMessage{
		Role:    core.RoleAssistant,
		Content: content,
	}

	// Convert tool call maps to ToolCall structs
	if len(toolCallMaps) > 0 {
		message.ToolCalls = make([]ToolCall, len(toolCallMaps))
		for i, tcMap := range toolCallMaps {
			fn := tcMap["function"].(map[string]interface{})
			message.ToolCalls[i] = ToolCall{
				ID:   tcMap["id"].(string),
				Type: tcMap["type"].(string),
				Function: FunctionCall{
					Name:      fn["name"].(string),
					Arguments: fn["arguments"].(string),
				},
			}
		}
	}

	// Map stop reason to finish reason
	finishReason := MapStopReasonToOpenAIFinishReason(resp.StopReason)

	// Add single choice
	openAIResp.Choices = []OpenAIChoice{
		{
			Index:        0,
			Message:      message,
			FinishReason: finishReason,
		},
	}

	return openAIResp
}
