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
	if err := validateOpenAIChatToolSequence(req.Messages); err != nil {
		return err
	}
	return nil
}

func validateParsedOpenAIResponsesRequest(req *OpenAIResponsesRequest) error {
	if req.Model == "" {
		return fmt.Errorf("model is required")
	}
	if req.Input == nil && req.PreviousResponseID == "" && req.Prompt == nil {
		return fmt.Errorf("input, prompt, or previous_response_id is required")
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

	return nil
}

func validateAnthropicOpus47Features(req *AnthropicRequest, provider string) error {
	if req == nil {
		return nil
	}

	if err := validateAnthropicFeatureSet(req.Thinking, req.OutputConfig); err != nil {
		return err
	}

	normalized := constants.NormalizeProvider(provider)
	if normalized != constants.ProviderAnthropic {
		return nil
	}
	return validateAnthropicFeatureSupportForModel(req.Model, req.Thinking, req.OutputConfig)
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

func validateOpenAIRequestForProvider(req *OpenAIRequest, provider, targetModel string) error {
	normalized := constants.NormalizeProvider(provider)
	model := strings.TrimSpace(targetModel)
	if model == "" {
		model = req.Model
	}
	if normalized == constants.ProviderArgo {
		normalized = providers.DetermineArgoModelProvider(model)
	}
	if normalized == constants.ProviderOpenAI {
		return nil
	}
	if normalized == constants.ProviderAnthropic {
		outputConfig := mergeAnthropicOutputConfig(nil, req.ResponseFormat, req.ReasoningEffort)
		return validateAnthropicFeatureSupportForModel(model, nil, outputConfig)
	}

	return nil
}

func validateAnthropicFeatureSet(thinking *AnthropicThinking, outputConfig *AnthropicOutputConfig) error {
	usesAdaptiveThinking := thinking != nil && strings.EqualFold(thinking.Type, "adaptive")
	usesThinkingDisplay := thinking != nil && thinking.Display != ""
	usesOutputConfig := outputConfig != nil
	if !usesAdaptiveThinking && !usesThinkingDisplay && !usesOutputConfig {
		return nil
	}

	if usesAdaptiveThinking && thinking.BudgetTokens != 0 {
		return fmt.Errorf("thinking.budget_tokens is not valid with thinking.type=%q", "adaptive")
	}
	if outputConfig != nil && !isValidAnthropicEffort(outputConfig.Effort) {
		return fmt.Errorf("output_config.effort must be one of low, medium, high, xhigh, max")
	}
	return nil
}

func validateAnthropicFeatureSupportForModel(model string, thinking *AnthropicThinking, outputConfig *AnthropicOutputConfig) error {
	if err := validateAnthropicFeatureSet(thinking, outputConfig); err != nil {
		return err
	}
	if !anthropicUsesOpus47Features(thinking, outputConfig) {
		return nil
	}
	if !isAnthropicOpus47Model(model) {
		return fmt.Errorf("anthropic Opus 4.7 thinking/output_config fields require model %q", "claude-opus-4-7")
	}
	return nil
}

func anthropicUsesOpus47Features(thinking *AnthropicThinking, outputConfig *AnthropicOutputConfig) bool {
	usesAdaptiveThinking := thinking != nil && strings.EqualFold(thinking.Type, "adaptive")
	usesThinkingDisplay := thinking != nil && thinking.Display != ""
	return usesAdaptiveThinking || usesThinkingDisplay || outputConfig != nil
}
