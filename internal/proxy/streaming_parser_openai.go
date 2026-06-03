package proxy

import (
	"encoding/json"
	"io"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"sort"
)

// OpenAIStreamParser parses OpenAI streaming responses.
type OpenAIStreamParser struct {
	handler           *AnthropicStreamHandler
	pendingStopReason string
	finished          bool
	flushedFinal      bool
	stops             []string
	stoppers          map[int]*stopTextEnforcer
	toolStates        map[openAIStreamToolKey]*openAIToAnthropicToolState
}

type openAIToAnthropicToolState struct {
	id         string
	toolType   string
	name       string
	arguments  string
	input      string
	sawCustom  bool
	started    bool
	blockIndex int
	flushed    bool
}

// NewOpenAIStreamParser creates a new OpenAI stream parser.
func NewOpenAIStreamParser(handler *AnthropicStreamHandler) *OpenAIStreamParser {
	return NewOpenAIStreamParserWithStops(handler, nil)
}

// NewOpenAIStreamParserWithStops creates a new OpenAI stream parser with local stop enforcement.
func NewOpenAIStreamParserWithStops(handler *AnthropicStreamHandler, stops []string) *OpenAIStreamParser {
	filtered := nonEmptyStopSequences(stops)
	return &OpenAIStreamParser{handler: handler, stops: filtered, stoppers: make(map[int]*stopTextEnforcer), toolStates: make(map[openAIStreamToolKey]*openAIToAnthropicToolState)}
}

// Parse parses an OpenAI streaming response.
func (p *OpenAIStreamParser) Parse(reader io.Reader) error {
	scanner := NewSSEScanner(reader)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		if data, ok := sseFieldValue(line, "data"); ok {
			if data == "[DONE]" {
				return p.finishPending("end_turn")
			}

			warnUnknownFields(p.handler.ctx, []byte(data), OpenAIStreamChunk{}, "OpenAI stream chunk")
			parsed, err := core.ParseOpenAIStreamChunk([]byte(data))
			if err != nil {
				if handleErr := handleStreamError(p.handler.ctx, nil, "OpenAIStreamParser", err); handleErr != nil {
					return handleErr
				}
				continue
			}

			if logger.From(p.handler.ctx).IsDebugEnabled() {
				var chunk map[string]interface{}
				if err := json.Unmarshal([]byte(data), &chunk); err == nil {
					logger.DebugJSON(logger.From(p.handler.ctx), "OpenAI Stream Chunk", chunk)
				}
			}

			if err := p.processChunk(parsed); err != nil {
				return handleStreamError(p.handler.ctx, p.handler, "OpenAIStreamParser", err)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	if p.pendingStopReason != "" && !p.finished {
		return p.finishPending(p.pendingStopReason)
	}
	return nil
}

func (p *OpenAIStreamParser) processChunk(chunk core.ParsedOpenAIStreamChunk) error {
	if p.finished {
		return nil
	}

	p.handler.SetParsedUsage(chunk.Usage.InputTokens, chunk.Usage.OutputTokens)
	choices := chunk.Choices
	for _, choice := range choices {
		if err := p.processChoice(choice); err != nil {
			return err
		}
	}
	return nil
}

func (p *OpenAIStreamParser) processChoice(choice core.ParsedOpenAIStreamChoice) error {
	if p.finished {
		return nil
	}

	if choice.Content != "" {
		content := choice.Content
		matched := false
		if len(p.stops) > 0 {
			stopper := p.stopperForChoice(choice.Index)
			content, matched = stopper.Push(choice.Content)
		}
		if content != "" {
			if err := p.handler.SendTextDelta(content); err != nil {
				return err
			}
		}
		if matched {
			p.pendingStopReason = combineAnthropicStopReason(p.pendingStopReason, "end_turn")
			return p.finishPending("end_turn")
		}
	}

	for _, tc := range choice.ToolCalls {
		if err := p.applyToolDelta(tc); err != nil {
			return err
		}
	}

	if choice.FinishReason != "" {
		mapped := MapOpenAIFinishReasonToStopReason(choice.FinishReason)
		p.pendingStopReason = combineAnthropicStopReason(p.pendingStopReason, mapped)
		if err := p.flushStopTail(choice.Index); err != nil {
			return err
		}
	}

	return nil
}

func (p *OpenAIStreamParser) applyToolDelta(tc core.ParsedOpenAIStreamToolCall) error {
	key := openAIStreamToolKeyFromParsed(tc)
	state := p.toolState(key)
	if tc.ID != "" {
		state.id = tc.ID
	}
	if tc.Type != "" {
		state.toolType = tc.Type
	}
	if tc.Name != "" {
		state.name = tc.Name
	}
	if tc.Type == "custom" {
		state.sawCustom = true
		state.input += tc.Input
	} else {
		state.arguments += tc.Arguments
	}
	if state.name == "" {
		return nil
	}
	if err := p.startToolState(key, state); err != nil {
		return err
	}
	if state.sawCustom {
		return nil
	}
	if state.arguments != "" {
		if err := p.handler.SendToolInputDelta(state.blockIndex, state.arguments); err != nil {
			return err
		}
		state.arguments = ""
	}
	return nil
}

func (p *OpenAIStreamParser) toolState(key openAIStreamToolKey) *openAIToAnthropicToolState {
	state := p.toolStates[key]
	if state == nil {
		state = &openAIToAnthropicToolState{}
		p.toolStates[key] = state
	}
	return state
}

func (p *OpenAIStreamParser) startToolState(key openAIStreamToolKey, state *openAIToAnthropicToolState) error {
	if state.started || state.name == "" {
		return nil
	}
	toolID := state.id
	if toolID == "" {
		toolID = generateToolUseID()
		state.id = toolID
	}
	blockIndex, err := p.handler.BeginParsedToolUseBlockForOpenAIKey(key, toolID, state.name)
	if err != nil {
		return err
	}
	state.blockIndex = blockIndex
	state.started = true
	return nil
}

func (p *OpenAIStreamParser) flushToolStates() error {
	keys := make([]openAIStreamToolKey, 0, len(p.toolStates))
	for key := range p.toolStates {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return openAIStreamToolKeyLess(keys[i], keys[j])
	})
	for _, key := range keys {
		state := p.toolStates[key]
		if state == nil || state.flushed || state.name == "" {
			continue
		}
		if err := p.startToolState(key, state); err != nil {
			return err
		}
		if state.sawCustom {
			if err := p.handler.SendToolInputDelta(state.blockIndex, string(core.WrapCustomToolInput(state.input))); err != nil {
				return err
			}
		} else if state.arguments != "" {
			if err := p.handler.SendToolInputDelta(state.blockIndex, state.arguments); err != nil {
				return err
			}
			state.arguments = ""
		}
		state.flushed = true
	}
	return nil
}

func (p *OpenAIStreamParser) stopperForChoice(index int) *stopTextEnforcer {
	stopper := p.stoppers[index]
	if stopper == nil {
		stopper = newStopTextEnforcer(p.stops)
		p.stoppers[index] = stopper
	}
	return stopper
}

func (p *OpenAIStreamParser) flushStopTail(index int) error {
	stopper := p.stoppers[index]
	if stopper == nil || stopper.Stopped() {
		return nil
	}
	if tail := stopper.Flush(); tail != "" {
		return p.handler.SendTextDelta(tail)
	}
	return nil
}

func (p *OpenAIStreamParser) flushStopTails() error {
	indexes := make([]int, 0, len(p.stoppers))
	for index := range p.stoppers {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		if err := p.flushStopTail(index); err != nil {
			return err
		}
	}
	return nil
}

func (p *OpenAIStreamParser) finishPending(defaultStopReason string) error {
	if p.finished {
		return nil
	}
	if !p.flushedFinal {
		if err := p.flushStopTails(); err != nil {
			return err
		}
		if err := p.flushToolStates(); err != nil {
			return err
		}
		p.flushedFinal = true
	}

	stopReason := p.pendingStopReason
	if stopReason == "" {
		stopReason = defaultStopReason
	}
	p.finished = true
	return p.handler.Complete(stopReason)
}
