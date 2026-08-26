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

func validateAnthropicRequestForProvider(req *AnthropicRequest, _ string) error {
	if req == nil {
		return nil
	}
	return validateAnthropicFeatureSet(req.Thinking, req.OutputConfig)
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
		return validateAnthropicFeatureSet(nil, outputConfig)
	}

	return nil
}

func validateAnthropicFeatureSet(thinking *AnthropicThinking, outputConfig *AnthropicOutputConfig) error {
	usesAdaptiveThinking := thinking != nil && strings.EqualFold(thinking.Type, "adaptive")
	if thinking == nil && outputConfig == nil {
		return nil
	}

	if thinking != nil {
		thinkingType := strings.ToLower(strings.TrimSpace(thinking.Type))
		switch thinkingType {
		case "enabled", "disabled", "adaptive":
		case "":
			return fmt.Errorf("thinking.type is required")
		default:
			return fmt.Errorf("thinking.type must be one of enabled, disabled, adaptive")
		}
		if usesAdaptiveThinking && thinking.BudgetTokens != 0 {
			return fmt.Errorf("thinking.budget_tokens is not valid with thinking.type=%q", "adaptive")
		}
		if thinking.Display != "" {
			display := strings.ToLower(strings.TrimSpace(thinking.Display))
			if display != "summarized" && display != "omitted" {
				return fmt.Errorf("thinking.display must be one of summarized, omitted")
			}
			if thinkingType == "disabled" {
				return fmt.Errorf("thinking.display is not valid with thinking.type=%q", "disabled")
			}
		}
	}
	if outputConfig != nil && !isValidAnthropicEffort(outputConfig.Effort) {
		return fmt.Errorf("output_config.effort must be one of low, medium, high, xhigh, max")
	}
	return nil
}
