package core

// PreparedRequestPayload captures the provider-normalized inputs required to
// render a provider-specific request.
type PreparedRequestPayload struct {
	Provider   string
	Model      string
	Messages   []TypedMessage
	System     string
	Tools      interface{}
	ToolChoice interface{}
	Stream     bool
}

// PrepareRequestPayload applies provider-specific request normalization,
// including out-of-band system prompt handling and tool conversion.
func PrepareRequestPayload(provider, model string, typedMessages []TypedMessage, system string, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (PreparedRequestPayload, error) {
	spec, err := requireProviderRequestSpec(provider)
	if err != nil {
		return PreparedRequestPayload{}, err
	}

	payload := PreparedRequestPayload{
		Provider: provider,
		Model:    model,
		Messages: typedMessages,
		System:   system,
		Stream:   stream,
	}

	if spec.usesOutOfBandSystemPrompt() {
		inlineSystem, rest := splitSystem(typedMessages)
		if payload.System == "" {
			payload.System = inlineSystem
		}
		payload.Messages = rest
	}

	if len(toolDefs) > 0 {
		converted := spec.convertToolsForRequest(toolDefs, toolChoice)
		payload.Tools = converted.Tools
		payload.ToolChoice = converted.ToolChoice
	}

	return payload, nil
}

// PrependSystemMessage adds a system message ahead of the provided messages
// when the target provider expects the system prompt inline.
func PrependSystemMessage(messages []TypedMessage, system string) []TypedMessage {
	if system == "" {
		return messages
	}
	if len(messages) > 0 && messages[0].Role == string(RoleSystem) {
		return messages
	}

	messagesWithSystem := make([]TypedMessage, 0, len(messages)+1)
	messagesWithSystem = append(messagesWithSystem, TypedMessage{
		Role: string(RoleSystem),
		Blocks: []Block{
			TextBlock{Text: system},
		},
	})
	messagesWithSystem = append(messagesWithSystem, messages...)
	return messagesWithSystem
}
