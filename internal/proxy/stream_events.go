package proxy

// Stream event type constants for SSE (Server-Sent Events) streaming.
// These constants standardize event names across all streaming handlers
// and prevent typos or inconsistencies in event naming.

// Anthropic streaming event types
const (
	// EventMessageStart signals the beginning of a message stream
	EventMessageStart = "message_start"

	// EventContentBlockStart signals the beginning of a content block
	EventContentBlockStart = "content_block_start"

	// EventContentBlockDelta contains incremental content updates
	EventContentBlockDelta = "content_block_delta"

	// EventContentBlockStop signals the end of a content block
	EventContentBlockStop = "content_block_stop"

	// EventMessageDelta contains message-level updates (e.g., stop_reason, usage)
	EventMessageDelta = "message_delta"

	// EventMessageStop signals the end of a message stream
	EventMessageStop = "message_stop"

	// EventPing is a keep-alive signal
	EventPing = "ping"

	// EventError indicates an error occurred during streaming
	EventError = "error"
)

// OpenAI streaming markers
const (
	// OpenAIDoneMarker signals the end of an OpenAI stream
	OpenAIDoneMarker = "[DONE]"
)

// Factory functions for creating typed event payloads

// NewTextDelta creates a text delta event payload
func NewTextDelta(index int, text string) ContentBlockDeltaEvent {
	return ContentBlockDeltaEvent{
		Type:  EventContentBlockDelta,
		Index: index,
		Delta: DeltaContent{
			Type: "text_delta",
			Text: text,
		},
	}
}

// NewToolInputDelta creates a tool input delta event payload
func NewToolInputDelta(index int, partialJSON string) ContentBlockDeltaEvent {
	pj := partialJSON
	return ContentBlockDeltaEvent{
		Type:  EventContentBlockDelta,
		Index: index,
		Delta: DeltaContent{
			Type:        "input_json_delta",
			PartialJSON: &pj,
		},
	}
}

// NewContentBlockStart creates a content block start event payload
func NewContentBlockStart(index int, blockType string) ContentBlockStartEvent {
	cb := AnthropicContentBlock{Type: blockType}
	if blockType == "text" {
		cb.Text = ""
	}
	return ContentBlockStartEvent{
		Type:         EventContentBlockStart,
		Index:        index,
		ContentBlock: cb,
	}
}

// NewToolUseStart creates a tool use start event payload
func NewToolUseStart(index int, toolID, name string) ContentBlockStartEvent {
	return ContentBlockStartEvent{
		Type:  EventContentBlockStart,
		Index: index,
		ContentBlock: AnthropicContentBlock{
			Type:  "tool_use",
			ID:    toolID,
			Name:  name,
			Input: make(map[string]interface{}), // Force empty object
		},
	}
}

// NewContentBlockStop creates a content block stop event payload
func NewContentBlockStop(index int) ContentBlockStopEvent {
	return ContentBlockStopEvent{
		Type:  EventContentBlockStop,
		Index: index,
	}
}

// NewMessageDelta creates a message delta event payload
func NewMessageDelta(stopReason string, usage *AnthropicUsage) MessageDeltaEvent {
	return MessageDeltaEvent{
		Type: EventMessageDelta,
		Delta: MessageDelta{
			StopReason:   stopReason,
			StopSequence: "",
		},
		Usage: usage,
	}
}

// NewMessageStop creates a message stop event payload
func NewMessageStop() MessageStopEvent {
	return MessageStopEvent{
		Type: EventMessageStop,
	}
}

// NewMessageStart creates a message_start event payload
func NewMessageStart(id, model string, inputTokens, outputTokens int) MessageStartEvent {
	return MessageStartEvent{
		Type: EventMessageStart,
		Message: AnthropicResponse{
			ID:         id,
			Type:       "message",
			Role:       "assistant",
			Model:      model,
			Content:    []AnthropicContentBlock{},
			StopReason: "",
			Usage: &AnthropicUsage{
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
			},
		},
	}
}

// NewPing creates a ping event payload
func NewPing() PingEvent {
	return PingEvent{
		Type: EventPing,
	}
}

// NewError creates an error event payload
func NewError(message string) ErrorEvent {
	return ErrorEvent{
		Type: EventError,
		Error: ErrorInfo{
			Type:    EventError,
			Message: message,
		},
	}
}
