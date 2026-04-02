package proxy

import "encoding/json"

// EstimateTokenCount estimates the number of tokens in a text string
// using a simple heuristic of dividing character count by 3.
//
// IMPORTANT: This function is ONLY needed for the Argo API provider,
// which does not return token counts in its responses. Other providers
// (OpenAI, Google, Anthropic) provide actual token counts in their
// responses and should use those instead of calling this function.
func EstimateTokenCount(text string) int {
	return len(text) / 3
}

// EstimateTokenCountFromChars estimates the number of tokens from character count
// using a simple heuristic of dividing character count by 3.
func EstimateTokenCountFromChars(charCount int) int {
	return charCount / 3
}

// EstimateRequestTokens estimates the total input tokens for an Anthropic request.
// This includes system message, conversation messages, and tool definitions.
func EstimateRequestTokens(req *AnthropicRequest) int {
	totalChars := 0

	if req.System != nil {
		var systemContent string
		if err := json.Unmarshal(req.System, &systemContent); err == nil {
			totalChars += len(systemContent)
		} else {
			totalChars += len(req.System)
		}
	}

	for _, msg := range req.Messages {
		var content string
		if err := json.Unmarshal(msg.Content, &content); err == nil {
			totalChars += len(content)
		} else {
			totalChars += len(msg.Content)
		}
		totalChars += len(string(msg.Role))
	}

	for _, tool := range req.Tools {
		totalChars += len(tool.Name) + len(tool.Description)
		if toolJSON, err := json.Marshal(tool.InputSchema); err == nil {
			totalChars += len(toolJSON)
		}
	}

	totalChars += 100
	return EstimateTokenCountFromChars(totalChars)
}
