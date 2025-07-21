package apiproxy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ConvertAnthropicToArgo converts an Anthropic request to Argo format
func (c *Converter) ConvertAnthropicToArgo(req *AnthropicRequest, user string) (*ArgoChatRequest, error) {
	argoReq := &ArgoChatRequest{
		Model:       req.Model,
		User:        user,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.StopSequences,
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
			LogDebug(fmt.Sprintf("Dropping max_tokens field (was %d) for Argo non-streaming request", req.MaxTokens))
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
		var systemContent interface{}

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
		var content interface{}

		// Try to unmarshal as array first
		var contentArray []AnthropicContentBlock
		if err := json.Unmarshal(msg.Content, &contentArray); err == nil {
			// For OpenAI models, we need to convert tool blocks
			if provider == "openai" {
				// Handle different message types
				if msg.Role == RoleUser {
					// User messages with tool_result blocks
					for _, block := range contentArray {
						if block.Type == "tool_result" {
							// Convert tool_result to OpenAI tool message format
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
						} else {
							// For other block types in user messages, preserve as is
							content = contentArray
						}
					}
					// If we processed tool_result blocks, skip adding the original message
					if content == nil {
						continue
					}
				} else if msg.Role == RoleAssistant {
					// Assistant messages with tool_use blocks
					var textContent string
					var toolCalls []ToolCall

					for _, block := range contentArray {
						switch block.Type {
						case "text":
							textContent = block.Text
						case "tool_use":
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
					}

					// Create the assistant message with tool calls
					if len(toolCalls) > 0 {
						messages = append(messages, ArgoMessage{
							Role:      string(msg.Role),
							Content:   textContent, // OpenAI allows text content with tool calls
							ToolCalls: toolCalls,
						})
						continue // Skip the normal message addition
					} else {
						// No tool calls, treat as normal text
						content = textContent
					}
				}
			} else {
				// For non-OpenAI providers, preserve array as is
				content = contentArray
			}
		} else {
			// Try as string
			var contentStr string
			if err := json.Unmarshal(msg.Content, &contentStr); err == nil {
				content = contentStr
			} else {
				// Fallback to extracting text
				text, err := c.extractTextFromAnthropicMessage(msg)
				if err != nil {
					return nil, fmt.Errorf("failed to extract text from message: %w", err)
				}
				content = text
			}
		}

		// Only add message if we have content
		if content != nil {
			messages = append(messages, ArgoMessage{
				Role:    string(msg.Role),
				Content: content,
			})
		}
	}

	argoReq.Messages = messages

	// Convert tools based on the target model type
	if len(req.Tools) > 0 {
		// For Argo, we need to determine the underlying provider from the model name
		// Use the Argo model name to determine format, not the mapped provider
		provider := c.determineArgoModelProvider(req.Model)
		tools, toolChoice := c.convertToolsForArgoModel(provider, req.Tools, req.ToolChoice)
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
		case "tool_use":
			fmt.Fprintf(&builder, "[Calling tool: %s with args: %v]", block.Name, block.Input)
		case "tool_result":
			fmt.Fprintf(&builder, "[Tool result: %v]", block.Content)
		}
	}
	return builder.String()
}

// convertToolsForArgoModel converts Anthropic tools to the appropriate format for the Argo model
func (c *Converter) convertToolsForArgoModel(provider string, tools []AnthropicTool, toolChoice *AnthropicToolChoice) ([]ArgoTool, interface{}) {
	argoTools := make([]ArgoTool, 0, len(tools))

	switch provider {
	case "openai":
		// OpenAI format
		for _, tool := range tools {
			argoTool := map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":        tool.Name,
					"description": tool.Description,
					"parameters":  filterSchemaMetadata(tool.InputSchema),
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

	case "gemini", "google":
		// Gemini format
		for _, tool := range tools {
			// Filter out $schema before converting to Gemini format
			filteredSchema := filterSchemaMetadata(tool.InputSchema)
			// Convert Anthropic schema to Gemini format
			params := c.convertSchemaToGeminiFormat(filteredSchema)

			argoTool := map[string]interface{}{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  params,
			}

			// Add required fields if present
			if schemaMap, ok := filteredSchema.(map[string]interface{}); ok {
				if required, ok := schemaMap["required"]; ok {
					argoTool["required"] = required
				}
			}

			argoTools = append(argoTools, argoTool)
		}
		return argoTools, nil // Gemini doesn't use tool_choice in the same way

	case "anthropic", "argo":
		// Anthropic/Argo format - check if we need special handling
		// For Argo models, we need to determine the underlying provider
		for _, tool := range tools {
			argoTool := map[string]interface{}{
				"name":         tool.Name,
				"description":  tool.Description,
				"input_schema": filterSchemaMetadata(tool.InputSchema),
			}
			argoTools = append(argoTools, argoTool)
		}
		// Convert tool choice
		if toolChoice != nil {
			return argoTools, map[string]interface{}{
				"type": toolChoice.Type,
				"name": toolChoice.Name,
			}
		}
		return argoTools, map[string]string{"type": "auto"}

	default:
		// Default to OpenAI format for unknown providers
		return c.convertToolsForArgoModel("openai", tools, toolChoice)
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

	// Gemini-based models
	if strings.HasPrefix(modelLower, "gemini") {
		return "gemini"
	}

	// Claude-based models
	if strings.HasPrefix(modelLower, "claude") {
		return "anthropic"
	}

	// Default to OpenAI format for unknown models
	return "openai"
}

// convertSchemaToGeminiFormat converts JSON schema to Gemini's expected format
func (c *Converter) convertSchemaToGeminiFormat(schema interface{}) interface{} {
	return c.convertSchemaToGeminiFormatWithDepth(schema, 0, 10)
}

// convertSchemaToGeminiFormatWithDepth converts JSON schema with depth limit
func (c *Converter) convertSchemaToGeminiFormatWithDepth(schema interface{}, depth, maxDepth int) interface{} {
	if depth > maxDepth {
		return schema // Stop recursion at max depth
	}

	schemaMap, ok := schema.(map[string]interface{})
	if !ok {
		return schema
	}

	// Convert type names to uppercase for Gemini
	if typeVal, ok := schemaMap["type"].(string); ok {
		schemaMap["type"] = strings.ToUpper(typeVal)
	}

	// Recursively convert properties
	if props, ok := schemaMap["properties"].(map[string]interface{}); ok {
		for key, val := range props {
			props[key] = c.convertSchemaToGeminiFormatWithDepth(val, depth+1, maxDepth)
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

// ConvertArgoToAnthropic converts an Argo response to Anthropic format
func (c *Converter) ConvertArgoToAnthropic(resp *ArgoChatResponse, originalModel string) *AnthropicResponse {
	// Handle different response formats
	switch r := resp.Response.(type) {
	case string:
		// Simple string response
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
			Usage: AnthropicUsage{
				InputTokens:  len(r) / 4,
				OutputTokens: len(r) / 4,
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
		if toolCalls, ok := r["tool_calls"].([]interface{}); ok {
			for _, tc := range toolCalls {
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
					// Gemini format
					name, _ := toolCall["name"].(string)

					content = append(content, AnthropicContentBlock{
						Type:  "tool_use",
						ID:    generateToolUseID(), // Gemini doesn't provide IDs
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

		return &AnthropicResponse{
			Type:       "message",
			Model:      originalModel,
			Role:       RoleAssistant,
			Content:    content,
			StopReason: stopReason,
			Usage: AnthropicUsage{
				InputTokens:  100, // Estimate
				OutputTokens: 100,
			},
		}

	default:
		// Fallback: treat as JSON string
		text := fmt.Sprintf("%v", resp.Response)
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
			Usage: AnthropicUsage{
				InputTokens:  len(text) / 4,
				OutputTokens: len(text) / 4,
			},
		}
	}
}
