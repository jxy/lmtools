package core

func parseOpenAIResponse(data []byte, isEmbed bool) (Response, error) {
	text, toolCalls, err := parseOpenAIResponseWithTools(data, isEmbed)
	return Response{Text: text, ToolCalls: toolCalls}, err
}

func parseAnthropicResponse(data []byte, isEmbed bool) (Response, error) {
	text, toolCalls, err := parseAnthropicResponseWithTools(data, isEmbed)
	return Response{Text: text, ToolCalls: toolCalls}, err
}

func parseArgoResponse(data []byte, isEmbed bool) (Response, error) {
	text, toolCalls, err := parseArgoResponseWithTools(data, isEmbed)
	return Response{Text: text, ToolCalls: toolCalls}, err
}
