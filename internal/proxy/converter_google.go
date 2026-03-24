package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/logger"
)

// ConvertAnthropicToGoogle converts an Anthropic request to Google AI format
func (c *Converter) ConvertAnthropicToGoogle(ctx context.Context, req *AnthropicRequest) (*GoogleRequest, error) {
	// Log omitted fields at DEBUG level
	if len(req.Metadata) > 0 {
		logger.DebugJSON(logger.From(ctx), "Omitting metadata from Anthropic request (not supported by Google)", req.Metadata)
	}
	if req.ToolChoice != nil {
		logger.From(ctx).Debugf("Omitting tool_choice from Anthropic request (Google uses different tool configuration): type=%s, name=%s", req.ToolChoice.Type, req.ToolChoice.Name)
	}

	googleReq := &GoogleRequest{
		GenerationConfig: &GoogleGenerationConfig{
			Temperature:     req.Temperature,
			TopP:            req.TopP,
			MaxOutputTokens: &req.MaxTokens,
			StopSequences:   req.StopSequences,
		},
	}

	// Note: top_k is included in GenerationConfig if provided
	if req.TopK != nil {
		googleReq.GenerationConfig.TopK = req.TopK
	}

	// Convert messages to contents
	contents := []GoogleContent{}

	// Add system instruction if present
	if req.System != nil {
		systemContent, err := extractSystemContent(req.System)
		if err != nil {
			return nil, fmt.Errorf("failed to extract system content: %w", err)
		}
		if systemContent != "" {
			// Google doesn't have a direct system instruction field, prepend to first message
			contents = append(contents, GoogleContent{
				Role: "user",
				Parts: []GooglePart{
					{Text: "System: " + systemContent},
				},
			})
		}
	}

	// Convert messages
	for _, msg := range req.Messages {
		googleContent, err := c.convertAnthropicMessageToGoogle(msg)
		if err != nil {
			return nil, fmt.Errorf("failed to convert message: %w", err)
		}
		contents = append(contents, googleContent)
	}

	googleReq.Contents = contents

	// Convert tools
	if len(req.Tools) > 0 {
		tools := []GoogleTool{}
		for _, tool := range req.Tools {
			// Filter out $schema and convert to Google format
			filteredSchema := filterSchemaMetadata(tool.InputSchema)
			parameters := core.ConvertSchemaToGoogleFormat(filteredSchema)
			// Type assert to map[string]interface{} - this should always succeed for valid schemas
			paramsMap, _ := parameters.(map[string]interface{})

			googleTool := GoogleTool{
				FunctionDeclarations: []GoogleFunctionDeclaration{
					{
						Name:        tool.Name,
						Description: tool.Description,
						Parameters:  paramsMap,
					},
				},
			}
			tools = append(tools, googleTool)
		}
		googleReq.Tools = tools
	}

	return googleReq, nil
}

// convertAnthropicMessageToGoogle converts a single Anthropic message to Google AI format
func (c *Converter) convertAnthropicMessageToGoogle(msg AnthropicMessage) (GoogleContent, error) {
	// Map roles
	role := "user"
	if msg.Role == core.RoleAssistant {
		role = "model"
	}

	googleContent := GoogleContent{
		Role:  role,
		Parts: []GooglePart{},
	}

	text, blocks, err := parseAnthropicMessageContent(msg.Content)
	if err == nil && text != nil {
		googleContent.Parts = append(googleContent.Parts, GooglePart{Text: *text})
		return googleContent, nil
	}
	if err == nil {
		parts, err := c.convertContentBlocksToGoogle(blocks)
		if err != nil {
			return googleContent, err
		}
		googleContent.Parts = parts
		return googleContent, nil
	}

	// Fall back to string representation
	googleContent.Parts = append(googleContent.Parts, GooglePart{Text: string(msg.Content)})

	return googleContent, nil
}

// convertContentBlocksToGoogle converts Anthropic content blocks to Google AI parts
func (c *Converter) convertContentBlocksToGoogle(blocks []AnthropicContentBlock) ([]GooglePart, error) {
	parts := []GooglePart{}

	for _, block := range blocks {
		switch block.Type {
		case "text":
			parts = append(parts, GooglePart{Text: block.Text})
		case "tool_use":
			// Convert to function call - block.Input is already map[string]interface{}
			parts = append(parts, GooglePart{
				FunctionCall: &GoogleFunctionCall{
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
			parts = append(parts, GooglePart{
				FunctionResp: &GoogleFunctionResp{
					Name:     block.ToolUseID, // Google uses the function name, we use the ID
					Response: responseMap,
				},
			})
		}
	}

	return parts, nil
}

// ConvertGoogleToAnthropic converts a Google AI response to Anthropic format
func (c *Converter) ConvertGoogleToAnthropic(resp *GoogleResponse, originalModel string) *AnthropicResponse {
	anthResp := &AnthropicResponse{
		Type:  "message",
		Model: originalModel,
		Role:  core.RoleAssistant,
	}

	// Set usage if available
	if resp.UsageMetadata != nil {
		anthResp.Usage = &AnthropicUsage{
			InputTokens:  resp.UsageMetadata.PromptTokenCount,
			OutputTokens: resp.UsageMetadata.CandidatesTokenCount,
		}
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
