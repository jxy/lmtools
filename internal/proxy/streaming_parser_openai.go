package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"time"
)

// OpenAIStreamParser parses OpenAI streaming responses.
type OpenAIStreamParser struct {
	handler           *AnthropicStreamHandler
	pendingStopReason string
	finished          bool
	stopper           *stopTextEnforcer
}

// NewOpenAIStreamParser creates a new OpenAI stream parser.
func NewOpenAIStreamParser(handler *AnthropicStreamHandler) *OpenAIStreamParser {
	return NewOpenAIStreamParserWithStops(handler, nil)
}

// NewOpenAIStreamParserWithStops creates a new OpenAI stream parser with local stop enforcement.
func NewOpenAIStreamParserWithStops(handler *AnthropicStreamHandler, stops []string) *OpenAIStreamParser {
	return &OpenAIStreamParser{handler: handler, stopper: newStopTextEnforcer(stops)}
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
				if err := p.flushStopTail(); err != nil {
					return err
				}
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
	if len(choices) == 0 {
		choices = []core.ParsedOpenAIStreamChoice{{
			FinishReason: chunk.FinishReason,
			Content:      chunk.Content,
			ToolCalls:    chunk.ToolCalls,
		}}
	}
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
		if p.stopper != nil {
			content, matched = p.stopper.Push(choice.Content)
		}
		if content != "" {
			if err := p.handler.SendTextDelta(content); err != nil {
				return err
			}
		}
		if matched {
			p.pendingStopReason = "end_turn"
			return p.finishPending("end_turn")
		}
	}

	for _, tc := range choice.ToolCalls {
		index := tc.Index
		toolID := tc.ID
		if toolID == "" {
			toolID = fmt.Sprintf("toolu_%x", time.Now().UnixNano())
		}

		blockIndex, err := p.handler.BeginParsedToolUseBlock(&index, toolID, tc.Name)
		if err != nil {
			return err
		}

		if tc.Arguments != "" {
			if err := p.handler.SendToolInputDelta(blockIndex, tc.Arguments); err != nil {
				return err
			}
		}
	}

	if choice.FinishReason != "" {
		p.pendingStopReason = MapOpenAIFinishReasonToStopReason(choice.FinishReason)
		if p.stopper != nil && !p.stopper.Stopped() {
			if err := p.flushStopTail(); err != nil {
				return err
			}
		}
	}

	return nil
}

func (p *OpenAIStreamParser) flushStopTail() error {
	if p.stopper == nil || p.stopper.Stopped() {
		return nil
	}
	if tail := p.stopper.Flush(); tail != "" {
		return p.handler.SendTextDelta(tail)
	}
	return nil
}

func (p *OpenAIStreamParser) finishPending(defaultStopReason string) error {
	if p.finished {
		return nil
	}

	stopReason := p.pendingStopReason
	if stopReason == "" {
		stopReason = defaultStopReason
	}
	p.finished = true
	return p.handler.Complete(stopReason)
}
