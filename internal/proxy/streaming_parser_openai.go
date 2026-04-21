package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"strings"
	"time"
)

// OpenAIStreamParser parses OpenAI streaming responses.
type OpenAIStreamParser struct {
	handler           *AnthropicStreamHandler
	pendingStopReason string
	finished          bool
}

// NewOpenAIStreamParser creates a new OpenAI stream parser.
func NewOpenAIStreamParser(handler *AnthropicStreamHandler) *OpenAIStreamParser {
	return &OpenAIStreamParser{handler: handler}
}

// Parse parses an OpenAI streaming response.
func (p *OpenAIStreamParser) Parse(reader io.Reader) error {
	scanner := NewSSEScanner(reader)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")

			if data == "[DONE]" {
				return p.finishPending("end_turn")
			}

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
	updateParsedStreamUsage(p.handler, chunk.Usage.InputTokens, chunk.Usage.OutputTokens)

	if err := emitParsedTextDelta(p.handler, chunk.Content); err != nil {
		return err
	}

	for _, tc := range chunk.ToolCalls {
		index := tc.Index
		toolID := tc.ID
		if toolID == "" {
			toolID = fmt.Sprintf("toolu_%x", time.Now().UnixNano())
		}

		blockIndex, err := beginParsedToolUseBlock(p.handler, &index, toolID, tc.Name)
		if err != nil {
			return err
		}

		if err := emitParsedToolInputDelta(p.handler, blockIndex, tc.Arguments); err != nil {
			return err
		}
	}

	if chunk.FinishReason != "" {
		p.pendingStopReason = MapOpenAIFinishReasonToStopReason(chunk.FinishReason)
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
