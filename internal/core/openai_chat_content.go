package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Some Chat Completions backends put thinking inside content instead of reasoning_content.
// The block's type records the wire dialect so history can replay it without
// guessing from a model name or changing ordinary OpenAI messages.
const openAIThinkingChunkType = "thinking"

func parseOpenAIChatContent(raw json.RawMessage) ([]Block, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, err
		}
		if text == "" {
			return nil, nil
		}
		return []Block{TextBlock{Text: text}}, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("chat content must be a string or chunk array: %w", err)
	}
	var blocks []Block
	for _, part := range parts {
		var chunk struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"thinking"`
		}
		if err := json.Unmarshal(part, &chunk); err != nil {
			return nil, fmt.Errorf("invalid chat content chunk: %w", err)
		}
		switch chunk.Type {
		case "text":
			blocks = append(blocks, TextBlock{Text: chunk.Text})
		case openAIThinkingChunkType:
			var text strings.Builder
			for _, inner := range chunk.Thinking {
				if inner.Type != "text" {
					return nil, fmt.Errorf("unsupported thinking content chunk %q", inner.Type)
				}
				text.WriteString(inner.Text)
			}
			blocks = append(blocks, ReasoningBlock{Provider: "openai", Type: openAIThinkingChunkType, Text: text.String(), Raw: append(json.RawMessage(nil), part...)})
		default:
			return nil, fmt.Errorf("unsupported chat content chunk %q", chunk.Type)
		}
	}
	return blocks, nil
}

func openAIChatContentText(blocks []Block) string {
	var text strings.Builder
	for _, block := range blocks {
		if b, ok := block.(TextBlock); ok {
			text.WriteString(b.Text)
		}
	}
	return text.String()
}

func openAIThinkingContent(block ReasoningBlock) OpenAIContent {
	raw := block.Raw
	if len(raw) == 0 {
		raw, _ = json.Marshal(map[string]interface{}{"type": "thinking", "thinking": []map[string]string{{"type": "text", "text": block.Text}}, "closed": true})
	}
	return OpenAIContent{Type: "thinking", Raw: raw}
}
