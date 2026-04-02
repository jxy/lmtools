package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"lmtools/internal/providers"
	"strings"
)

// ConvertAnthropicToArgo converts an Anthropic request to Argo format
func (c *Converter) ConvertAnthropicToArgo(ctx context.Context, req *AnthropicRequest, user string) (*ArgoChatRequest, error) {
	// Log omitted fields at DEBUG level
	c.logOmittedFields(ctx, req)

	if req.System != nil {
		if _, err := extractSystemContent(req.System); err != nil {
			return nil, fmt.Errorf("failed to extract system content: %w", err)
		}
	}

	// Determine the provider to handle max_tokens correctly
	provider := providers.DetermineArgoModelProvider(req.Model)
	if provider == constants.ProviderOpenAI {
		logDroppedThinkingBlocksForOpenAI(ctx, req.Messages)
	}

	typed := AnthropicRequestToTyped(req)
	argoReq, err := TypedToArgoRequest(typed, req.Model, user)
	if err != nil {
		return nil, err
	}

	// Handle thinking field
	c.applyThinkingConfig(ctx, req, argoReq)

	// Handle max_tokens based on provider
	c.setArgoMaxTokens(ctx, req, argoReq, provider)

	return argoReq, nil
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
	if provider == constants.ProviderOpenAI {
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

// ConvertArgoToAnthropicWithRequest converts an Argo response to Anthropic format with request for token estimation.
func (c *Converter) ConvertArgoToAnthropicWithRequest(resp *ArgoChatResponse, originalModel string, req *AnthropicRequest) *AnthropicResponse {
	responseID := generateResponseID()

	switch r := resp.Response.(type) {
	case string:
		return anthropicResponseFromText(responseID, originalModel, r, req)
	case map[string]interface{}:
		return convertArgoMapResponseToAnthropic(r, responseID, originalModel, req)
	default:
		return anthropicResponseFromText(responseID, originalModel, fmt.Sprintf("%v", resp.Response), req)
	}
}

func anthropicResponseFromText(responseID, originalModel, text string, req *AnthropicRequest) *AnthropicResponse {
	return anthropicResponseFromContent(responseID, originalModel, []AnthropicContentBlock{
		{
			Type: "text",
			Text: text,
		},
	}, req)
}

func anthropicResponseFromContent(responseID, originalModel string, content []AnthropicContentBlock, req *AnthropicRequest) *AnthropicResponse {
	stopReason := "end_turn"
	if len(content) > 0 && content[len(content)-1].Type == "tool_use" {
		stopReason = "tool_use"
	}

	return &AnthropicResponse{
		ID:         responseID,
		Type:       "message",
		Model:      originalModel,
		Role:       core.RoleAssistant,
		Content:    content,
		StopReason: stopReason,
		Usage: &AnthropicUsage{
			InputTokens:  EstimateRequestTokens(req),
			OutputTokens: estimateTokenCount(content),
		},
	}
}

func convertArgoMapResponseToAnthropic(response map[string]interface{}, responseID, originalModel string, req *AnthropicRequest) *AnthropicResponse {
	content := extractArgoContentBlocks(response, req)
	content = append(content, extractArgoToolUseBlocks(response["tool_calls"])...)
	return anthropicResponseFromContent(responseID, originalModel, content, req)
}

func extractArgoContentBlocks(response map[string]interface{}, req *AnthropicRequest) []AnthropicContentBlock {
	contentValue, ok := response["content"]
	if !ok {
		return nil
	}

	if contentStr, ok := contentValue.(string); ok && contentStr != "" {
		return parseArgoStringContent(contentStr, response["tool_calls"], req)
	}

	contentArray, ok := contentValue.([]interface{})
	if !ok {
		return nil
	}

	content := make([]AnthropicContentBlock, 0, len(contentArray))
	for _, item := range contentArray {
		blockMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		block, ok := parseArgoContentBlock(blockMap)
		if !ok {
			continue
		}
		content = append(content, block)
	}
	return content
}

func parseArgoStringContent(contentStr string, rawToolCalls interface{}, req *AnthropicRequest) []AnthropicContentBlock {
	content := []AnthropicContentBlock{{Type: "text", Text: contentStr}}
	exists, empty := argoToolCallPresence(rawToolCalls)
	if !empty && exists {
		return content
	}

	rebuilt, ok := rebuildEmbeddedToolCallContent(contentStr, req)
	if ok {
		return rebuilt
	}
	return content
}

func argoToolCallPresence(rawToolCalls interface{}) (exists bool, empty bool) {
	if rawToolCalls == nil {
		return false, false
	}

	toolCalls, ok := rawToolCalls.([]interface{})
	if !ok {
		return true, false
	}
	return true, len(toolCalls) == 0
}

func rebuildEmbeddedToolCallContent(contentStr string, req *AnthropicRequest) ([]AnthropicContentBlock, bool) {
	seq, suffix, err := core.ExtractEmbeddedToolCallsWithSequence(contentStr, validToolDefinitions(req))
	if err != nil || len(seq) == 0 {
		return nil, false
	}

	logger.From(context.Background()).Debugf("Argo workaround: extracted %d embedded tool calls using loose JSON parser", len(seq))

	rebuilt := make([]AnthropicContentBlock, 0, len(seq)*2+1)
	if len(seq) > 0 {
		rebuilt = append(rebuilt, AnthropicContentBlock{Type: "text", Text: seq[0].Prefix})
	}

	for i, part := range seq {
		rebuilt = append(rebuilt, AnthropicContentBlock{
			Type:  "tool_use",
			ID:    part.Call.ID,
			Name:  part.Call.Name,
			Input: decodeToolCallArgs(part.Call.ArgsJSON),
		})
		if i+1 < len(seq) {
			rebuilt = append(rebuilt, AnthropicContentBlock{Type: "text", Text: seq[i+1].Prefix})
		}
	}

	if suffix != "" {
		rebuilt = append(rebuilt, AnthropicContentBlock{Type: "text", Text: suffix})
	}
	if len(rebuilt) == 0 {
		rebuilt = append(rebuilt, AnthropicContentBlock{Type: "text", Text: contentStr})
	}

	return rebuilt, true
}

func validToolDefinitions(req *AnthropicRequest) []core.ToolDefinition {
	if req == nil || len(req.Tools) == 0 {
		return nil
	}

	validTools := make([]core.ToolDefinition, len(req.Tools))
	for i, tool := range req.Tools {
		validTools[i] = core.ToolDefinition{Name: tool.Name}
	}
	return validTools
}

func decodeToolCallArgs(raw json.RawMessage) map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}

	var input map[string]interface{}
	if err := json.Unmarshal(raw, &input); err != nil {
		return map[string]interface{}{"raw_arguments": string(raw)}
	}
	return input
}

func parseArgoContentBlock(blockMap map[string]interface{}) (AnthropicContentBlock, bool) {
	blockType, ok := blockMap["type"].(string)
	if !ok || blockType == "" {
		return AnthropicContentBlock{}, false
	}

	block := AnthropicContentBlock{Type: blockType}
	switch block.Type {
	case "text":
		block.Text, _ = blockMap["text"].(string)
	case "image":
		block.Source, _ = blockMap["source"].(map[string]interface{})
	case "audio", "input_audio":
		block.InputAudio, _ = blockMap["input_audio"].(map[string]interface{})
	case "file":
		block.File, _ = blockMap["file"].(map[string]interface{})
	case "tool_use":
		block.ID, _ = blockMap["id"].(string)
		block.Name, _ = blockMap["name"].(string)
		block.Input, _ = blockMap["input"].(map[string]interface{})
	}

	return block, true
}

func extractArgoToolUseBlocks(rawToolCalls interface{}) []AnthropicContentBlock {
	toolCalls, ok := normalizeArgoToolCalls(rawToolCalls)
	if !ok {
		return nil
	}

	content := make([]AnthropicContentBlock, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		block, ok := parseArgoToolCall(toolCall)
		if !ok {
			continue
		}
		content = append(content, block)
	}
	return content
}

func normalizeArgoToolCalls(rawToolCalls interface{}) ([]map[string]interface{}, bool) {
	switch value := rawToolCalls.(type) {
	case []interface{}:
		toolCalls := make([]map[string]interface{}, 0, len(value))
		for _, item := range value {
			toolCall, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			toolCalls = append(toolCalls, toolCall)
		}
		return toolCalls, true
	case map[string]interface{}:
		return []map[string]interface{}{value}, true
	default:
		return nil, false
	}
}

func parseArgoToolCall(toolCall map[string]interface{}) (AnthropicContentBlock, bool) {
	if fn, ok := toolCall["function"].(map[string]interface{}); ok {
		name, _ := fn["name"].(string)
		args, _ := fn["arguments"].(string)
		id, _ := toolCall["id"].(string)
		if id == "" {
			id = generateToolUseID()
		}
		return AnthropicContentBlock{
			Type:  "tool_use",
			ID:    id,
			Name:  name,
			Input: decodeToolCallArgs(json.RawMessage(args)),
		}, true
	}

	if input, ok := toolCall["input"].(map[string]interface{}); ok {
		id, _ := toolCall["id"].(string)
		if id == "" {
			id = generateToolUseID()
		}
		name, _ := toolCall["name"].(string)
		return AnthropicContentBlock{
			Type:  "tool_use",
			ID:    id,
			Name:  name,
			Input: input,
		}, true
	}

	if args, ok := toolCall["args"].(map[string]interface{}); ok {
		name, _ := toolCall["name"].(string)
		return AnthropicContentBlock{
			Type:  "tool_use",
			ID:    generateToolUseID(),
			Name:  name,
			Input: args,
		}, true
	}

	return AnthropicContentBlock{}, false
}
