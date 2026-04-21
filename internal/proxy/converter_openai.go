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

	if req.System != nil {
		if _, err := extractSystemContent(req.System); err != nil {
			return nil, fmt.Errorf("failed to extract system content: %w", err)
		}
	}

	logDroppedThinkingBlocksForOpenAI(ctx, req.Messages)

	typed := AnthropicRequestToTyped(req)

	// Handle thinking field for OpenAI models
	if req.Thinking != nil && req.Thinking.Type == "enabled" {
		modelLower := strings.ToLower(req.Model)
		if strings.HasPrefix(modelLower, "gpt") || strings.HasPrefix(modelLower, "o3") || strings.HasPrefix(modelLower, "o4") {
			// For GPT and O3/O4 models, convert to reasoning_effort
			typed.ReasoningEffort = "high"
			logger.From(ctx).Debugf("Converting thinking.budget_tokens=%d to reasoning_effort=high for model %s", req.Thinking.BudgetTokens, req.Model)
		}
	}

	return TypedToOpenAIRequest(typed, req.Model)
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
	}
	if resp.Usage != nil {
		anthResp.Usage = &AnthropicUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		}
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
	_ = ctx

	// Use the typed conversion path as the single source of truth
	typed := OpenAIRequestToTyped(req)
	return TypedToAnthropicRequest(typed, req.Model)
}

func logDroppedThinkingBlocksForOpenAI(ctx context.Context, messages []AnthropicMessage) {
	for _, msg := range messages {
		_, blocks, err := parseAnthropicMessageContent(msg.Content)
		if err != nil {
			continue
		}
		for _, block := range blocks {
			if block.Type != "thinking" {
				continue
			}
			droppedBlock := map[string]interface{}{
				"type":     "thinking",
				"thinking": block.Thinking,
			}
			logger.From(ctx).Debugf("Dropping thinking content block (not supported by OpenAI): %+v", droppedBlock)
		}
	}
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

	// Use typed converter to get OpenAI content and tool calls
	contentUnion, typedToolCalls := core.ConvertBlocksToOpenAIContentTyped(coreBlocks)

	// Build the message
	message := OpenAIMessage{
		Role: core.RoleAssistant,
	}

	message.Content = openAIContentUnionToInterface(contentUnion)

	message.ToolCalls = typedOpenAIToolCallsToProxy(typedToolCalls)

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
