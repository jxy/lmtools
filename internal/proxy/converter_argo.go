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
	if req.TopK != nil {
		logger.From(ctx).Debugf("Omitting top_k=%d from Anthropic request (not supported by Argo)", *req.TopK)
	}
	if len(req.Metadata) > 0 {
		logger.DebugJSON(logger.From(ctx), "Omitting metadata from Anthropic request (not supported by Argo)", req.Metadata)
	}

	argoReq := &ArgoChatRequest{
		Model:       req.Model,
		User:        user,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.StopSequences,
	}

	// Handle thinking field based on model type
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

	// Determine the provider to handle max_tokens correctly
	provider := core.DetermineArgoModelProvider(req.Model)

	// Handle max_tokens based on provider
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

	// Build conversation history
	var messages []ArgoMessage

	// Add system message if present
	if req.System != nil {
		// Check if system is a string or array
		var systemContent string

		// Try to unmarshal as array first
		var systemArray []AnthropicContentBlock
		if err := json.Unmarshal(req.System, &systemArray); err == nil {
			// For system messages with arrays, we need to extract text
			// Argo expects system content as a string
			text, err := extractSystemContent(req.System)
			if err != nil {
				return nil, fmt.Errorf("failed to extract system content: %w", err)
			}
			systemContent = text
		} else {
			// Try as string
			var systemStr string
			if err := json.Unmarshal(req.System, &systemStr); err == nil {
				systemContent = systemStr
			} else {
				return nil, fmt.Errorf("failed to parse system content")
			}
		}

		if systemContent != "" {
			messages = append(messages, ArgoMessage{
				Role:    "system",
				Content: systemContent,
			})
		}
	}

	// Convert messages - handle content based on provider
	for _, msg := range req.Messages {
		// Check if content is already an array
		var contentArray []AnthropicContentBlock
		if err := json.Unmarshal(msg.Content, &contentArray); err == nil {
			// For OpenAI models, we need special conversion to handle thinking blocks and tools
			if provider == "openai" {
				// Process each block in the message
				var textContent string
				var hasText bool
				var toolCalls []ToolCall

				for _, block := range contentArray {
					switch block.Type {
					case "text":
						textContent += block.Text
						hasText = true

					case "thinking":
						// OpenAI/GPT models don't support thinking blocks, so we drop them and log at DEBUG level
						droppedBlock := map[string]interface{}{
							"type":     "thinking",
							"thinking": block.Thinking,
						}
						logger.From(ctx).Debugf("Dropping thinking content block for %s model (not supported by OpenAI): %+v", req.Model, droppedBlock)

					case "tool_use":
						// Only process tool_use in assistant messages
						if msg.Role == core.RoleAssistant {
							// Convert to OpenAI tool call format
							toolCall := ToolCall{
								ID:   block.ID,
								Type: "function",
								Function: FunctionCall{
									Name: block.Name,
								},
							}
							// Convert input to JSON string
							if inputJSON, err := json.Marshal(block.Input); err == nil {
								toolCall.Function.Arguments = string(inputJSON)
								logger.DebugJSON(logger.From(ctx), "Tool call arguments", block.Input)
							} else {
								toolCall.Function.Arguments = "{}"
							}
							toolCalls = append(toolCalls, toolCall)
						}

					case "tool_result":
						// Convert tool_result to separate OpenAI tool message
						toolMsg := ArgoMessage{
							Role:       "tool",
							ToolCallID: block.ToolUseID,
						}
						// Extract content from the tool result
						var toolContent interface{}
						if err := json.Unmarshal(block.Content, &toolContent); err == nil {
							if contentStr, ok := toolContent.(string); ok {
								toolMsg.Content = contentStr
							} else {
								toolMsg.Content = string(block.Content)
							}
						} else {
							toolMsg.Content = string(block.Content)
						}
						messages = append(messages, toolMsg)
					}
				}

				// Add the message with text and/or tool calls if applicable
				if msg.Role == core.RoleAssistant && len(toolCalls) > 0 {
					// Assistant message with tool calls
					messages = append(messages, ArgoMessage{
						Role:      string(msg.Role),
						Content:   textContent, // OpenAI allows text content with tool calls
						ToolCalls: toolCalls,
					})
				} else if hasText {
					// Message with text content
					messages = append(messages, ArgoMessage{
						Role:    string(msg.Role),
						Content: textContent,
					})
				}
				// If only tool_result blocks were processed, they were already added as separate messages

			} else {
				// For non-OpenAI providers or OpenAI without tool blocks, preserve array as is
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

// ConvertArgoToAnthropicWithRequest converts an Argo response to Anthropic format with request for token estimation
func (c *Converter) ConvertArgoToAnthropicWithRequest(resp *ArgoChatResponse, originalModel string, req *AnthropicRequest) *AnthropicResponse {
	// Handle different response formats
	switch r := resp.Response.(type) {
	case string:
		// Simple string response
		outputTokens := EstimateTokenCount(r)
		inputTokens := EstimateRequestTokens(req)
		return &AnthropicResponse{
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
