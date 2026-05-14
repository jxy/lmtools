package core

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
)

// NOTE: Tool support for Google provider:
// - Direct Google provider (using Google API directly): SUPPORTS tools
// - Google models via Argo provider: SUPPORT tool-shaped responses routed through Google format
// This file implements Google-format tool parsing.

// googleToolCallCounter is used to generate unique IDs for Google tool calls
var googleToolCallCounter uint64

// generateGoogleToolCallID creates a unique ID for tool calls (used by Google which doesn't provide IDs)
func generateGoogleToolCallID() string {
	n := atomic.AddUint64(&googleToolCallCounter, 1)
	return fmt.Sprintf("call_%d", n)
}

func parseGoogleResponseDetailed(data []byte, isEmbed bool) (Response, error) {
	text, toolCalls, thoughtSignature, blocks, err := parseGoogleResponseWithMetadata(data, isEmbed)
	if err != nil {
		return Response{}, err
	}
	return Response{
		Text:             text,
		ToolCalls:        toolCalls,
		Blocks:           blocks,
		ThoughtSignature: thoughtSignature,
	}, nil
}

// parseGoogleResponseWithTools parses Google responses that may contain tool calls
// This parses Google-format responses for both direct Google usage and Argo-routed Google models.
func parseGoogleResponseWithTools(data []byte, isEmbed bool) (string, []ToolCall, error) {
	text, toolCalls, _, _, err := parseGoogleResponseWithMetadata(data, isEmbed)
	return text, toolCalls, err
}

func parseGoogleResponseWithMetadata(data []byte, isEmbed bool) (string, []ToolCall, string, []Block, error) {
	if isEmbed {
		// Google AI doesn't support embeddings through this interface
		return "", nil, "", nil, fmt.Errorf("google provider does not support embedding mode")
	}

	var resp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text             string `json:"text"`
					ThoughtSignature string `json:"thoughtSignature"`
					FunctionCall     *struct {
						Name string                 `json:"name"`
						Args map[string]interface{} `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
				Role string `json:"role"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}

	if err := json.Unmarshal(data, &resp); err != nil {
		return "", nil, "", nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if resp.Error != nil {
		return "", nil, "", nil, fmt.Errorf("API error: %s (code: %d, status: %s)",
			resp.Error.Message, resp.Error.Code, resp.Error.Status)
	}

	if len(resp.Candidates) == 0 {
		return "", nil, "", nil, fmt.Errorf("no candidates in response")
	}

	var text string
	var toolCalls []ToolCall
	var thoughtSignature string
	var blocks []Block

	for _, part := range resp.Candidates[0].Content.Parts {
		if part.ThoughtSignature != "" {
			blocks = append(blocks, ReasoningBlock{
				Provider:  "google",
				Type:      "thought_signature",
				Signature: part.ThoughtSignature,
			})
		}
		if part.Text != "" {
			text += part.Text
			blocks = append(blocks, TextBlock{Text: part.Text})
		}
		if part.ThoughtSignature != "" && part.FunctionCall == nil {
			thoughtSignature = part.ThoughtSignature
		}
		if part.FunctionCall != nil {
			// Convert args to JSON
			argsJSON, _ := json.Marshal(part.FunctionCall.Args)
			toolCalls = append(toolCalls, ToolCall{
				ID:               generateGoogleToolCallID(),
				Name:             part.FunctionCall.Name,
				Args:             argsJSON,
				ThoughtSignature: part.ThoughtSignature,
			})
			blocks = append(blocks, ToolUseBlock{
				ID:    toolCalls[len(toolCalls)-1].ID,
				Name:  part.FunctionCall.Name,
				Input: argsJSON,
			})
		}
	}

	return text, toolCalls, thoughtSignature, blocks, nil
}
