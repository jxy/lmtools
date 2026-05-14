package core

import (
	"encoding/json"
	"fmt"
	"lmtools/internal/constants"
)

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
	Effort     string
	JSONMode   bool
	JSONSchema json.RawMessage
}

// PrepareRequestPayload applies provider-specific request normalization,
// including out-of-band system prompt handling and tool conversion.
func PrepareRequestPayload(provider, model string, typedMessages []TypedMessage, system string, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (PreparedRequestPayload, error) {
	return PrepareRequestPayloadWithSystemExplicit(provider, model, typedMessages, system, system != "", toolDefs, toolChoice, stream)
}

// PrepareRequestPayloadWithSystemExplicit applies provider-specific request
// normalization while distinguishing explicit caller system prompts from
// default/effective prompts. Explicit prompts override a leading session system
// message; non-explicit prompts yield to the leading session system message.
func PrepareRequestPayloadWithSystemExplicit(provider, model string, typedMessages []TypedMessage, system string, systemExplicit bool, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (PreparedRequestPayload, error) {
	spec, err := requireProviderRequestSpec(provider)
	if err != nil {
		return PreparedRequestPayload{}, err
	}
	if err := ValidateMessagesForProvider(provider, typedMessages); err != nil {
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
		if !systemExplicit && inlineSystem != "" {
			payload.System = inlineSystem
		}
		payload.Messages = rest
	} else {
		payload.Messages = normalizeInlineSystemMessages(typedMessages, payload.System, systemExplicit)
	}

	if len(toolDefs) > 0 {
		converted := spec.convertToolsForRequest(toolDefs, toolChoice)
		payload.Tools = converted.Tools
		payload.ToolChoice = converted.ToolChoice
	}

	return payload, nil
}

func normalizeInlineSystemMessages(messages []TypedMessage, system string, systemExplicit bool) []TypedMessage {
	if systemExplicit {
		_, rest := splitSystem(messages)
		return PrependSystemMessage(rest, system)
	}
	if inlineSystem, _ := splitSystem(messages); inlineSystem != "" {
		return messages
	}
	return PrependSystemMessage(messages, system)
}

// ValidateMessagesForProvider rejects message block shapes that the target
// provider cannot render.
func ValidateMessagesForProvider(provider string, typedMessages []TypedMessage) error {
	normalized := constants.NormalizeProvider(provider)
	if normalized != constants.ProviderAnthropic && normalized != constants.ProviderArgo {
		return nil
	}

	for _, message := range typedMessages {
		for _, block := range message.Blocks {
			if _, ok := block.(AudioBlock); ok {
				return fmt.Errorf("%s provider does not support audio input blocks", normalized)
			}
		}
	}

	return nil
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
