package proxy

import "lmtools/internal/core"

type openAIStreamToolKey struct {
	ChoiceIndex int
	ToolIndex   int
}

func openAIStreamToolKeyFromParsed(tc core.ParsedOpenAIStreamToolCall) openAIStreamToolKey {
	return openAIStreamToolKey{ChoiceIndex: tc.ChoiceIndex, ToolIndex: tc.Index}
}

func openAIStreamToolKeyLess(a, b openAIStreamToolKey) bool {
	if a.ChoiceIndex != b.ChoiceIndex {
		return a.ChoiceIndex < b.ChoiceIndex
	}
	return a.ToolIndex < b.ToolIndex
}
