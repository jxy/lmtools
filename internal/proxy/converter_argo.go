package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"strings"
)

// ConvertAnthropicToArgo converts an Anthropic request to Argo format
func (c *Converter) ConvertAnthropicToArgo(ctx context.Context, req *AnthropicRequest, user string) (*ArgoChatRequest, error) {
	// Log omitted fields at DEBUG level
	c.logOmittedFields(ctx, req)

	argoReq := &ArgoChatRequest{
		Model:       req.Model,
		User:        user,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.StopSequences,
	}

	// Handle thinking field
	c.applyThinkingConfig(ctx, req, argoReq)

	// Determine the provider to handle max_tokens correctly
	provider := core.DetermineArgoModelProvider(req.Model)

	// Handle max_tokens based on provider
	c.setArgoMaxTokens(ctx, req, argoReq, provider)

	// Build conversation history
	var messages []ArgoMessage

	// Add system message if present
	systemMsg, err := c.extractArgoSystemMessage(req.System)
	if err != nil {
		return nil, err
	}
	if systemMsg != nil {
		messages = append(messages, *systemMsg)
	}

	// Convert messages - handle content based on provider
	for _, msg := range req.Messages {
		// Check if content is already an array
		var contentArray []AnthropicContentBlock
		if err := json.Unmarshal(msg.Content, &contentArray); err == nil {
			// For OpenAI models, use centralized converters to avoid duplication
			if provider == "openai" {
				// Log thinking blocks before conversion (they'll be filtered out by AnthropicBlocksToCore)
				for _, block := range contentArray {
					if block.Type == "thinking" {
						droppedBlock := map[string]interface{}{
							"type":     "thinking",
							"thinking": block.Thinking,
						}
						logger.From(ctx).Debugf("Dropping thinking content block for %s model (not supported by OpenAI): %+v", req.Model, droppedBlock)
					}
				}

				// Convert Anthropic blocks to core.Block using existing converter
				coreBlocks := AnthropicBlocksToCore(contentArray)

				// Separate tool results from other blocks
				var filteredBlocks []core.Block
				var toolResultMessages []ArgoMessage

				for _, block := range coreBlocks {
					switch b := block.(type) {
					case core.ToolResultBlock:
						// Tool results become separate messages in OpenAI format
						toolResultMessages = append(toolResultMessages, ArgoMessage{
							Role:       "tool",
							ToolCallID: b.ToolUseID,
							Content:    b.Content,
						})
					default:
						// Keep all other blocks
						filteredBlocks = append(filteredBlocks, block)
					}
				}

				// Add tool result messages first
				messages = append(messages, toolResultMessages...)

				// Use centralized converter for content and tool calls
				if len(filteredBlocks) > 0 {
					content, toolCallMaps := core.ConvertBlocksToOpenAIContentMap(filteredBlocks)

					// Convert tool call maps to ToolCall structs
					var toolCalls []ToolCall
					for _, tcMap := range toolCallMaps {
						fn := tcMap["function"].(map[string]interface{})
						toolCalls = append(toolCalls, ToolCall{
							ID:   tcMap["id"].(string),
							Type: tcMap["type"].(string),
							Function: FunctionCall{
								Name:      fn["name"].(string),
								Arguments: fn["arguments"].(string),
							},
						})
					}

					// For assistant messages with only tool calls and no content, use empty string
					if msg.Role == core.RoleAssistant && len(toolCalls) > 0 && content == nil {
						content = ""
					}

					// Create the message with content and tool calls
					messages = append(messages, ArgoMessage{
						Role:      string(msg.Role),
						Content:   content,
						ToolCalls: toolCalls,
					})
				}

			} else {
				// For non-OpenAI providers, preserve array as is
				messages = append(messages, ArgoMessage{
					Role:    string(msg.Role),
					Content: contentArray,
				})
			}
		} else {
			// Try as string
			var contentStr string
			if err := json.Unmarshal(msg.Content, &contentStr); err == nil {
				messages = append(messages, ArgoMessage{
					Role:    string(msg.Role),
					Content: contentStr,
				})
			} else {
				// Fallback to extracting text
				text, err := c.extractTextFromAnthropicMessage(msg)
				if err != nil {
					return nil, fmt.Errorf("failed to extract text from message: %w", err)
				}
				messages = append(messages, ArgoMessage{
					Role:    string(msg.Role),
					Content: text,
				})
			}
		}
	}

	argoReq.Messages = messages

	// Convert tools based on the target model type
	if len(req.Tools) > 0 {
		// For Argo, we need to determine the underlying provider from the model name
		// Convert tools to appropriate format based on model
		tools, toolChoice := c.convertToolsForArgoModel(req.Model, req.Tools, req.ToolChoice)
		argoReq.Tools = tools
		argoReq.ToolChoice = toolChoice
	}

	return argoReq, nil
}

// extractTextFromAnthropicMessage extracts text content from an Anthropic message
func (c *Converter) extractTextFromAnthropicMessage(msg AnthropicMessage) (string, error) {
	// Try to parse as string first
	var str string
	if err := json.Unmarshal(msg.Content, &str); err == nil {
		return str, nil
	}

	// Try as content blocks
	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err == nil {
		return c.extractTextFromContentBlocks(blocks), nil
	}

	// Try as array of interfaces
	var items []interface{}
	if err := json.Unmarshal(msg.Content, &items); err == nil {
		if len(items) == 0 {
			return "", nil
		}

		var builder strings.Builder
		builder.Grow(len(items) * 50) // Approximate capacity

		first := true
		for _, item := range items {
			if blockMap, ok := item.(map[string]interface{}); ok {
				if blockType, ok := blockMap["type"].(string); ok && blockType == "text" {
					if blockText, ok := blockMap["text"].(string); ok {
						if !first {
							builder.WriteByte('\n')
						}
						builder.WriteString(blockText)
						first = false
					}
				}
			}
		}
		return builder.String(), nil
	}

	// Fall back to string representation
	return string(msg.Content), nil
}

// extractTextFromContentBlocks extracts text from content blocks
func (c *Converter) extractTextFromContentBlocks(blocks []AnthropicContentBlock) string {
	if len(blocks) == 0 {
		return ""
	}

	var builder strings.Builder
	// Pre-allocate approximate capacity
	builder.Grow(len(blocks) * 100)

	for i, block := range blocks {
		if i > 0 {
			builder.WriteByte('\n')
		}

		switch block.Type {
		case "text":
			builder.WriteString(block.Text)
		case "thinking":
			// Include thinking content if present
			if block.Thinking != "" {
				fmt.Fprintf(&builder, "[Thinking: %s]", block.Thinking)
			}
		case "tool_use":
			fmt.Fprintf(&builder, "[Calling tool: %s with args: %v]", block.Name, block.Input)
		case "tool_result":
			fmt.Fprintf(&builder, "[Tool result: %v]", block.Content)
		}
	}
	return builder.String()
}

// convertToolsForArgoModel converts Anthropic tools to the appropriate format for the Argo model
// This now delegates to the core function to avoid duplication
func (c *Converter) convertToolsForArgoModel(model string, tools []AnthropicTool, toolChoice *AnthropicToolChoice) (interface{}, interface{}) {
	// Convert AnthropicTool to core.ToolDefinition
	toolDefs := make([]core.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		// Filter metadata before converting
		filteredSchema := filterSchemaMetadata(tool.InputSchema)
		toolDef := core.ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: filteredSchema,
		}
		toolDefs = append(toolDefs, toolDef)
	}

	// Convert AnthropicToolChoice to core.ToolChoice
	var coreToolChoice *core.ToolChoice
	if toolChoice != nil {
		coreToolChoice = &core.ToolChoice{
			Type: toolChoice.Type,
			Name: toolChoice.Name,
		}
	}

	// Delegate to core function
	converted := core.ConvertToolsForProvider(model, toolDefs, coreToolChoice)
	return converted.Tools, converted.ToolChoice
}

// filterSchemaMetadata removes JSON Schema metadata fields like $schema from a schema object
func filterSchemaMetadata(schema interface{}) interface{} {
	schemaMap, ok := schema.(map[string]interface{})
	if !ok {
		return schema
	}

	// Create a new map without the $schema field
	filtered := make(map[string]interface{})
	for key, val := range schemaMap {
		if key != "$schema" {
			// Recursively filter nested objects
			if nestedMap, ok := val.(map[string]interface{}); ok {
				filtered[key] = filterSchemaMetadata(nestedMap)
			} else {
				filtered[key] = val
			}
		}
	}
	return filtered
}

// estimateTokenCount estimates token count from content blocks using simple heuristic
func estimateTokenCount(content []AnthropicContentBlock) int {
	totalLength := 0
	for _, block := range content {
		switch block.Type {
		case "text":
			totalLength += len(block.Text)
		case "tool_use":
			// Estimate tool use length based on name and input
			totalLength += len(block.Name)
			if block.Input != nil {
				// Convert input to JSON string to estimate length
				if inputJSON, err := json.Marshal(block.Input); err == nil {
					totalLength += len(inputJSON)
				}
			}
		}
	}
	// Simple heuristic: ~4 characters per token
	return totalLength / 4
}

// logOmittedFields logs fields that are omitted when converting from Anthropic to Argo
func (c *Converter) logOmittedFields(ctx context.Context, req *AnthropicRequest) {
	if req.TopK != nil {
		logger.From(ctx).Debugf("Omitting top_k=%d from Anthropic request (not supported by Argo)", *req.TopK)
	}
	if len(req.Metadata) > 0 {
		logger.DebugJSON(logger.From(ctx), "Omitting metadata from Anthropic request (not supported by Argo)", req.Metadata)
	}
}

// applyThinkingConfig applies thinking configuration based on model type
func (c *Converter) applyThinkingConfig(ctx context.Context, req *AnthropicRequest, argoReq *ArgoChatRequest) {
	if req.Thinking != nil && req.Thinking.Type == "enabled" {
		modelLower := strings.ToLower(req.Model)
		if strings.HasPrefix(modelLower, "gpt") || strings.HasPrefix(modelLower, "o3") || strings.HasPrefix(modelLower, "o4") {
			// For GPT and O3/O4 models, convert to reasoning_effort
			argoReq.ReasoningEffort = "high"
			logger.From(ctx).Debugf("Converting thinking.budget_tokens=%d to reasoning_effort=high for model %s", req.Thinking.BudgetTokens, req.Model)
		} else if strings.HasPrefix(modelLower, "claude") {
			// For Claude models (opus/sonnet), pass through the thinking structure
			argoReq.Thinking = req.Thinking
			logger.From(ctx).Debugf("Passing through thinking structure for Claude model %s", req.Model)
		}
	}
}

// setArgoMaxTokens sets max_tokens for Argo request based on provider
func (c *Converter) setArgoMaxTokens(ctx context.Context, req *AnthropicRequest, argoReq *ArgoChatRequest, provider string) {
	if provider == "openai" {
		// For OpenAI models, use max_completion_tokens
		argoReq.MaxCompletionTokens = req.MaxTokens
	} else {
		// For non-OpenAI models, handle max_tokens for Argo requests
		// Drop max_tokens >= 21000 for:
		// 1. Non-streaming requests
		// 2. Streaming requests with tools (which use the non-streaming endpoint)
		if (!req.Stream || len(req.Tools) > 0) && req.MaxTokens >= 21000 {
			// Drop max_tokens field entirely if >= 21000
			logger.From(ctx).Debugf("Dropping max_tokens field (was %d) for Argo request (streaming=%v, tools=%d)",
				req.MaxTokens, req.Stream, len(req.Tools))
			// MaxTokens will remain nil/0, which means it won't be included in JSON
		} else {
			argoReq.MaxTokens = req.MaxTokens
		}
	}
}

// extractArgoSystemMessage extracts system message from Anthropic format for Argo
func (c *Converter) extractArgoSystemMessage(system json.RawMessage) (*ArgoMessage, error) {
	if system == nil {
		return nil, nil
	}

	// Check if system is a string or array
	var systemContent string

	// Try to unmarshal as array first
	var systemArray []AnthropicContentBlock
	if err := json.Unmarshal(system, &systemArray); err == nil {
		// For system messages with arrays, we need to extract text
		// Argo expects system content as a string
		text, err := extractSystemContent(system)
		if err != nil {
			return nil, fmt.Errorf("failed to extract system content: %w", err)
		}
		systemContent = text
	} else {
		// Try as string
		var systemStr string
		if err := json.Unmarshal(system, &systemStr); err == nil {
			systemContent = systemStr
		} else {
			return nil, fmt.Errorf("failed to parse system content")
		}
	}

	if systemContent != "" {
		return &ArgoMessage{
			Role:    "system",
			Content: systemContent,
		}, nil
	}
	return nil, nil
}

// ConvertArgoToAnthropicWithRequest converts an Argo response to Anthropic format with request for token estimation
func (c *Converter) ConvertArgoToAnthropicWithRequest(resp *ArgoChatResponse, originalModel string, req *AnthropicRequest) *AnthropicResponse {
	// Generate a response ID since Argo doesn't provide one
	responseID := generateResponseID()

	// Handle different response formats
	switch r := resp.Response.(type) {
	case string:
		// Simple string response
		outputTokens := EstimateTokenCount(r)
		inputTokens := EstimateRequestTokens(req)
		return &AnthropicResponse{
			ID:    responseID,
			Type:  "message",
			Model: originalModel,
			Role:  core.RoleAssistant,
			Content: []AnthropicContentBlock{
				{
					Type: "text",
					Text: r,
				},
			},
			StopReason: "end_turn",
			Usage: &AnthropicUsage{
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
			},
		}

	case map[string]interface{}:
		// Response with content array or tool calls
		content := []AnthropicContentBlock{}

		// Check for simple content string
		if contentStr, ok := r["content"].(string); ok && contentStr != "" {
			content = append(content, AnthropicContentBlock{
				Type: "text",
				Text: contentStr,
			})
		} else if contentArray, ok := r["content"].([]interface{}); ok {
			// Parse content array
			for _, item := range contentArray {
				if blockMap, ok := item.(map[string]interface{}); ok {
					block := AnthropicContentBlock{}

					// Parse block type
					if blockType, ok := blockMap["type"].(string); ok {
						block.Type = blockType
					}

					// Parse based on type
					switch block.Type {
					case "text":
						if text, ok := blockMap["text"].(string); ok {
							block.Text = text
						}
					case "image":
						// Parse image blocks
						if source, ok := blockMap["source"].(map[string]interface{}); ok {
							block.Source = source
						}
					case "audio", "input_audio":
						// Parse audio blocks
						if inputAudio, ok := blockMap["input_audio"].(map[string]interface{}); ok {
							block.InputAudio = inputAudio
						}
					case "file":
						// Parse file blocks
						if file, ok := blockMap["file"].(map[string]interface{}); ok {
							block.File = file
						}
					case "tool_use":
						if id, ok := blockMap["id"].(string); ok {
							block.ID = id
						}
						if name, ok := blockMap["name"].(string); ok {
							block.Name = name
						}
						if input, ok := blockMap["input"].(map[string]interface{}); ok {
							block.Input = input
						}
					}

					content = append(content, block)
				}
			}
		}

		// Add tool calls (OpenAI-style format)
		// Handle both array and single object formats
		if toolCallsRaw, ok := r["tool_calls"]; ok {
			var toolCallsArray []interface{}

			// Check if it's an array or a single object
			if arr, ok := toolCallsRaw.([]interface{}); ok {
				toolCallsArray = arr
			} else if obj, ok := toolCallsRaw.(map[string]interface{}); ok {
				// Single object - wrap in array
				toolCallsArray = []interface{}{obj}
			}

			for _, tc := range toolCallsArray {
				toolCall, ok := tc.(map[string]interface{})
				if !ok {
					continue
				}

				// Convert based on format
				if fn, ok := toolCall["function"].(map[string]interface{}); ok {
					// OpenAI format
					name, _ := fn["name"].(string)
					args, _ := fn["arguments"].(string)

					// Parse arguments
					var input map[string]interface{}
					if err := json.Unmarshal([]byte(args), &input); err != nil {
						input = map[string]interface{}{"raw_arguments": args}
					}

					// Safely get ID with fallback
					id, ok := toolCall["id"].(string)
					if !ok {
						id = generateToolUseID()
					}

					content = append(content, AnthropicContentBlock{
						Type:  "tool_use",
						ID:    id,
						Name:  name,
						Input: input,
					})
				} else if input, ok := toolCall["input"].(map[string]interface{}); ok {
					// Anthropic format
					id, _ := toolCall["id"].(string)
					if id == "" {
						id = generateToolUseID()
					}
					name, _ := toolCall["name"].(string)

					content = append(content, AnthropicContentBlock{
						Type:  "tool_use",
						ID:    id,
						Name:  name,
						Input: input,
					})
				} else if args, ok := toolCall["args"].(map[string]interface{}); ok {
					// Google format
					name, _ := toolCall["name"].(string)

					content = append(content, AnthropicContentBlock{
						Type:  "tool_use",
						ID:    generateToolUseID(), // Google doesn't provide IDs
						Name:  name,
						Input: args,
					})
				}
			}
		}

		// Determine stop reason
		stopReason := "end_turn"
		if len(content) > 0 && content[len(content)-1].Type == "tool_use" {
			stopReason = "tool_use"
		}

		// Calculate token usage based on content
		outputTokens := estimateTokenCount(content)
		inputTokens := EstimateRequestTokens(req)
		return &AnthropicResponse{
			ID:         responseID,
			Type:       "message",
			Model:      originalModel,
			Role:       core.RoleAssistant,
			Content:    content,
			StopReason: stopReason,
			Usage: &AnthropicUsage{
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
			},
		}

	default:
		// Fallback: treat as JSON string
		text := fmt.Sprintf("%v", resp.Response)
		outputTokens := EstimateTokenCount(text)
		inputTokens := EstimateRequestTokens(req)
		return &AnthropicResponse{
			ID:    responseID,
			Type:  "message",
			Model: originalModel,
			Role:  core.RoleAssistant,
			Content: []AnthropicContentBlock{
				{
					Type: "text",
					Text: text,
				},
			},
			StopReason: "end_turn",
			Usage: &AnthropicUsage{
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
			},
		}
	}
}
