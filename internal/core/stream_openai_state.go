package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type openAIStreamToolCallKey struct {
	choiceIndex int
	toolIndex   int
}

type openAIStreamBlock struct {
	reasoning     bool
	reasoningType string
	metadata      map[string]json.RawMessage
	thinkingParts []json.RawMessage
	text          strings.Builder
}

type openAIStreamChoiceState struct {
	finishReason string
	text         *openAIStreamBlock
	reasoning    *openAIStreamBlock
}

// OpenAIStreamState keeps Chat reasoning, answer text, and tool arguments in
// separate buffers. A finish reason completes a choice; [DONE] completes the
// stream, including compatibility backends that omit finish reasons.
type OpenAIStreamState struct {
	partialToolCalls     map[openAIStreamToolCallKey]*ToolCall
	choices              map[int]*openAIStreamChoiceState
	blocks               []*openAIStreamBlock
	toolBlocks           []Block
	streamEvents         []ResponseStreamEvent
	reasoningOpen        bool
	reasoningDisplayType string
	reasoningIncomplete  bool
	terminal             bool
	failed               bool
	toolsFinalized       bool
	usage                *Usage
}

func NewOpenAIStreamState() *OpenAIStreamState {
	return &OpenAIStreamState{
		partialToolCalls: make(map[openAIStreamToolCallKey]*ToolCall),
		choices:          make(map[int]*openAIStreamChoiceState),
	}
}

// ParseLine implements StreamState for OpenAI SSE format.
func (s *OpenAIStreamState) ParseLine(line string) (string, []ToolCall, bool, error) {
	data, ok := streamFieldValue(line, "data")
	if !ok || strings.TrimSpace(data) == "" {
		return "", nil, false, nil
	}
	if data == "[DONE]" {
		s.terminal = true
		s.closeReasoning(s.IncompleteReason() != "")
		calls, err := s.finalizeToolCalls()
		return "", calls, true, err
	}

	parsed, err := ParseOpenAIStreamChunk([]byte(data))
	if err != nil {
		if !isFatalStreamError(err) {
			s.failed = true
		}
		return "", nil, false, newFatalStreamError(fmt.Errorf("invalid OpenAI stream event: %w", err))
	}
	if parsed.Usage.InputTokens != nil || parsed.Usage.OutputTokens != nil || parsed.Usage.TotalTokens != nil {
		s.usage = s.usage.MergedWith(UsageFromPayload([]byte(data)))
	}

	var text strings.Builder
	for _, choice := range parsed.Choices {
		state := s.choiceState(choice.Index)
		blocks := choice.Blocks
		if choice.ReasoningContent != "" {
			blocks = append([]Block{openAIChatReasoningBlock(choice.ReasoningContent)}, blocks...)
		}
		for _, block := range blocks {
			switch b := block.(type) {
			case ReasoningBlock:
				if state.reasoning == nil || s.blocks[len(s.blocks)-1] != state.reasoning || state.reasoning.reasoningType != b.Type {
					state.text = nil
					state.reasoning = &openAIStreamBlock{reasoning: true, reasoningType: b.Type}
					s.blocks = append(s.blocks, state.reasoning)
				}
				if len(b.Raw) > 0 {
					if state.reasoning.metadata == nil {
						state.reasoning.metadata = make(map[string]json.RawMessage)
					}
					var fields map[string]json.RawMessage
					_ = json.Unmarshal(b.Raw, &fields)
					var parts []json.RawMessage
					_ = json.Unmarshal(fields["thinking"], &parts)
					state.reasoning.thinkingParts = append(state.reasoning.thinkingParts, parts...)
					for key, value := range fields {
						if key != "thinking" {
							state.reasoning.metadata[key] = value
						}
					}
				}
				if b.Text != "" {
					if s.reasoningOpen && s.reasoningDisplayType != b.Type {
						s.closeReasoningDisplay(false)
					}
					if !s.reasoningOpen {
						s.streamEvents = append(s.streamEvents, ResponseStreamEvent{Type: ResponseStreamReasoningStart, ReasoningType: b.Type})
						s.reasoningOpen = true
						s.reasoningDisplayType = b.Type
					}
					state.reasoning.text.WriteString(b.Text)
					s.streamEvents = append(s.streamEvents, ResponseStreamEvent{Type: ResponseStreamReasoningDelta, ReasoningType: b.Type, Text: b.Text})
				}
			case TextBlock:
				if b.Text == "" {
					continue
				}
				state.reasoning = nil
				s.closeReasoningDisplay(false)
				if state.text == nil || s.blocks[len(s.blocks)-1] != state.text {
					state.text = &openAIStreamBlock{}
					s.blocks = append(s.blocks, state.text)
				}
				state.text.text.WriteString(b.Text)
				text.WriteString(b.Text)
				s.streamEvents = append(s.streamEvents, ResponseStreamEvent{Type: ResponseStreamTextDelta, Text: b.Text})
			}
		}
		if len(choice.ToolCalls) > 0 {
			state.reasoning = nil
			s.closeReasoningDisplay(false)
		}
		for _, delta := range choice.ToolCalls {
			s.appendToolCall(delta)
		}
		if choice.FinishReason != "" {
			state.finishReason = choice.FinishReason
			if state.reasoning != nil && openAIChatIncompleteReason(choice.FinishReason) != "" {
				s.reasoningIncomplete = true
			}
			state.reasoning = nil
		}
	}
	// Text and tool transitions close the display in order above. A chunk
	// ending with more thinking leaves that section open for later deltas.
	if !s.hasActiveReasoning() {
		s.closeReasoningDisplay(false)
	}
	return text.String(), nil, false, nil
}

func (s *OpenAIStreamState) choiceState(index int) *openAIStreamChoiceState {
	if state := s.choices[index]; state != nil {
		return state
	}
	state := &openAIStreamChoiceState{}
	s.choices[index] = state
	return state
}

func (s *OpenAIStreamState) appendToolCall(delta ParsedOpenAIStreamToolCall) {
	key := openAIStreamToolCallKey{choiceIndex: delta.ChoiceIndex, toolIndex: delta.Index}
	partial := s.partialToolCalls[key]
	if partial == nil {
		partial = &ToolCall{}
		s.partialToolCalls[key] = partial
	}
	if delta.ID != "" {
		partial.ID = delta.ID
	}
	if delta.Type != "" {
		partial.Type = delta.Type
	}
	if delta.Name != "" {
		partial.Name = delta.Name
	}
	if partial.Type == "custom" {
		partial.Input += delta.Input
	} else if delta.Arguments != "" {
		partial.Args = append(partial.Args, delta.Arguments...)
	}
}

func (s *OpenAIStreamState) closeReasoningDisplay(incomplete bool) {
	if !s.reasoningOpen {
		return
	}
	s.streamEvents = append(s.streamEvents, ResponseStreamEvent{
		Type: ResponseStreamReasoningEnd, ReasoningType: s.reasoningDisplayType, Incomplete: incomplete || s.reasoningIncomplete,
	})
	s.reasoningOpen = false
	s.reasoningIncomplete = false
}

func (s *OpenAIStreamState) closeReasoning(incomplete bool) {
	for _, choice := range s.choices {
		choice.reasoning = nil
	}
	s.closeReasoningDisplay(incomplete)
}

func (s *OpenAIStreamState) hasActiveReasoning() bool {
	for _, choice := range s.choices {
		if choice.reasoning != nil {
			return true
		}
	}
	return false
}

func (s *OpenAIStreamState) choiceIndexes() []int {
	indexes := make([]int, 0, len(s.choices))
	for index := range s.choices {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	return indexes
}

// IncompleteReason reports provider truncation or a stream that has not reached
// completion. A valid finish reason also permits clean EOF without [DONE].
func (s *OpenAIStreamState) IncompleteReason() string {
	if s.failed {
		return "invalid_stream"
	}
	complete := len(s.choices) > 0
	for _, index := range s.choiceIndexes() {
		choice := s.choices[index]
		if reason := openAIChatIncompleteReason(choice.finishReason); reason != "" {
			return reason
		}
		if choice.finishReason == "" {
			complete = false
		}
	}
	if s.terminal || complete {
		return ""
	}
	return "stream_interrupted"
}

// finishStream closes visible reasoning even when the transport stops before
// [DONE]. An unfinished choice cannot authorize execution of pending tools.
func (s *OpenAIStreamState) finishStream() ([]ToolCall, error) {
	s.closeReasoning(s.IncompleteReason() != "")
	return s.finalizeToolCalls()
}

func (s *OpenAIStreamState) finalizeToolCalls() ([]ToolCall, error) {
	if s.toolsFinalized || s.IncompleteReason() != "" {
		return nil, nil
	}
	keys := make([]openAIStreamToolCallKey, 0, len(s.partialToolCalls))
	for key := range s.partialToolCalls {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].choiceIndex != keys[j].choiceIndex {
			return keys[i].choiceIndex < keys[j].choiceIndex
		}
		return keys[i].toolIndex < keys[j].toolIndex
	})
	calls := make([]ToolCall, 0, len(keys))
	for _, key := range keys {
		call := *s.partialToolCalls[key]
		if call.Type != "custom" && len(call.Args) > 0 {
			// Compact without decoding numbers through float64. Tool arguments
			// can contain integers that exceed its exact range.
			var compacted bytes.Buffer
			if err := json.Compact(&compacted, call.Args); err != nil {
				s.failed = true
				return nil, newFatalStreamError(fmt.Errorf("invalid JSON in tool call choice %d index %d arguments: %w", key.choiceIndex, key.toolIndex, err))
			}
			call.Args = json.RawMessage(compacted.Bytes())
		}
		calls = append(calls, call)
	}
	s.toolsFinalized = true
	s.toolBlocks = responseBlocksFromParts("", calls, "")
	return calls, nil
}

func (s *OpenAIStreamState) Blocks() []Block {
	blocks := make([]Block, 0, len(s.blocks)+len(s.toolBlocks))
	for _, block := range s.blocks {
		if block.reasoning {
			reasoning := ReasoningBlock{Provider: "openai", Type: block.reasoningType, Text: block.text.String()}
			if block.reasoningType == openAIThinkingChunkType {
				fields := make(map[string]json.RawMessage, len(block.metadata)+1)
				for key, value := range block.metadata {
					fields[key] = value
				}
				fields["thinking"], _ = json.Marshal(block.thinkingParts)
				reasoning.Raw, _ = json.Marshal(fields)
			}
			blocks = append(blocks, reasoning)
		} else {
			blocks = append(blocks, TextBlock{Text: block.text.String()})
		}
	}
	return append(blocks, s.toolBlocks...)
}

func (s *OpenAIStreamState) drainResponseStreamEvents() []ResponseStreamEvent {
	events := s.streamEvents
	s.streamEvents = nil
	return events
}

func (s *OpenAIStreamState) Usage() *Usage {
	return s.usage
}
