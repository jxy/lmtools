package proxy

import (
	"encoding/json"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/providers"
	"strings"
)

type typedRenderContext struct {
	Model                        string
	TopK                         *int
	User                         string
	OpenAIChatCompatibilityTools bool
}

type argoMessageRenderer func([]core.TypedMessage) ([]ArgoMessage, error)

var argoMessageRenderers = map[string]argoMessageRenderer{
	constants.ProviderOpenAI: func(messages []core.TypedMessage) ([]ArgoMessage, error) {
		rendered := typedMessagesToArgoOpenAI(normalizeTypedMessagesForOpenAIChat(messages))
		if err := validateArgoOpenAIChatToolSequence(rendered); err != nil {
			return nil, err
		}
		return rendered, nil
	},
	constants.ProviderAnthropic: typedMessagesToArgoAnthropic,
	constants.ProviderGoogle:    typedMessagesToArgoAnthropic,
}

func argoMessageRendererForModel(model string) argoMessageRenderer {
	provider := providers.DetermineArgoModelProvider(model)
	if renderer, ok := argoMessageRenderers[provider]; ok {
		return renderer
	}
	return argoMessageRenderers[constants.ProviderOpenAI]
}

func TypedToOpenAIRequest(typed TypedRequest, model string) (*OpenAIRequest, error) {
	return renderTypedToOpenAIRequest(typed, typedRenderContext{Model: model})
}

func renderTypedToOpenAIRequest(typed TypedRequest, ctx typedRenderContext) (*OpenAIRequest, error) {
	prepared, err := prepareTypedRequestPayload(constants.ProviderOpenAI, typed, ctx)
	if err != nil {
		return nil, err
	}
	if ctx.OpenAIChatCompatibilityTools {
		prepared.Messages = core.AdaptCustomToolBlocksForFunctionCompatibility(prepared.Messages)
		if len(typed.Tools) > 0 {
			converted := core.ConvertToolsForOpenAIChatCompatibility(typed.Tools, typed.ToolChoice)
			prepared.Tools = converted.Tools
			prepared.ToolChoice = converted.ToolChoice
		}
	}

	openAIReq := &OpenAIRequest{
		Model:           ctx.Model,
		Temperature:     typed.Temperature,
		TopP:            typed.TopP,
		Stream:          typed.Stream,
		Stop:            typed.Stop,
		ReasoningEffort: typed.ReasoningEffort,
		ResponseFormat:  typed.ResponseFormat,
		Metadata:        cloneStringInterfaceMap(typed.Metadata),
		ServiceTier:     serviceTierForOpenAI(typed.ServiceTier),
	}
	maxTokens := positiveIntPtr(typed.MaxTokens)
	if openAIModelUsesMaxCompletionTokens(ctx.Model) {
		openAIReq.MaxCompletionTokens = maxTokens
	} else {
		openAIReq.MaxTokens = maxTokens
	}

	messages := prependOpenAIInstructionMessages(prepared.Messages, typed.System, typed.Developer, ctx.Model)
	messages = normalizeTypedMessagesForOpenAIChat(messages)
	openAIReq.Messages = typedOpenAIMessagesToProxy(core.ToOpenAITyped(messages))
	if err := validateOpenAIChatToolSequence(openAIReq.Messages); err != nil {
		return nil, err
	}
	openAIReq.Tools = proxyOpenAIToolsFromCore(prepared.Tools)
	if typed.ToolChoice != nil {
		openAIReq.ToolChoice = proxyOpenAIToolChoiceFromCore(prepared.ToolChoice)
	}

	return openAIReq, nil
}

func openAIModelUsesMaxCompletionTokens(model string) bool {
	modelLower := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(modelLower, "gpt-5") ||
		strings.HasPrefix(modelLower, "o1") ||
		strings.HasPrefix(modelLower, "o3") ||
		strings.HasPrefix(modelLower, "o4")
}

func openAIModelUsesDeveloperRole(model string) bool {
	modelLower := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(modelLower, "gpt-5") ||
		strings.HasPrefix(modelLower, "o1") ||
		strings.HasPrefix(modelLower, "o3") ||
		strings.HasPrefix(modelLower, "o4")
}

func TypedToAnthropicRequest(typed TypedRequest, model string) (*AnthropicRequest, error) {
	return renderTypedToAnthropicRequest(typed, typedRenderContext{Model: model})
}

func renderTypedToAnthropicRequest(typed TypedRequest, ctx typedRenderContext) (*AnthropicRequest, error) {
	prepared, err := prepareTypedRequestPayload(constants.ProviderAnthropic, typed, ctx)
	if err != nil {
		return nil, err
	}

	outputConfig := mergeAnthropicOutputConfig(typed.OutputConfig, typed.ResponseFormat, typed.ReasoningEffort)
	temperature := typed.Temperature
	if anthropicModelRejectsTemperature(ctx.Model) {
		temperature = nil
	}
	anthReq := &AnthropicRequest{
		Model:         ctx.Model,
		Stream:        prepared.Stream,
		StopSequences: typed.Stop,
		Temperature:   temperature,
		Tools:         proxyAnthropicToolsFromCore(prepared.Tools),
		Thinking:      typed.Thinking,
		OutputConfig:  outputConfig,
		Metadata:      cloneStringInterfaceMap(typed.Metadata),
		ServiceTier:   serviceTierForAnthropic(typed.ServiceTier),
	}
	if typed.Temperature == nil {
		anthReq.TopP = typed.TopP
	}
	if typed.ToolChoice != nil {
		anthReq.ToolChoice = proxyAnthropicToolChoiceFromCore(prepared.ToolChoice)
	}

	if typed.MaxTokens != nil {
		anthReq.MaxTokens = *typed.MaxTokens
	}

	if prepared.System != "" {
		systemJSON, err := json.Marshal(prepared.System)
		if err != nil {
			return nil, err
		}
		anthReq.System = json.RawMessage(systemJSON)
	}

	messages, err := typedAnthropicMessagesToProxy(core.ToAnthropicTyped(prepared.Messages))
	if err != nil {
		return nil, err
	}
	anthReq.Messages = messages

	return anthReq, nil
}

func anthropicModelRejectsTemperature(model string) bool {
	return isAnthropicOpus47Model(model)
}

func TypedToGoogleRequest(typed TypedRequest, model string, topK *int) (*GoogleRequest, error) {
	return renderTypedToGoogleRequest(typed, typedRenderContext{Model: model, TopK: topK})
}

func renderTypedToGoogleRequest(typed TypedRequest, ctx typedRenderContext) (*GoogleRequest, error) {
	prepared, err := prepareTypedRequestPayload(constants.ProviderGoogle, typed, ctx)
	if err != nil {
		return nil, err
	}

	googleReq := &GoogleRequest{
		GenerationConfig: &GoogleGenerationConfig{
			Temperature:     typed.Temperature,
			TopP:            typed.TopP,
			MaxOutputTokens: typed.MaxTokens,
			StopSequences:   typed.Stop,
		},
	}
	applyResponseFormatToGoogleConfig(googleReq.GenerationConfig, typed.ResponseFormat)
	googleReq.GenerationConfig.ThinkingConfig = googleThinkingConfigForReasoning(ctx.Model, typed.ReasoningEffort)

	if ctx.TopK != nil {
		googleReq.GenerationConfig.TopK = ctx.TopK
	}

	googleReq.Contents = make([]GoogleContent, 0, len(prepared.Messages))
	if prepared.System != "" {
		googleReq.SystemInstruction = &GoogleSystemInstruction{
			Parts: []GooglePart{{Text: prepared.System}},
		}
	}
	googleReq.Contents = append(googleReq.Contents, typedGoogleMessagesToProxy(core.ToGoogleTyped(prepared.Messages))...)

	googleReq.Tools = proxyGoogleToolsFromCore(prepared.Tools)
	if len(googleReq.Tools) > 0 {
		googleReq.ToolConfig = googleToolConfigFromChoice(typed.ToolChoice)
	}

	return googleReq, nil
}

func TypedToArgoRequest(typed TypedRequest, model string, user string) (*ArgoChatRequest, error) {
	return renderTypedToArgoRequest(typed, typedRenderContext{Model: model, User: user})
}

func renderTypedToArgoRequest(typed TypedRequest, ctx typedRenderContext) (*ArgoChatRequest, error) {
	if err := core.ValidateMessagesForProvider(constants.ProviderArgo, typed.Messages); err != nil {
		return nil, err
	}

	argoReq := &ArgoChatRequest{
		User:            ctx.User,
		Model:           ctx.Model,
		Temperature:     typed.Temperature,
		TopP:            typed.TopP,
		Stop:            typed.Stop,
		ReasoningEffort: typed.ReasoningEffort,
		ResponseFormat:  typed.ResponseFormat,
		Metadata:        cloneStringInterfaceMap(typed.Metadata),
		ServiceTier:     typed.ServiceTier,
	}

	var typedMessages []core.TypedMessage
	if providers.DetermineArgoModelProvider(ctx.Model) == constants.ProviderOpenAI {
		typedMessages = prependOpenAIInstructionMessages(typed.Messages, typed.System, typed.Developer, ctx.Model)
		typedMessages = core.AdaptCustomToolBlocksForFunctionCompatibility(typedMessages)
		typedMessages = normalizeTypedMessagesForOpenAIChat(typedMessages)
	} else {
		system, messages := prepareOutOfBandInstructionMessages(typed.Messages, typed.System, typed.Developer)
		typedMessages = core.PrependSystemMessage(messages, system)
	}
	messages := make([]ArgoMessage, 0, len(typedMessages))
	renderMessages := argoMessageRendererForModel(ctx.Model)
	renderedMessages, err := renderMessages(typedMessages)
	if err != nil {
		return nil, err
	}
	messages = append(messages, renderedMessages...)
	argoReq.Messages = messages

	if len(typed.Tools) > 0 {
		converted := core.ConvertToolsForProvider(ctx.Model, typed.Tools, typed.ToolChoice)
		if providers.DetermineArgoModelProvider(ctx.Model) == constants.ProviderOpenAI {
			converted = core.ConvertToolsForOpenAIChatCompatibility(typed.Tools, typed.ToolChoice)
		}
		argoReq.Tools = converted.Tools
		argoReq.ToolChoice = converted.ToolChoice
	}

	return argoReq, nil
}
