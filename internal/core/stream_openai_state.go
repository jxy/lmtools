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
	reasoning bool
	text      strings.Builder
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
	partialToolCalls    map[openAIStreamToolCallKey]*ToolCall
	choices             map[int]*openAIStreamChoiceState
	blocks              []*openAIStreamBlock
	toolBlocks          []Block
	streamEvents        []ResponseStreamEvent
	reasoningOpen       bool
	reasoningIncomplete bool
	terminal            bool
	toolsFinalized      bool
	usage               *Usage
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
	if !ok {
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
		return "", nil, false, err
	}
	if parsed.Usage.InputTokens != nil || parsed.Usage.OutputTokens != nil || parsed.Usage.TotalTokens != nil {
		s.usage = s.usage.MergedWith(UsageFromPayload([]byte(data)))
	}

	var text strings.Builder
	var hasAnswerOrTool bool
	for _, choice := range parsed.Choices {
		state := s.choiceState(choice.Index)
		if choice.ReasoningContent != "" {
			if state.reasoning == nil || s.blocks[len(s.blocks)-1] != state.reasoning {
				state.text = nil
				state.reasoning = &openAIStreamBlock{reasoning: true}
				s.blocks = append(s.blocks, state.reasoning)
			}
			if !s.reasoningOpen {
				s.streamEvents = append(s.streamEvents, ResponseStreamEvent{
					Type: ResponseStreamReasoningStart, ReasoningType: openAIChatReasoningType,
				})
				s.reasoningOpen = true
			}
			state.reasoning.text.WriteString(choice.ReasoningContent)
			s.streamEvents = append(s.streamEvents, ResponseStreamEvent{
				Type: ResponseStreamReasoningDelta, ReasoningType: openAIChatReasoningType, Text: choice.ReasoningContent,
			})
		}
		if choice.Content != "" || len(choice.ToolCalls) > 0 {
			state.reasoning = nil
			hasAnswerOrTool = true
		}
		if choice.Content != "" {
			if state.text == nil || s.blocks[len(s.blocks)-1] != state.text {
				state.text = &openAIStreamBlock{}
				s.blocks = append(s.blocks, state.text)
			}
			state.text.text.WriteString(choice.Content)
			text.WriteString(choice.Content)
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
	// ResponseStreamEvent carries no choice identity. Present interleaved
	// reasoning in one section, closing it before the chunk's answer is
	// emitted, while retaining a separate canonical block for each choice.
	if hasAnswerOrTool || !s.hasActiveReasoning() {
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
		Type: ResponseStreamReasoningEnd, ReasoningType: openAIChatReasoningType, Incomplete: incomplete || s.reasoningIncomplete,
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
			blocks = append(blocks, openAIChatReasoningBlock(block.text.String()))
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
