package proxy

import (
	"lmtools/internal/constants"
	"lmtools/internal/core"
)

func prepareTypedRequestPayload(provider string, typed TypedRequest, ctx typedRenderContext) (core.PreparedRequestPayload, error) {
	messages := typed.Messages
	var system string
	switch provider {
	case constants.ProviderAnthropic, constants.ProviderGoogle:
		system, messages = prepareOutOfBandInstructionMessages(typed.Messages, typed.System, typed.Developer)
	case constants.ProviderOpenAI:
		// OpenAI carries instruction roles inline in the message array.
	default:
		system = combineInstructionText(typed.System, typed.Developer)
	}
	return core.PrepareRequestPayload(provider, ctx.Model, messages, system, typed.Tools, typed.ToolChoice, typed.Stream)
}

func proxyOpenAIToolsFromCore(raw interface{}) []OpenAITool {
	tools, ok := raw.([]core.OpenAITool)
	if !ok || len(tools) == 0 {
		return nil
	}

	proxyTools := make([]OpenAITool, 0, len(tools))
	for _, tool := range tools {
		parameters := filterSchemaMetadata(rawJSONToInterface(tool.Function.Parameters))
		proxyTools = append(proxyTools, OpenAITool{
			Type: tool.Type,
			Function: OpenAIFunc{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  parameters,
			},
		})
	}
	return proxyTools
}

func proxyOpenAIToolChoiceFromCore(raw interface{}) interface{} {
	switch choice := raw.(type) {
	case nil:
		return nil
	case string:
		return choice
	case core.OpenAIToolChoice:
		if choice.Function == nil {
			return map[string]interface{}{"type": choice.Type}
		}
		return map[string]interface{}{
			"type": choice.Type,
			"function": map[string]string{
				"name": choice.Function.Name,
			},
		}
	case *core.OpenAIToolChoice:
		if choice == nil {
			return nil
		}
		return proxyOpenAIToolChoiceFromCore(*choice)
	default:
		return nil
	}
}

func proxyAnthropicToolsFromCore(raw interface{}) []AnthropicTool {
	tools, ok := raw.([]core.AnthropicTool)
	if !ok || len(tools) == 0 {
		return nil
	}

	proxyTools := make([]AnthropicTool, 0, len(tools))
	for _, tool := range tools {
		proxyTools = append(proxyTools, AnthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: rawJSONToInterface(tool.InputSchema),
		})
	}
	return proxyTools
}

func proxyAnthropicToolChoiceFromCore(raw interface{}) *AnthropicToolChoice {
	switch choice := raw.(type) {
	case nil:
		return nil
	case core.AnthropicToolChoice:
		return &AnthropicToolChoice{
			Type: choice.Type,
			Name: choice.Name,
		}
	case *core.AnthropicToolChoice:
		if choice == nil {
			return nil
		}
		return &AnthropicToolChoice{
			Type: choice.Type,
			Name: choice.Name,
		}
	default:
		return nil
	}
}

func proxyGoogleToolsFromCore(raw interface{}) []GoogleTool {
	tools, ok := raw.([]core.GoogleTool)
	if !ok || len(tools) == 0 {
		return nil
	}

	proxyTools := make([]GoogleTool, 0, len(tools))
	for _, tool := range tools {
		declarations := make([]GoogleFunction, 0, len(tool.FunctionDeclarations))
		for _, decl := range tool.FunctionDeclarations {
			declarations = append(declarations, GoogleFunction{
				Name:        decl.Name,
				Description: decl.Description,
				Parameters:  rawJSONToInterface(decl.Parameters),
			})
		}
		proxyTools = append(proxyTools, GoogleTool{
			FunctionDeclarations: declarations,
		})
	}
	return proxyTools
}
