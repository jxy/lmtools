package proxy

func updateParsedStreamUsage(handler *AnthropicStreamHandler, inputTokens, outputTokens *int) {
	if inputTokens != nil {
		handler.state.InputTokens = *inputTokens
	}
	if outputTokens != nil {
		handler.state.OutputTokens = *outputTokens
	}
}

func emitParsedTextDelta(handler *AnthropicStreamHandler, text string) error {
	if text == "" {
		return nil
	}
	return handler.SendTextDelta(text)
}

func closeParsedTextBlockIfNeeded(handler *AnthropicStreamHandler) error {
	if handler.state.TextBlockClosed {
		return nil
	}
	if handler.state.AccumulatedText != "" && !handler.state.TextSent {
		if err := handler.SendTextDelta(handler.state.AccumulatedText); err != nil {
			return err
		}
	}
	if err := handler.SendContentBlockStop(0); err != nil {
		return err
	}
	handler.state.TextBlockClosed = true
	return nil
}

func beginParsedToolUseBlock(handler *AnthropicStreamHandler, streamIndex *int, toolID, name string) (int, error) {
	if streamIndex != nil && handler.state.ToolIndex != nil && *streamIndex == *handler.state.ToolIndex {
		return handler.state.LastToolIndex, nil
	}

	if err := closeParsedTextBlockIfNeeded(handler); err != nil {
		return 0, err
	}

	if streamIndex != nil {
		index := *streamIndex
		handler.state.ToolIndex = &index
	}

	handler.state.LastToolIndex++
	if err := handler.SendToolUseStart(handler.state.LastToolIndex, toolID, name); err != nil {
		return 0, err
	}

	return handler.state.LastToolIndex, nil
}

func emitParsedToolInputDelta(handler *AnthropicStreamHandler, blockIndex int, partialJSON string) error {
	if partialJSON == "" {
		return nil
	}
	return handler.SendToolInputDelta(blockIndex, partialJSON)
}
