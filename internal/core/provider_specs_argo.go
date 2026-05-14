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
	Legacy       bool
}

func buildArgoChatRequest(cfg ChatRequestConfig, messages []TypedMessage, model string, system string, systemExplicit bool, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
	if err := ValidateMessagesForProvider(constants.ProviderArgo, messages); err != nil {
		return nil, nil, err
	}
	plan, err := newArgoChatRequestPlan(cfg, messages, model, system, systemExplicit, toolDefs, toolChoice, stream)
	if err != nil {
		return nil, nil, err
	}
	if plan.Legacy {
		return buildLegacyArgoChatRequest(cfg, plan.Model, messages, system, systemExplicit, toolDefs, toolChoice, stream)
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

func newArgoChatRequestPlan(cfg ChatRequestConfig, messages []TypedMessage, model string, system string, systemExplicit bool, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (argoChatRequestPlan, error) {
	if model == "" {
		model = GetDefaultChatModel(constants.ProviderArgo)
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
		Legacy:       isArgoLegacyMode(cfg),
	}
	endpoint, err := providers.ResolveChatURLWithArgoOptions(constants.ProviderArgo, cfg.GetProviderURL(), cfg.GetEnv(), model, stream, plan.Legacy)
	if err != nil {
		return argoChatRequestPlan{}, err
	}
	plan.Endpoint = endpoint
	return plan, nil
}

type argoLegacyConfig interface {
	IsArgoLegacy() bool
}

func isArgoLegacyMode(cfg interface{}) bool {
	v, ok := cfg.(argoLegacyConfig)
	return ok && v.IsArgoLegacy()
}

func buildLegacyArgoChatRequest(cfg ChatRequestConfig, model string, messages []TypedMessage, system string, systemExplicit bool, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
	actualStream := stream && len(toolDefs) == 0
	endpoint, err := providers.ResolveChatURLWithArgoOptions(constants.ProviderArgo, cfg.GetProviderURL(), cfg.GetEnv(), model, actualStream, true)
	if err != nil {
		return nil, nil, err
	}

	requestMessages := normalizeInlineSystemMessages(messages, system, systemExplicit)
	if providers.DetermineArgoModelProvider(model) == constants.ProviderOpenAI {
		requestMessages = AdaptCustomToolBlocksForFunctionCompatibility(requestMessages)
	}
	bodyMap := map[string]interface{}{
		"user":     cfg.GetUser(),
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
	addOpenAIOutputFields(bodyMap, payload)

	body, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal Argo legacy request: %w", err)
	}

	return buildProviderRequest(cfg, endpoint, body, constants.ProviderArgo, actualStream)
}
