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
	addOpenAIOutputFields(reqMap, payload)
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
	addAnthropicOutputFields(reqMap, payload)
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
	addGoogleOutputFields(reqMap, payload)
	return reqMap
}

func applyOutputOptionsFromConfig(payload *PreparedRequestPayload, cfg interface{}) {
	outputCfg, ok := cfg.(OutputConfig)
	if !ok {
		return
	}
	payload.Effort = strings.ToLower(strings.TrimSpace(outputCfg.GetEffort()))
	payload.JSONMode = outputCfg.IsJSONMode()
	if schema := outputCfg.GetJSONSchema(); len(schema) > 0 {
		payload.JSONSchema = append(payload.JSONSchema[:0], schema...)
	}
}

func addOpenAIOutputFields(reqMap map[string]interface{}, payload PreparedRequestPayload) {
	if effort := openAIReasoningEffort(payload.Effort); effort != "" {
		reqMap["reasoning_effort"] = effort
	}
	if responseFormat := openAIResponseFormat(payload); responseFormat != nil {
		reqMap["response_format"] = responseFormat
	}
}

func addAnthropicOutputFields(reqMap map[string]interface{}, payload PreparedRequestPayload) {
	outputConfig := make(map[string]interface{})
	if effort := anthropicOutputEffort(payload.Effort); effort != "" {
		outputConfig["effort"] = effort
	}
	if format := anthropicOutputFormat(payload); format != nil {
		outputConfig["format"] = format
	}
	if len(outputConfig) > 0 {
		reqMap["output_config"] = outputConfig
	}
}

func addGoogleOutputFields(reqMap map[string]interface{}, payload PreparedRequestPayload) {
	generationConfig := make(map[string]interface{})
	if existing, ok := reqMap["generationConfig"].(map[string]interface{}); ok {
		generationConfig = existing
	}

	if payload.JSONMode || len(payload.JSONSchema) > 0 {
		generationConfig["responseMimeType"] = "application/json"
	}
	if len(payload.JSONSchema) > 0 {
		generationConfig["responseJsonSchema"] = payload.JSONSchema
	}
	if thinkingConfig := googleThinkingConfig(payload.Model, payload.Effort); thinkingConfig != nil {
		generationConfig["thinkingConfig"] = thinkingConfig
	}
	if len(generationConfig) > 0 {
		reqMap["generationConfig"] = generationConfig
	}
}

func openAIReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(effort))
	case "max":
		return "xhigh"
	default:
		return ""
	}
}

func anthropicOutputEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal", "low":
		return "low"
	case "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return ""
	}
}

func openAIResponseFormat(payload PreparedRequestPayload) map[string]interface{} {
	if len(payload.JSONSchema) > 0 {
		return map[string]interface{}{
			"type": "json_schema",
			"json_schema": map[string]interface{}{
				"name":   "response",
				"schema": payload.JSONSchema,
			},
		}
	}
	if payload.JSONMode {
		return map[string]interface{}{"type": "json_object"}
	}
	return nil
}

func anthropicOutputFormat(payload PreparedRequestPayload) map[string]interface{} {
	if len(payload.JSONSchema) > 0 {
		return map[string]interface{}{
			"type":   "json_schema",
			"schema": payload.JSONSchema,
		}
	}
	if payload.JSONMode {
		return map[string]interface{}{"type": "json_object"}
	}
	return nil
}

func googleThinkingConfig(model, effort string) map[string]interface{} {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		return nil
	}

	modelLower := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(modelLower, "gemini-2.5") {
		budget, ok := googleThinkingBudget(effort)
		if !ok {
			return nil
		}
		return map[string]interface{}{"thinkingBudget": budget}
	}

	level := googleThinkingLevel(effort)
	if level == "" {
		return nil
	}
	return map[string]interface{}{"thinkingLevel": level}
}

func googleThinkingBudget(effort string) (int, bool) {
	switch effort {
	case "none":
		return 0, true
	case "minimal", "low":
		return 1024, true
	case "medium":
		return 8192, true
	case "high", "xhigh", "max":
		return 24576, true
	default:
		return 0, false
	}
}

func googleThinkingLevel(effort string) string {
	switch effort {
	case "minimal":
		return "minimal"
	case "low", "medium", "high":
		return effort
	case "xhigh", "max":
		return "high"
	default:
		return ""
	}
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
