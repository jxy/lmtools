package proxy

import (
	"encoding/json"
	"lmtools/internal/core"
)

func TypedToOpenAIRequest(typed TypedRequest, model string) (*OpenAIRequest, error) {
	openAIReq := &OpenAIRequest{
		Model:           model,
		MaxTokens:       typed.MaxTokens,
		Temperature:     typed.Temperature,
		TopP:            typed.TopP,
		Stream:          typed.Stream,
		Stop:            typed.Stop,
		ReasoningEffort: typed.ReasoningEffort,
	}

	messages := typed.Messages
	if typed.System != "" {
		messagesWithSystem := make([]core.TypedMessage, 0, len(typed.Messages)+1)
		messagesWithSystem = append(messagesWithSystem, core.TypedMessage{
			Role: string(core.RoleSystem),
			Blocks: []core.Block{
				core.TextBlock{Text: typed.System},
			},
		})
		messagesWithSystem = append(messagesWithSystem, typed.Messages...)
		messages = messagesWithSystem
	}

	openAIReq.Messages = typedOpenAIMessagesToProxy(core.ToOpenAITyped(messages))
	openAIReq.Tools = toolDefinitionsToOpenAITools(typed.Tools)
	openAIReq.ToolChoice = toolChoiceToOpenAI(typed.ToolChoice)

	return openAIReq, nil
}

func TypedToAnthropicRequest(typed TypedRequest, model string) (*AnthropicRequest, error) {
	anthReq := &AnthropicRequest{
		Model:         model,
		Stream:        typed.Stream,
		StopSequences: typed.Stop,
		Temperature:   typed.Temperature,
		TopP:          typed.TopP,
		Tools:         toolDefinitionsToAnthropicTools(typed.Tools),
		ToolChoice:    toolChoiceToAnthropic(typed.ToolChoice),
	}

	if typed.MaxTokens != nil {
		anthReq.MaxTokens = *typed.MaxTokens
	}

	if typed.System != "" {
		systemJSON, err := json.Marshal(typed.System)
		if err != nil {
			return nil, err
		}
		anthReq.System = json.RawMessage(systemJSON)
	}

	messages, err := typedAnthropicMessagesToProxy(core.ToAnthropicTyped(typed.Messages))
	if err != nil {
		return nil, err
	}
	anthReq.Messages = messages

	return anthReq, nil
}
