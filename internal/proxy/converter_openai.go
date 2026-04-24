package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"strings"
	"time"
)

// ConvertAnthropicToOpenAI converts an Anthropic request to OpenAI format
// ARCHITECTURAL NOTE: This conversion goes through TypedRequest as the
// canonical intermediate representation to ensure consistency.
// Flow: Anthropic -> TypedRequest -> OpenAI
func (c *Converter) ConvertAnthropicToOpenAI(ctx context.Context, req *AnthropicRequest) (*OpenAIRequest, error) {
	warnAnthropicRequestDropsForOpenAI(ctx, req)

	if req.System != nil {
		if _, err := extractSystemContent(req.System); err != nil {
			return nil, fmt.Errorf("failed to extract system content: %w", err)
		}
	}

	typed := AnthropicRequestToTyped(req)
	if req.OutputConfig != nil {
		if effort := anthropicEffortToOpenAIReasoningEffort(req.OutputConfig.Effort); effort != "" {
			typed.ReasoningEffort = effort
		}
	}

	// Handle thinking field for OpenAI models
	if typed.ReasoningEffort == "" && req.Thinking != nil && req.Thinking.Type == "enabled" {
		if openAIModelUsesDeveloperRole(req.Model) || strings.HasPrefix(strings.ToLower(req.Model), "gpt") {
			// For GPT and O3/O4 models, convert to reasoning_effort
			typed.ReasoningEffort = "high"
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
		ID:          resp.ID,
		Type:        "message",
		Model:       originalModel,
		Role:        core.RoleAssistant,
		ServiceTier: resp.ServiceTier,
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
	if choice.Message.Refusal != nil && *choice.Message.Refusal != "" {
		content = append(content, AnthropicContentBlock{
			Type: "text",
			Text: *choice.Message.Refusal,
		})
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
	warnOpenAIRequestDropsForAnthropic(ctx, req)

	typed, err := OpenAIRequestToTypedStrict(req)
	if err != nil {
		return nil, err
	}
	if req.User != "" {
		if typed.Metadata == nil {
			typed.Metadata = map[string]interface{}{}
		}
		typed.Metadata["user_id"] = req.User
	} else if req.SafetyIdentifier != "" {
		if typed.Metadata == nil {
			typed.Metadata = map[string]interface{}{}
		}
		typed.Metadata["user_id"] = req.SafetyIdentifier
	}
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		if typed.Metadata == nil {
			typed.Metadata = map[string]interface{}{}
		}
		typed.Metadata[constants.IncludeUsageKey] = true
	}
	return TypedToAnthropicRequest(typed, req.Model)
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
		ID:          responseID,
		Object:      "chat.completion",
		Created:     time.Now().Unix(),
		Model:       originalModel,
		ServiceTier: resp.ServiceTier,
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
