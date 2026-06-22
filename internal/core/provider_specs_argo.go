package core

import (
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/providers"
	"net/http"
)

type argoChatRequestPlan struct {
	Model        string
	WireProvider string
	Payload      PreparedRequestPayload
	Endpoint     string
}

func buildArgoChatRequest(cfg RequestOptions, messages []TypedMessage, model string, system string, systemExplicit bool, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
	if err := ValidateMessagesForProvider(constants.ProviderArgo, messages); err != nil {
		return nil, nil, err
	}
	if model == "" {
		model = providers.DefaultChatModel(constants.ProviderArgo)
	}
	if cfg.ArgoLegacy {
		return buildLegacyArgoChatRequest(cfg, model, messages, system, systemExplicit, toolDefs, toolChoice, stream)
	}

	plan, err := newArgoChatRequestPlan(cfg, messages, model, system, systemExplicit, toolDefs, toolChoice, stream)
	if err != nil {
		return nil, nil, err
	}
	spec, err := requireProviderRequestSpec(plan.WireProvider)
	if err != nil {
		return nil, nil, err
	}

	body, err := json.Marshal(spec.RequestMap(plan.Payload))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal chat request: %w", err)
	}

	return buildProviderRequest(cfg, plan.Endpoint, body, plan.WireProvider, plan.Payload.Stream)
}

func newArgoChatRequestPlan(cfg RequestOptions, messages []TypedMessage, model string, system string, systemExplicit bool, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (argoChatRequestPlan, error) {
	if model == "" {
		model = providers.DefaultChatModel(constants.ProviderArgo)
	}

	wireProvider := providers.DetermineArgoModelProvider(model)
	payload, err := PrepareRequestPayloadWithSystemExplicit(wireProvider, model, messages, system, systemExplicit, toolDefs, toolChoice, stream)
	if err != nil {
		return argoChatRequestPlan{}, err
	}
	if wireProvider == constants.ProviderOpenAI {
		payload.Messages = AdaptCustomToolBlocksForFunctionCompatibility(payload.Messages)
		if len(toolDefs) > 0 {
			converted := ConvertToolsForOpenAIChatCompatibility(toolDefs, toolChoice)
			payload.Tools = converted.Tools
			payload.ToolChoice = converted.ToolChoice
		}
	}
	applyOutputOptionsFromConfig(&payload, cfg)

	plan := argoChatRequestPlan{
		Model:        model,
		WireProvider: wireProvider,
		Payload:      payload,
	}
	endpoint, err := providers.ResolveChatURLWithArgoOptions(constants.ProviderArgo, cfg.ProviderURL, cfg.Env, model, stream, false)
	if err != nil {
		return argoChatRequestPlan{}, err
	}
	plan.Endpoint = endpoint
	return plan, nil
}

// argoLegacyAnthropicMaxTokensDropThreshold mirrors the apiproxy legacy Argo
// behavior: non-streaming Claude routes (including streaming-with-tools, which
// fall back to the non-streaming endpoint) reject large max_tokens values.
const argoLegacyAnthropicMaxTokensDropThreshold = 21000

// setLegacyArgoAnthropicMaxTokens sets max_tokens for legacy Argo Claude requests,
// dropping the field when the non-streaming endpoint would reject it. The stream
// argument is the effective stream flag (false when tools are present, since those
// requests use the non-streaming endpoint).
func setLegacyArgoAnthropicMaxTokens(bodyMap map[string]interface{}, payload PreparedRequestPayload, stream bool) {
	maxTokens := effectiveAnthropicMaxTokens(payload)
	if !stream && maxTokens >= argoLegacyAnthropicMaxTokensDropThreshold {
		return
	}
	bodyMap["max_tokens"] = maxTokens
}

func buildLegacyArgoChatRequest(cfg RequestOptions, model string, messages []TypedMessage, system string, systemExplicit bool, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
	actualStream := stream && len(toolDefs) == 0
	endpoint, err := providers.ResolveChatURLWithArgoOptions(constants.ProviderArgo, cfg.ProviderURL, cfg.Env, model, actualStream, true)
	if err != nil {
		return nil, nil, err
	}

	requestMessages := normalizeInlineSystemMessages(messages, system, systemExplicit)
	if providers.DetermineArgoModelProvider(model) == constants.ProviderOpenAI {
		requestMessages = AdaptCustomToolBlocksForFunctionCompatibility(requestMessages)
	}
	bodyMap := map[string]interface{}{
		"user":     cfg.User,
		"model":    model,
		"messages": marshalArgoTypedMessages(model, requestMessages),
	}
	if len(toolDefs) > 0 {
		converted := ConvertToolsForProvider(model, toolDefs, toolChoice)
		if providers.DetermineArgoModelProvider(model) == constants.ProviderOpenAI {
			converted = ConvertToolsForOpenAIChatCompatibility(toolDefs, toolChoice)
		}
		if converted.Tools != nil {
			bodyMap["tools"] = converted.Tools
		}
		if converted.ToolChoice != nil {
			bodyMap["tool_choice"] = converted.ToolChoice
		}
	}
	payload := PreparedRequestPayload{Model: model}
	applyOutputOptionsFromConfig(&payload, cfg)
	if providers.DetermineArgoModelProvider(model) == constants.ProviderAnthropic {
		addAnthropicOutputFields(bodyMap, payload)
		setLegacyArgoAnthropicMaxTokens(bodyMap, payload, actualStream)
	} else {
		addOpenAIOutputFields(bodyMap, payload)
	}

	body, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal Argo legacy request: %w", err)
	}

	return buildProviderRequest(cfg, endpoint, body, constants.ProviderArgo, actualStream)
}
