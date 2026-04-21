package proxy

import (
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/providers"
	"strings"
)

type typedRenderContext struct {
	Model string
	TopK  *int
	User  string
}

type (
	typedRequestRenderer func(TypedRequest, typedRenderContext) (interface{}, error)
	argoMessageRenderer  func([]core.TypedMessage) ([]ArgoMessage, error)
)

var argoMessageRenderers = map[string]argoMessageRenderer{
	constants.ProviderOpenAI: func(messages []core.TypedMessage) ([]ArgoMessage, error) {
		return typedMessagesToArgoOpenAI(messages), nil
	},
	constants.ProviderAnthropic: typedMessagesToArgoAnthropic,
	constants.ProviderGoogle:    typedMessagesToArgoAnthropic,
}

func renderTypedRequest(provider string, typed TypedRequest, ctx typedRenderContext) (interface{}, error) {
	capability, ok := proxyProviderCapabilityFor(provider)
	if !ok || capability.RenderTyped == nil {
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}
	return capability.RenderTyped(typed, ctx)
}

func argoMessageRendererForModel(model string) argoMessageRenderer {
	provider := providers.DetermineArgoModelProvider(model)
	if renderer, ok := argoMessageRenderers[provider]; ok {
		return renderer
	}
	return argoMessageRenderers[constants.ProviderOpenAI]
}

func TypedToOpenAIRequest(typed TypedRequest, model string) (*OpenAIRequest, error) {
	rendered, err := renderTypedRequest(constants.ProviderOpenAI, typed, typedRenderContext{Model: model})
	if err != nil {
		return nil, err
	}
	openAIReq, ok := rendered.(*OpenAIRequest)
	if !ok {
		return nil, fmt.Errorf("internal render type mismatch for provider: %s", constants.ProviderOpenAI)
	}
	return openAIReq, nil
}

func renderTypedToOpenAIRequest(typed TypedRequest, ctx typedRenderContext) (interface{}, error) {
	prepared, err := prepareTypedRequestPayload(constants.ProviderOpenAI, typed, ctx)
	if err != nil {
		return nil, err
	}

	openAIReq := &OpenAIRequest{
		Model:           ctx.Model,
		Temperature:     typed.Temperature,
		TopP:            typed.TopP,
		Stream:          typed.Stream,
		Stop:            typed.Stop,
		ReasoningEffort: typed.ReasoningEffort,
	}
	if openAIModelUsesMaxCompletionTokens(ctx.Model) {
		openAIReq.MaxCompletionTokens = typed.MaxTokens
	} else {
		openAIReq.MaxTokens = typed.MaxTokens
	}

	messages := core.PrependSystemMessage(prepared.Messages, prepared.System)
	openAIReq.Messages = typedOpenAIMessagesToProxy(core.ToOpenAITyped(messages))
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

func TypedToAnthropicRequest(typed TypedRequest, model string) (*AnthropicRequest, error) {
	rendered, err := renderTypedRequest(constants.ProviderAnthropic, typed, typedRenderContext{Model: model})
	if err != nil {
		return nil, err
	}
	anthReq, ok := rendered.(*AnthropicRequest)
	if !ok {
		return nil, fmt.Errorf("internal render type mismatch for provider: %s", constants.ProviderAnthropic)
	}
	return anthReq, nil
}

func renderTypedToAnthropicRequest(typed TypedRequest, ctx typedRenderContext) (interface{}, error) {
	prepared, err := prepareTypedRequestPayload(constants.ProviderAnthropic, typed, ctx)
	if err != nil {
		return nil, err
	}

	anthReq := &AnthropicRequest{
		Model:         ctx.Model,
		Stream:        prepared.Stream,
		StopSequences: typed.Stop,
		Temperature:   typed.Temperature,
		Tools:         proxyAnthropicToolsFromCore(prepared.Tools),
		Thinking:      typed.Thinking,
		OutputConfig:  typed.OutputConfig,
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

func TypedToGoogleRequest(typed TypedRequest, model string, topK *int) (*GoogleRequest, error) {
	rendered, err := renderTypedRequest(constants.ProviderGoogle, typed, typedRenderContext{Model: model, TopK: topK})
	if err != nil {
		return nil, err
	}
	googleReq, ok := rendered.(*GoogleRequest)
	if !ok {
		return nil, fmt.Errorf("internal render type mismatch for provider: %s", constants.ProviderGoogle)
	}
	return googleReq, nil
}

func renderTypedToGoogleRequest(typed TypedRequest, ctx typedRenderContext) (interface{}, error) {
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
		googleReq.ToolConfig = &GoogleToolConfig{
			FunctionCallingConfig: GoogleFunctionConfig{
				Mode: "AUTO",
			},
		}
	}

	return googleReq, nil
}

func TypedToArgoRequest(typed TypedRequest, model string, user string) (*ArgoChatRequest, error) {
	rendered, err := renderTypedRequest(constants.ProviderArgo, typed, typedRenderContext{Model: model, User: user})
	if err != nil {
		return nil, err
	}
	argoReq, ok := rendered.(*ArgoChatRequest)
	if !ok {
		return nil, fmt.Errorf("internal render type mismatch for provider: %s", constants.ProviderArgo)
	}
	return argoReq, nil
}

func renderTypedToArgoRequest(typed TypedRequest, ctx typedRenderContext) (interface{}, error) {
	for _, message := range typed.Messages {
		for _, block := range message.Blocks {
			if _, ok := block.(core.AudioBlock); ok {
				return nil, fmt.Errorf("argo provider does not support audio input blocks")
			}
		}
	}

	argoReq := &ArgoChatRequest{
		User:            ctx.User,
		Model:           ctx.Model,
		Temperature:     typed.Temperature,
		TopP:            typed.TopP,
		Stop:            typed.Stop,
		ReasoningEffort: typed.ReasoningEffort,
	}

	typedMessages := core.PrependSystemMessage(typed.Messages, typed.System)
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
		argoReq.Tools = converted.Tools
		argoReq.ToolChoice = converted.ToolChoice
	}

	return argoReq, nil
}
