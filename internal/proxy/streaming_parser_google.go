package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"time"
)

// GoogleStreamParser parses Google AI streaming responses.
type GoogleStreamParser struct {
	handler *AnthropicStreamHandler
}

// NewGoogleStreamParser creates a new Google AI stream parser.
func NewGoogleStreamParser(handler *AnthropicStreamHandler) *GoogleStreamParser {
	return &GoogleStreamParser{handler: handler}
}

// Parse parses a Google AI streaming response.
func (p *GoogleStreamParser) Parse(reader io.Reader) error {
	decoder := json.NewDecoder(reader)

	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			if handleErr := handleStreamError(p.handler.ctx, nil, "GoogleStreamParser", err); handleErr != nil {
				return handleErr
			}
			break
		}

		if logger.From(p.handler.ctx).IsDebugEnabled() {
			var chunk map[string]interface{}
			if err := json.Unmarshal(raw, &chunk); err == nil {
				logger.DebugJSON(logger.From(p.handler.ctx), "Google Stream Chunk", chunk)
			}
		}

		parsed, err := core.ParseGoogleStreamChunk(raw)
		if err != nil {
			if handleErr := handleStreamError(p.handler.ctx, nil, "GoogleStreamParser", err); handleErr != nil {
				return handleErr
			}
			continue
		}

		if err := p.processChunk(parsed); err != nil {
			return handleStreamError(p.handler.ctx, p.handler, "GoogleStreamParser", err)
		}
	}

	return p.handler.Complete("end_turn")
}

func (p *GoogleStreamParser) processChunk(chunk core.ParsedGoogleStreamChunk) error {
	updateParsedStreamUsage(p.handler, chunk.Usage.InputTokens, chunk.Usage.OutputTokens)

	if chunk.FinishReason != "" {
		stopReason := "end_turn"
		if chunk.FinishReason == "MAX_TOKENS" {
			stopReason = "max_tokens"
		}
		return p.handler.Complete(stopReason)
	}

	for _, text := range chunk.TextParts {
		if err := emitParsedTextDelta(p.handler, text); err != nil {
			return err
		}
	}

	for _, functionCall := range chunk.FunctionCalls {
		toolID := fmt.Sprintf("toolu_%x", time.Now().UnixNano())
		blockIndex, err := beginParsedToolUseBlock(p.handler, nil, toolID, functionCall.Name)
		if err != nil {
			return err
		}

		if len(functionCall.Args) > 0 {
			if err := emitParsedToolInputDelta(p.handler, blockIndex, string(functionCall.Args)); err != nil {
				return err
			}
		}

		if err := p.handler.SendContentBlockStop(blockIndex); err != nil {
			return err
		}
	}

	return nil
}
