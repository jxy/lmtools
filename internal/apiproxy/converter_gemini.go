package apiproxy

import (
	"encoding/json"
	"fmt"
)

// ConvertAnthropicToGemini converts an Anthropic request to Gemini format
func (c *Converter) ConvertAnthropicToGemini(req *AnthropicRequest) (*GeminiRequest, error) {
	geminiReq := &GeminiRequest{
		GenerationConfig: &GeminiGenerationConfig{
			Temperature:     req.Temperature,
			TopP:            req.TopP,
			MaxOutputTokens: req.MaxTokens,
			StopSequences:   req.StopSequences,
		},
	}

	// Convert messages to contents
	contents := []GeminiContent{}

	// Add system instruction if present
	if req.System != nil {
		systemContent, err := extractSystemContent(req.System)
		if err != nil {
			return nil, fmt.Errorf("failed to extract system content: %w", err)
		}
		if systemContent != "" {
			// Gemini doesn't have a direct system instruction field, prepend to first message
			contents = append(contents, GeminiContent{
				Role: "user",
				Parts: []GeminiPart{
					{Text: "System: " + systemContent},
				},
			})
		}
	}

	// Convert messages
	for _, msg := range req.Messages {
		geminiContent, err := c.convertAnthropicMessageToGemini(msg)
		if err != nil {
			return nil, fmt.Errorf("failed to convert message: %w", err)
		}
		contents = append(contents, geminiContent)
	}

	geminiReq.Contents = contents

	// Convert tools
	if len(req.Tools) > 0 {
		tools := []GeminiTool{}
		for _, tool := range req.Tools {
			// Filter out $schema and convert to Gemini format
			filteredSchema := filterSchemaMetadata(tool.InputSchema)
			parameters := c.convertSchemaToGeminiFormat(filteredSchema)
			// Type assert to map[string]interface{} - this should always succeed for valid schemas
			paramsMap, _ := parameters.(map[string]interface{})

			geminiTool := GeminiTool{
				FunctionDeclarations: []GeminiFunctionDeclaration{
					{
						Name:        tool.Name,
						Description: tool.Description,
						Parameters:  paramsMap,
					},
				},
			}
			tools = append(tools, geminiTool)
		}
		geminiReq.Tools = tools
	}

	return geminiReq, nil
}

// convertAnthropicMessageToGemini converts a single Anthropic message to Gemini format
func (c *Converter) convertAnthropicMessageToGemini(msg AnthropicMessage) (GeminiContent, error) {
	// Map roles
	role := "user"
	if msg.Role == RoleAssistant {
		role = "model"
	}

	geminiContent := GeminiContent{
		Role:  role,
		Parts: []GeminiPart{},
	}

	// Handle different content types - msg.Content is json.RawMessage
	// Try to parse as string first
	var str string
	if err := json.Unmarshal(msg.Content, &str); err == nil {
		geminiContent.Parts = append(geminiContent.Parts, GeminiPart{Text: str})
		return geminiContent, nil
	}

	// Try as content blocks
	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err == nil {
		parts, err := c.convertContentBlocksToGemini(blocks)
		if err != nil {
			return geminiContent, err
		}
		geminiContent.Parts = parts
		return geminiContent, nil
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
		parts, err := c.convertContentBlocksToGemini(blocks)
		if err != nil {
			return geminiContent, err
		}
		geminiContent.Parts = parts
		return geminiContent, nil
	}

	// Fall back to string representation
	geminiContent.Parts = append(geminiContent.Parts, GeminiPart{Text: string(msg.Content)})

	return geminiContent, nil
}

// convertContentBlocksToGemini converts Anthropic content blocks to Gemini parts
func (c *Converter) convertContentBlocksToGemini(blocks []AnthropicContentBlock) ([]GeminiPart, error) {
	parts := []GeminiPart{}

	for _, block := range blocks {
		switch block.Type {
		case "text":
			parts = append(parts, GeminiPart{Text: block.Text})
		case "tool_use":
			// Convert to function call - block.Input is already map[string]interface{}
			parts = append(parts, GeminiPart{
				FunctionCall: &GeminiFunctionCall{
					Name: block.Name,
					Args: block.Input,
				},
			})
		case "tool_result":
			// Convert to function response
			// block.Content is json.RawMessage, parse it
			var content interface{}
			if err := json.Unmarshal(block.Content, &content); err != nil {
				// If unmarshal fails, use string representation
				content = string(block.Content)
			}
			if contentStr, ok := content.(string); ok {
				content = map[string]interface{}{"result": contentStr}
			}
			// Ensure content is a map
			responseMap, ok := content.(map[string]interface{})
			if !ok {
				responseMap = map[string]interface{}{"result": content}
			}
			parts = append(parts, GeminiPart{
				FunctionResponse: &GeminiFunctionResponse{
					Name:     block.ToolUseID, // Gemini uses the function name, we use the ID
					Response: responseMap,
				},
			})
		}
	}

	return parts, nil
}

// ConvertGeminiToAnthropic converts a Gemini response to Anthropic format
func (c *Converter) ConvertGeminiToAnthropic(resp *GeminiResponse, originalModel string) *AnthropicResponse {
	anthResp := &AnthropicResponse{
		Type:  "message",
		Model: originalModel,
		Role:  RoleAssistant,
		Usage: AnthropicUsage{
			InputTokens:  resp.UsageMetadata.PromptTokenCount,
			OutputTokens: resp.UsageMetadata.CandidatesTokenCount,
		},
	}

	// Convert candidates to content blocks
	content := []AnthropicContentBlock{}

	if len(resp.Candidates) > 0 {
		candidate := resp.Candidates[0]

		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				content = append(content, AnthropicContentBlock{
					Type: "text",
					Text: part.Text,
				})
			} else if part.FunctionCall != nil {
				// Convert function call to tool use
				content = append(content, AnthropicContentBlock{
					Type:  "tool_use",
					ID:    generateToolUseID(),
					Name:  part.FunctionCall.Name,
					Input: part.FunctionCall.Args,
				})
			}
		}

		// Map finish reason
		switch candidate.FinishReason {
		case "STOP":
			anthResp.StopReason = "end_turn"
		case "MAX_TOKENS":
			anthResp.StopReason = "max_tokens"
		case "SAFETY":
			anthResp.StopReason = "stop_sequence"
		default:
			anthResp.StopReason = "end_turn"
		}
	}

	anthResp.Content = content
	return anthResp
}
