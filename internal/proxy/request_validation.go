package proxy

import (
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/providers"
	"strings"
)

func validateParsedAnthropicRequest(req *AnthropicRequest) error {
	if req.Model == "" {
		return fmt.Errorf("model is required")
	}
	if len(req.Messages) == 0 {
		return fmt.Errorf("messages array cannot be empty")
	}
	return nil
}

func validateParsedOpenAIRequest(req *OpenAIRequest) error {
	if req.Model == "" {
		return fmt.Errorf("model is required")
	}
	if len(req.Messages) == 0 {
		return fmt.Errorf("messages array cannot be empty")
	}
	return nil
}

func validateAnthropicRequestForProvider(req *AnthropicRequest, provider string) error {
	if err := validateAnthropicOpus47Features(req, provider); err != nil {
		return err
	}

	normalized := constants.NormalizeProvider(provider)
	if normalized == constants.ProviderArgo {
		normalized = providers.DetermineArgoModelProvider(req.Model)
	}
	if normalized == constants.ProviderAnthropic {
		return nil
	}

	if len(req.Metadata) > 0 {
		return unsupportedProviderField("metadata", normalized)
	}
	if req.TopK != nil && (normalized == constants.ProviderOpenAI || normalized == constants.ProviderArgo) {
		return unsupportedProviderField("top_k", normalized)
	}
	if req.ToolChoice != nil && normalized == constants.ProviderGoogle {
		return unsupportedProviderField("tool_choice", normalized)
	}
	if req.Thinking != nil && normalized == constants.ProviderGoogle {
		return unsupportedProviderField("thinking", normalized)
	}
	if req.OutputConfig != nil {
		return unsupportedProviderField("output_config", normalized)
	}

	return nil
}

func validateAnthropicOpus47Features(req *AnthropicRequest, provider string) error {
	if req == nil {
		return nil
	}

	usesAdaptiveThinking := req.Thinking != nil && strings.EqualFold(req.Thinking.Type, "adaptive")
	usesThinkingDisplay := req.Thinking != nil && req.Thinking.Display != ""
	usesOutputConfig := req.OutputConfig != nil
	if !usesAdaptiveThinking && !usesThinkingDisplay && !usesOutputConfig {
		return nil
	}

	normalized := constants.NormalizeProvider(provider)
	if normalized != constants.ProviderAnthropic {
		return fmt.Errorf("anthropic Opus 4.7 thinking/output_config fields are only supported when proxying to provider %q", constants.ProviderAnthropic)
	}
	if !isAnthropicOpus47Model(req.Model) {
		return fmt.Errorf("anthropic Opus 4.7 thinking/output_config fields require model %q", "claude-opus-4-7")
	}
	if usesAdaptiveThinking && req.Thinking.BudgetTokens != 0 {
		return fmt.Errorf("thinking.budget_tokens is not valid with thinking.type=%q", "adaptive")
	}
	if req.OutputConfig != nil && !isValidAnthropicEffort(req.OutputConfig.Effort) {
		return fmt.Errorf("output_config.effort must be one of low, medium, high, xhigh, max")
	}
	return nil
}

func isAnthropicOpus47Model(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return normalized == "claude-opus-4-7" || strings.HasPrefix(normalized, "claude-opus-4-7-")
}

func isValidAnthropicEffort(effort string) bool {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func validateOpenAIRequestForProvider(req *OpenAIRequest, provider string) error {
	normalized := constants.NormalizeProvider(provider)
	if normalized == constants.ProviderArgo {
		normalized = providers.DetermineArgoModelProvider(req.Model)
	}
	if normalized == constants.ProviderOpenAI {
		return nil
	}

	if req.N != nil {
		return unsupportedProviderField("n", normalized)
	}
	if req.PresencePenalty != nil {
		return unsupportedProviderField("presence_penalty", normalized)
	}
	if req.FrequencyPenalty != nil {
		return unsupportedProviderField("frequency_penalty", normalized)
	}
	if len(req.LogitBias) > 0 {
		return unsupportedProviderField("logit_bias", normalized)
	}
	if req.User != "" {
		return unsupportedProviderField("user", normalized)
	}
	if req.ResponseFormat != nil {
		return unsupportedProviderField("response_format", normalized)
	}
	if req.StreamOptions != nil {
		return unsupportedProviderField("stream_options", normalized)
	}
	for _, message := range req.Messages {
		if message.Name != "" {
			return unsupportedProviderField("messages[].name", normalized)
		}
	}

	return nil
}

func unsupportedProviderField(field, provider string) error {
	return fmt.Errorf("field %q is not supported when proxying to provider %q", field, provider)
}
