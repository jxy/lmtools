package proxy

import (
	"fmt"
	"lmtools/internal/constants"
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
	normalized := constants.NormalizeProvider(provider)
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

	return nil
}

func validateOpenAIRequestForProvider(req *OpenAIRequest, provider string) error {
	normalized := constants.NormalizeProvider(provider)
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
