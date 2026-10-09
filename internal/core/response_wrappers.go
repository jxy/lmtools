package core

func parseOpenAIResponse(data []byte, isEmbed bool) (Response, error) {
	return parseOpenAIResponseDetailed(data, isEmbed)
}

func parseAnthropicResponse(data []byte, isEmbed bool) (Response, error) {
	text, toolCalls, blocks, err := parseAnthropicResponseDetailed(data, isEmbed)
	return Response{Text: text, ToolCalls: toolCalls, Blocks: blocks}, err
}

func parseArgoResponse(data []byte, isEmbed bool) (Response, error) {
	text, toolCalls, err := parseArgoResponseWithTools(data, isEmbed)
	return Response{Text: text, ToolCalls: toolCalls, Blocks: responseBlocksFromParts(text, toolCalls, "")}, err
}
