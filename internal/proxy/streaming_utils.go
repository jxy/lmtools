package proxy

// streaming_utils.go - Centralized utilities for streaming and format conversions

// MapStopReasonToOpenAIFinishReason maps Anthropic stop reasons to OpenAI finish reasons
func MapStopReasonToOpenAIFinishReason(stopReason string) string {
	switch stopReason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return stopReason
	}
}

// MapOpenAIFinishReasonToStopReason maps OpenAI finish reasons to Anthropic stop reasons
func MapOpenAIFinishReasonToStopReason(finishReason string) string {
	switch finishReason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		return finishReason
	}
}

// combineOpenAIFinishReason keeps the most severe finish reason seen across choices.
func combineOpenAIFinishReason(current, next string) string {
	if openAIFinishReasonPriority(next) > openAIFinishReasonPriority(current) {
		return next
	}
	return current
}

func openAIFinishReasonPriority(reason string) int {
	switch reason {
	case "":
		return 0
	case "stop":
		return 1
	case "tool_calls", "function_call":
		return 2
	case "length", "max_tokens":
		return 3
	case "content_filter":
		return 4
	default:
		return 2
	}
}

// combineAnthropicStopReason keeps the most severe stop reason seen across choices.
func combineAnthropicStopReason(current, next string) string {
	if anthropicStopReasonPriority(next) > anthropicStopReasonPriority(current) {
		return next
	}
	return current
}

func anthropicStopReasonPriority(reason string) int {
	switch reason {
	case "":
		return 0
	case "end_turn":
		return 1
	case "tool_use":
		return 2
	case "max_tokens":
		return 3
	case "content_filter":
		return 4
	default:
		return 2
	}
}

// AnthropicUsageToOpenAI converts Anthropic usage to OpenAI format
func AnthropicUsageToOpenAI(usage *AnthropicUsage) *OpenAIUsage {
	if usage == nil {
		return nil
	}
	openAIUsage := &OpenAIUsage{
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
		TotalTokens:      usage.InputTokens + usage.OutputTokens,
	}
	if usage.CacheReadInputTokens > 0 {
		openAIUsage.PromptTokensDetails = &OpenAITokenDetails{
			CachedTokens: usage.CacheReadInputTokens,
		}
	}
	return openAIUsage
}

// OpenAIUsageToAnthropic converts OpenAI usage to Anthropic format
func OpenAIUsageToAnthropic(usage *OpenAIUsage) *AnthropicUsage {
	if usage == nil {
		return nil
	}
	anthropicUsage := &AnthropicUsage{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
	}
	if usage.PromptTokensDetails != nil {
		anthropicUsage.CacheReadInputTokens = usage.PromptTokensDetails.CachedTokens
	}
	return anthropicUsage
}

// OpenAIUsageFromCounts creates OpenAI usage from token counts
func OpenAIUsageFromCounts(promptTokens, completionTokens int) *OpenAIUsage {
	return &OpenAIUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}
}

// mapGoogleFinishReason maps Google finish reasons to OpenAI format
func mapGoogleFinishReason(finishReason string) string {
	switch finishReason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY":
		return "content_filter"
	default:
		return "stop"
	}
}
