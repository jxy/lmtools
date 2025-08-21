package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ConvertAnthropicToArgo converts an Anthropic request to Argo format
func (c *Converter) ConvertAnthropicToArgo(ctx context.Context, req *AnthropicRequest, user string) (*ArgoChatRequest, error) {
	// Log omitted fields at DEBUG level
	if req.TopK != nil {
		LogDebugCtx(ctx, fmt.Sprintf("Omitting top_k=%d from Anthropic request (not supported by Argo)", *req.TopK))
	}
	if len(req.Metadata) > 0 {
		LogDebugCtx(ctx, fmt.Sprintf("Omitting metadata from Anthropic request (not supported by Argo): %s", formatJSONForLog(req.Metadata)))
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
			LogDebugCtx(ctx, fmt.Sprintf("Converting thinking.budget_tokens=%d to reasoning_effort=high for model %s", req.Thinking.BudgetTokens, req.Model))
		} else if strings.HasPrefix(modelLower, "claude") {
			// For Claude models (opus/sonnet), pass through the thinking structure
			argoReq.Thinking = req.Thinking
			LogDebugCtx(ctx, fmt.Sprintf("Passing through thinking structure for Claude model %s", req.Model))
		}
	}

	// Determine the provider to handle max_tokens correctly
	provider := c.determineArgoModelProvider(req.Model)

	// Handle max_tokens based on provider
	if provider == "openai" {
		// For OpenAI models, use max_completion_tokens
		argoReq.MaxCompletionTokens = req.MaxTokens
	} else {
		// For non-OpenAI models, handle max_tokens for Argo non-streaming requests
		if !req.Stream && req.MaxTokens >= 21000 {
			// Drop max_tokens field entirely if >= 21000
			LogDebugCtx(ctx, fmt.Sprintf("Dropping max_tokens field (was %d) for Argo non-streaming request", req.MaxTokens))
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
						LogDebugCtx(ctx, fmt.Sprintf("Dropping thinking content block for %s model (not supported by OpenAI): %s", req.Model, formatJSONForLog(droppedBlock)))

					case "tool_use":
						// Only process tool_use in assistant messages
						if msg.Role == RoleAssistant {
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
				if msg.Role == RoleAssistant && len(toolCalls) > 0 {
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
		// Use the Argo model name to determine format, not the mapped provider
		provider := c.determineArgoModelProvider(req.Model)
		tools, toolChoice := c.convertToolsForArgoModel(provider, req.Model, req.Tools, req.ToolChoice)
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
func (c *Converter) convertToolsForArgoModel(provider string, model string, tools []AnthropicTool, toolChoice *AnthropicToolChoice) (interface{}, interface{}) {
	switch provider {
	case "openai":
		// OpenAI format with type/function wrapper
		argoTools := make([]ArgoTool, 0, len(tools))
		for _, tool := range tools {
			argoTool := ArgoTool{
				Type: "function",
				Function: ArgoFunc{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  filterSchemaMetadata(tool.InputSchema),
				},
			}
			argoTools = append(argoTools, argoTool)
		}
		// Convert tool choice
		if toolChoice != nil {
			if toolChoice.Type == "tool" && toolChoice.Name != "" {
				return argoTools, map[string]interface{}{
					"type": "function",
					"function": map[string]string{
						"name": toolChoice.Name,
					},
				}
			}
			return argoTools, toolChoice.Type
		}
		return argoTools, "auto"

	case "google":
		// Google format - unwrapped with parameters field
		var googleTools []map[string]interface{}
		for _, tool := range tools {
			// Filter out $schema before converting to Google format
			filteredSchema := filterSchemaMetadata(tool.InputSchema)
			// Convert to Google format with uppercase types
			params := c.convertSchemaToGoogleFormat(filteredSchema)

			googleTool := map[string]interface{}{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  params,
			}

			// Extract and add required field at top level if it exists
			if schema, ok := filteredSchema.(map[string]interface{}); ok {
				if required, exists := schema["required"]; exists {
					googleTool["required"] = required
				}
			}

			googleTools = append(googleTools, googleTool)
		}
		return googleTools, nil // Google doesn't use tool_choice in the same way

	case "anthropic":
		// Anthropic format - unwrapped with input_schema field
		var anthropicTools []map[string]interface{}
		for _, tool := range tools {
			anthropicTool := map[string]interface{}{
				"name":         tool.Name,
				"description":  tool.Description,
				"input_schema": filterSchemaMetadata(tool.InputSchema),
			}
			anthropicTools = append(anthropicTools, anthropicTool)
		}
		// Convert tool choice
		if toolChoice != nil {
			return anthropicTools, map[string]interface{}{
				"type": toolChoice.Type,
				"name": toolChoice.Name,
			}
		}
		return anthropicTools, map[string]string{"type": "auto"}

	case "argo":
		// Determine format based on model prefix
		modelLower := strings.ToLower(model)

		if strings.HasPrefix(modelLower, "claude") {
			// Use Anthropic format for Claude models
			return c.convertToolsForArgoModel("anthropic", model, tools, toolChoice)
		} else if strings.HasPrefix(modelLower, "gemini") {
			// Use Google format for these models
			return c.convertToolsForArgoModel("google", model, tools, toolChoice)
		} else if strings.HasPrefix(modelLower, "gpt") || strings.HasPrefix(modelLower, "gpto") {
			// Use OpenAI format for GPT models
			return c.convertToolsForArgoModel("openai", model, tools, toolChoice)
		} else {
			// Default to OpenAI format for unknown models
			return c.convertToolsForArgoModel("openai", model, tools, toolChoice)
		}

	default:
		// Default to OpenAI format for unknown providers
		return c.convertToolsForArgoModel("openai", model, tools, toolChoice)
	}
}

// determineArgoModelProvider determines the underlying provider for an Argo model
func (c *Converter) determineArgoModelProvider(model string) string {
	// Map Argo models to their underlying providers
	modelLower := strings.ToLower(model)

	// OpenAI-based models
	if strings.HasPrefix(modelLower, "gpt") || strings.HasPrefix(modelLower, "o1") || strings.HasPrefix(modelLower, "o3") {
		return "openai"
	}

	// Google AI models
	if strings.HasPrefix(modelLower, "gemini") {
		return "google"
	}

	// Claude-based models
	if strings.HasPrefix(modelLower, "claude") {
		return "anthropic"
	}

	// Default to OpenAI format for unknown models
	return "openai"
}

// convertSchemaToGoogleFormat converts JSON schema to Google AI's expected format
func (c *Converter) convertSchemaToGoogleFormat(schema interface{}) interface{} {
	return c.convertSchemaToGoogleFormatWithDepth(schema, 0, 10)
}

// convertSchemaToGoogleFormatWithDepth converts JSON schema with depth limit
func (c *Converter) convertSchemaToGoogleFormatWithDepth(schema interface{}, depth, maxDepth int) interface{} {
	if depth > maxDepth {
		return schema // Stop recursion at max depth
	}

	schemaMap, ok := schema.(map[string]interface{})
	if !ok {
		return schema
	}

	// Convert type names to uppercase for Google AI
	if typeVal, ok := schemaMap["type"].(string); ok {
		schemaMap["type"] = strings.ToUpper(typeVal)
	}

	// Recursively convert properties
	if props, ok := schemaMap["properties"].(map[string]interface{}); ok {
		for key, val := range props {
			props[key] = c.convertSchemaToGoogleFormatWithDepth(val, depth+1, maxDepth)
		}
	}

	return schemaMap
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

// ConvertArgoToAnthropic converts an Argo response to Anthropic format
// Note: For Argo provider, we need the original request to estimate input tokens
func (c *Converter) ConvertArgoToAnthropic(resp *ArgoChatResponse, originalModel string) *AnthropicResponse {
	// This method is deprecated - use ConvertArgoToAnthropicWithRequest instead
	// We can't estimate input tokens without the request
	return c.ConvertArgoToAnthropicWithRequest(resp, originalModel, &AnthropicRequest{})
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
			Role:  RoleAssistant,
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

		// Check if content is an array (Anthropic format)
		if contentArray, ok := r["content"].([]interface{}); ok {
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
		} else if text, ok := r["content"].(string); ok && text != "" {
			// Legacy string content
			content = append(content, AnthropicContentBlock{
				Type: "text",
				Text: text,
			})
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
			Role:       RoleAssistant,
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
			Role:  RoleAssistant,
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
