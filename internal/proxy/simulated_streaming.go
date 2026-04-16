package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
)

type simulatedContentEmitter interface {
	StartTextBlock(index int, content string) error
	WriteTextChunk(index int, chunk string) error
	EndTextBlock(index int, content string) error
	StartToolBlock(index int, block AnthropicContentBlock) error
	WriteToolInputChunk(index int, chunk string) error
	EndToolBlock(index int, block AnthropicContentBlock) error
}

func streamSimulatedContentBlocks(ctx context.Context, content []AnthropicContentBlock, emitter simulatedContentEmitter) error {
	for i, block := range content {
		switch block.Type {
		case "text":
			if err := emitter.StartTextBlock(i, block.Text); err != nil {
				return err
			}
			if err := emitSimulatedTextChunks(ctx, block.Text, func(chunk string) error {
				return emitter.WriteTextChunk(i, chunk)
			}); err != nil {
				return err
			}
			if err := emitter.EndTextBlock(i, block.Text); err != nil {
				return err
			}

		case "tool_use":
			if err := emitter.StartToolBlock(i, block); err != nil {
				return err
			}
			if err := emitSimulatedToolInputChunks(ctx, block.Input, func(chunk string) error {
				return emitter.WriteToolInputChunk(i, chunk)
			}); err != nil {
				return err
			}
			if err := emitter.EndToolBlock(i, block); err != nil {
				return err
			}
		}
	}

	return nil
}

func simulatedTextChunkSize(content string) int {
	chunkSize := constants.DefaultTextChunkSize
	if len(content) > 1000 {
		chunkSize = constants.DefaultTextChunkSize * 2
	}
	return chunkSize
}

func emitSimulatedTextChunks(ctx context.Context, content string, emit func(string) error) error {
	splitter := NewContentSplitter(ctx, TextMode, simulatedTextChunkSize(content))
	for _, chunk := range splitter.Split(content) {
		if err := emit(chunk); err != nil {
			return err
		}
	}
	return nil
}

func emitSimulatedToolInputChunks(ctx context.Context, input interface{}, emit func(string) error) error {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return err
	}

	splitter := NewContentSplitter(ctx, JSONMode, constants.DefaultJSONChunkSize)
	for _, chunk := range splitter.Split(string(inputJSON)) {
		if err := emit(chunk); err != nil {
			return err
		}
	}
	return nil
}

func logStreamedToolUseBlock(ctx context.Context, block AnthropicContentBlock) {
	log := logger.From(ctx)
	if !log.IsDebugEnabled() {
		return
	}

	toolJSON, err := json.Marshal(block)
	if err != nil {
		log.Debugf("Streamed tool use block: %s", block.Name)
		return
	}
	log.Debugf("Streamed tool use block: %s", string(toolJSON))
}

type anthropicSimulatedContentEmitter struct {
	ctx     context.Context
	handler *AnthropicStreamHandler
}

func (e anthropicSimulatedContentEmitter) StartTextBlock(index int, _ string) error {
	return e.handler.SendContentBlockStart(index, "text")
}

func (e anthropicSimulatedContentEmitter) WriteTextChunk(_ int, chunk string) error {
	return e.handler.SendTextDelta(chunk)
}

func (e anthropicSimulatedContentEmitter) EndTextBlock(index int, _ string) error {
	return e.handler.SendContentBlockStop(index)
}

func (e anthropicSimulatedContentEmitter) StartToolBlock(index int, block AnthropicContentBlock) error {
	if err := e.handler.SendToolUseStart(index, block.ID, block.Name); err != nil {
		return err
	}
	return e.handler.SendToolInputDelta(index, "")
}

func (e anthropicSimulatedContentEmitter) WriteToolInputChunk(index int, chunk string) error {
	return e.handler.SendToolInputDelta(index, chunk)
}

func (e anthropicSimulatedContentEmitter) EndToolBlock(index int, block AnthropicContentBlock) error {
	if err := e.handler.SendContentBlockStop(index); err != nil {
		return err
	}
	logStreamedToolUseBlock(e.ctx, block)
	return nil
}

type openAISimulatedContentEmitter struct {
	writer  *OpenAIStreamWriter
	started bool
}

func (e *openAISimulatedContentEmitter) StartTextBlock(_ int, _ string) error {
	if e.started {
		return nil
	}
	e.started = true
	return e.writer.WriteInitialAssistantTextDelta()
}

func (e *openAISimulatedContentEmitter) WriteTextChunk(_ int, chunk string) error {
	return e.writer.WriteContent(chunk)
}

func (e *openAISimulatedContentEmitter) EndTextBlock(_ int, _ string) error {
	return nil
}

func (e *openAISimulatedContentEmitter) StartToolBlock(index int, block AnthropicContentBlock) error {
	if e.started {
		return e.writer.WriteToolCallIntro(index, block.ID, block.Name)
	}
	e.started = true
	return e.writer.WriteInitialAssistantToolCallDelta(index, block.ID, block.Name)
}

func (e *openAISimulatedContentEmitter) WriteToolInputChunk(index int, chunk string) error {
	return e.writer.WriteToolArguments(index, chunk)
}

func (e *openAISimulatedContentEmitter) EndToolBlock(_ int, _ AnthropicContentBlock) error {
	return nil
}
