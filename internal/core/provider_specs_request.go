package core

import (
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/providers"
	"strings"
)

func openAIRequestMap(payload PreparedRequestPayload) map[string]interface{} {
	reqMap := map[string]interface{}{
		"model":    payload.Model,
		"messages": marshalOpenAITypedMessages(payload.Messages, false),
		"stream":   payload.Stream,
	}
	addToolFields(reqMap, payload)
	return reqMap
}

func anthropicRequestMap(payload PreparedRequestPayload) map[string]interface{} {
	reqMap := map[string]interface{}{
		"model":    payload.Model,
		"messages": marshalAnthropicTypedMessages(payload.Messages, false),
		"stream":   payload.Stream,
	}
	if payload.System != "" {
		reqMap["system"] = payload.System
	}
	addToolFields(reqMap, payload)
	return reqMap
}

func googleRequestMap(payload PreparedRequestPayload) map[string]interface{} {
	reqMap := map[string]interface{}{
		"contents": marshalGoogleTypedMessages(payload.Messages, false),
	}
	if payload.System != "" {
		reqMap["systemInstruction"] = googleSystemInstruction(payload.System)
	}
	if payload.Tools != nil {
		reqMap["tools"] = payload.Tools
		reqMap["toolConfig"] = googleAutoToolConfig()
	}
	return reqMap
}

func googleSystemInstruction(system string) map[string]interface{} {
	return map[string]interface{}{
		"parts": []map[string]string{
			{"text": system},
		},
	}
}

func googleAutoToolConfig() map[string]interface{} {
	return map[string]interface{}{
		"functionCallConfig": map[string]interface{}{
			"mode": "AUTO",
		},
	}
}

func openAIChatURL(cfg ProviderConfig, _ string, _ bool) string {
	chatURL, err := providers.ResolveChatURL(constants.ProviderOpenAI, cfg.GetProviderURL(), "", "", false)
	if err == nil {
		return chatURL
	}
	url := cfg.GetProviderURL()
	if url == "" {
		url = "https://api.openai.com/v1"
	}
	return strings.TrimRight(url, "/") + "/chat/completions"
}

func anthropicChatURL(cfg ProviderConfig, _ string, _ bool) string {
	messagesURL, err := providers.ResolveChatURL(constants.ProviderAnthropic, cfg.GetProviderURL(), "", "", false)
	if err == nil {
		return messagesURL
	}
	messagesURL, _ = providers.AnthropicURLs(cfg.GetProviderURL())
	return messagesURL
}

func googleChatURL(cfg ProviderConfig, model string, stream bool) string {
	url, err := providers.ResolveChatURL(constants.ProviderGoogle, cfg.GetProviderURL(), "", model, stream)
	if err == nil {
		return url
	}

	action := "generateContent"
	if stream {
		action = "streamGenerateContent"
	}
	url = cfg.GetProviderURL()
	if url == "" {
		url = "https://generativelanguage.googleapis.com/v1beta"
	}
	return fmt.Sprintf("%s/models/%s:%s", strings.TrimRight(url, "/"), model, action)
}

func marshalOpenAITypedMessages(messages []TypedMessage, _ bool) []interface{} {
	return MarshalOpenAIMessagesForRequest(ToOpenAITyped(messages))
}

func marshalAnthropicTypedMessages(messages []TypedMessage, _ bool) []interface{} {
	return MarshalAnthropicMessagesForRequest(ToAnthropicTyped(messages))
}

func marshalGoogleTypedMessages(messages []TypedMessage, keepGoogleSystem bool) []interface{} {
	if keepGoogleSystem {
		return MarshalGoogleMessagesForRequest(ToGoogleForArgoTyped(messages))
	}
	return MarshalGoogleMessagesForRequest(ToGoogleTyped(messages))
}

func convertOpenAITools(tools []ToolDefinition, toolChoice *ToolChoice) ConvertedTools {
	openAITools := ConvertToolsToOpenAITyped(tools)
	if toolChoice != nil {
		if toolChoice.Type == "tool" && toolChoice.Name != "" {
			return ConvertedTools{
				Tools: openAITools,
				ToolChoice: OpenAIToolChoice{
					Type: "function",
					Function: &OpenAIToolChoiceFunction{
						Name: toolChoice.Name,
					},
				},
			}
		}
		return ConvertedTools{
			Tools:      openAITools,
			ToolChoice: toolChoice.Type,
		}
	}

	return ConvertedTools{Tools: openAITools, ToolChoice: "auto"}
}

func convertAnthropicTools(tools []ToolDefinition, toolChoice *ToolChoice) ConvertedTools {
	anthropicTools := ConvertToolsToAnthropicTyped(tools)
	if toolChoice != nil {
		return ConvertedTools{
			Tools: anthropicTools,
			ToolChoice: AnthropicToolChoice{
				Type: toolChoice.Type,
				Name: toolChoice.Name,
			},
		}
	}
	return ConvertedTools{
		Tools: anthropicTools,
		ToolChoice: AnthropicToolChoice{
			Type: "auto",
		},
	}
}

func convertGoogleTools(tools []ToolDefinition, _ *ToolChoice) ConvertedTools {
	return ConvertedTools{
		Tools: ConvertToolsToGoogleTyped(tools),
	}
}
