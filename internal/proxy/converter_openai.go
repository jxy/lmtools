package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ConvertAnthropicToOpenAI converts an Anthropic request to OpenAI format
func (c *Converter) ConvertAnthropicToOpenAI(ctx context.Context, req *AnthropicRequest) (*OpenAIRequest, error) {
	// Log omitted fields at DEBUG level
	if req.TopK != nil {
		LogDebugCtx(ctx, fmt.Sprintf("Omitting top_k=%d from Anthropic request (not supported by OpenAI)", *req.TopK))
	}
	if len(req.Metadata) > 0 {
		LogDebugCtx(ctx, fmt.Sprintf("Omitting metadata from Anthropic request (not supported by OpenAI): %v", req.Metadata))
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
			LogDebugCtx(ctx, fmt.Sprintf("Converting thinking.budget_tokens=%d to reasoning_effort=high for model %s", req.Thinking.BudgetTokens, req.Model))
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
				Role:    RoleSystem,
				Content: systemContent,
			})
		}
	}

	// Convert conversation messages
	for _, msg := range req.Messages {
		openAIMsg, err := c.convertAnthropicMessageToOpenAI(msg)
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
func (c *Converter) convertAnthropicMessageToOpenAI(msg AnthropicMessage) (OpenAIMessage, error) {
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
		return c.convertContentBlocksToOpenAI(string(msg.Role), blocks)
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
		return c.convertContentBlocksToOpenAI(string(msg.Role), blocks)
	}

	// Fall back to string representation
	openAIMsg.Content = string(msg.Content)

	return openAIMsg, nil
}

// convertContentBlocksToOpenAI converts Anthropic content blocks to OpenAI message format
func (c *Converter) convertContentBlocksToOpenAI(role string, blocks []AnthropicContentBlock) (OpenAIMessage, error) {
	msg := OpenAIMessage{Role: Role(role)}

	// Check if this is a tool call response
	var hasToolCalls bool
	var toolCalls []ToolCall
	textContent := ""

	for _, block := range blocks {
		switch block.Type {
		case "text":
			textContent += block.Text
		case "tool_use":
			hasToolCalls = true
			// Convert tool input to JSON string
			inputJSON, err := json.Marshal(block.Input)
			if err != nil {
				return msg, fmt.Errorf("failed to marshal tool input: %w", err)
			}
			toolCall := ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: FunctionCall{
					Name:      block.Name,
					Arguments: string(inputJSON),
				},
			}
			toolCalls = append(toolCalls, toolCall)
		case "tool_result":
			// This is a tool response message
			msg.Role = "tool"
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

	// If we have tool calls, add them to the message
	if hasToolCalls {
		msg.ToolCalls = toolCalls
		// OpenAI expects null content when tool_calls are present
		msg.Content = nil
	} else {
		msg.Content = textContent
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
		Role:  RoleAssistant,
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
