package proxy

import (
	"context"
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

	if req.System != nil {
		if _, err := extractSystemContent(req.System); err != nil {
			return nil, fmt.Errorf("failed to extract system content: %w", err)
		}
	}

	typed := AnthropicRequestToTyped(req)
	return TypedToGoogleRequest(typed, req.Model, req.TopK)
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
