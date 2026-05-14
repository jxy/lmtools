package core

import (
	"encoding/json"
	"log"
	"strings"
)

// ToAnthropicTyped converts TypedMessage to strongly typed Anthropic format.
func ToAnthropicTyped(messages []TypedMessage) []AnthropicMessage {
	result := make([]AnthropicMessage, 0, len(messages))

	for _, msg := range messages {
		anthMsg := AnthropicMessage{
			Role: msg.Role,
		}

		if len(msg.Blocks) == 1 {
			if textBlock, ok := msg.Blocks[0].(TextBlock); ok {
				text := textBlock.Text
				if text == "" {
					continue
				}
				anthMsg.Content = AnthropicContentUnion{
					Text:     &text,
					Contents: nil,
				}
				result = append(result, anthMsg)
				continue
			}
		}

		content := make([]AnthropicContent, 0, len(msg.Blocks))
		for _, block := range msg.Blocks {
			switch b := block.(type) {
			case TextBlock:
				if b.Text == "" {
					continue
				}
				content = append(content, AnthropicContent{
					Type: "text",
					Text: b.Text,
				})
			case ReasoningBlock:
				if anthropicContent, ok := reasoningBlockToAnthropicContent(b); ok {
					content = append(content, anthropicContent)
				}
			case ImageBlock:
				source := &AnthropicImageSource{
					Type: "url",
					URL:  b.URL,
				}
				if mediaType, data, ok := ParseBase64DataURL(b.URL); ok {
					source = &AnthropicImageSource{
						Type:      "base64",
						MediaType: mediaType,
						Data:      data,
					}
				}
				content = append(content, AnthropicContent{
					Type:   "image",
					Source: source,
				})
			case AudioBlock:
				audioContent := AnthropicContent{
					Type: "input_audio",
				}
				audioData := &AudioData{
					ID:       b.ID,
					Data:     b.Data,
					Format:   b.Format,
					URL:      b.URL,
					Duration: b.Duration,
				}
				ensureAudioFormat(audioData)
				audioContent.InputAudio = audioData
				content = append(content, audioContent)
			case FileBlock:
				content = append(content, AnthropicContent{
					Type: "file",
					File: &FileData{
						FileID: b.FileID,
					},
				})
			case ToolUseBlock:
				input := b.Input
				if b.Type == "custom" {
					input = WrapCustomToolInput(CustomToolRawInput(b.InputString, b.Input))
				}
				content = append(content, AnthropicContent{
					Type:  "tool_use",
					ID:    b.ID,
					Name:  b.Name,
					Input: input,
				})
			case ToolResultBlock:
				content = append(content, AnthropicContent{
					Type:      "tool_result",
					ToolUseID: b.ToolUseID,
					Content:   b.Content,
					IsError:   b.IsError,
				})
			}
		}

		if len(content) > 0 {
			anthMsg.Content = AnthropicContentUnion{
				Text:     nil,
				Contents: content,
			}
			result = append(result, anthMsg)
		}
	}

	return result
}

func reasoningBlockToAnthropicContent(block ReasoningBlock) (AnthropicContent, bool) {
	if block.Provider == "anthropic" {
		if content, ok := signedAnthropicReasoningContent(block); ok {
			return content, true
		}
		return AnthropicContent{}, false
	}

	if block.Provider == "openai" {
		text := foreignReasoningSummaryText(block)
		if text == "" {
			return AnthropicContent{}, false
		}
		return AnthropicContent{
			Type: "text",
			Text: text,
		}, true
	}
	return AnthropicContent{}, false
}

func signedAnthropicReasoningContent(block ReasoningBlock) (AnthropicContent, bool) {
	if len(block.Raw) > 0 {
		var raw map[string]interface{}
		if err := json.Unmarshal(block.Raw, &raw); err == nil {
			blockType, _ := raw["type"].(string)
			switch blockType {
			case "thinking":
				if signature, _ := raw["signature"].(string); signature != "" {
					return AnthropicContent{Raw: sanitizedAnthropicReasoningRaw(raw, block.Raw)}, true
				}
			case "redacted_thinking":
				if data, _ := raw["data"].(string); data != "" {
					return AnthropicContent{Raw: sanitizedAnthropicReasoningRaw(raw, block.Raw)}, true
				}
			}
		}
	}

	switch block.Type {
	case "thinking":
		if block.Signature == "" {
			return AnthropicContent{}, false
		}
		thinking := block.Text
		if thinking == "" {
			thinking = string(block.Content)
		}
		if thinking == "" {
			return AnthropicContent{}, false
		}
		return AnthropicContent{
			Type:      "thinking",
			Thinking:  thinking,
			Signature: block.Signature,
		}, true
	case "redacted_thinking":
		if data := reasoningBlockData(block); data != "" {
			return AnthropicContent{
				Type: "redacted_thinking",
				Data: data,
			}, true
		}
	}
	return AnthropicContent{}, false
}

func sanitizedAnthropicReasoningRaw(raw map[string]interface{}, original json.RawMessage) json.RawMessage {
	text, hasText := raw["text"]
	if !hasText {
		return append(json.RawMessage(nil), original...)
	}
	textString, isString := text.(string)
	if text != nil && (!isString || textString != "") {
		return append(json.RawMessage(nil), original...)
	}

	sanitized := make(map[string]interface{}, len(raw)-1)
	for key, value := range raw {
		if key != "text" {
			sanitized[key] = value
		}
	}
	data, err := json.Marshal(sanitized)
	if err != nil {
		return append(json.RawMessage(nil), original...)
	}
	return data
}

func reasoningBlockData(block ReasoningBlock) string {
	if len(block.Content) > 0 {
		var content struct {
			Data string `json:"data"`
		}
		if err := json.Unmarshal(block.Content, &content); err == nil && content.Data != "" {
			return content.Data
		}
	}
	return ""
}

func foreignReasoningSummaryText(block ReasoningBlock) string {
	var parts []string
	if block.Text != "" {
		parts = append(parts, block.Text)
	}
	appendReasoningTextFromRaw(block.Summary, &parts)
	appendReasoningTextFromRaw(block.Content, &parts)
	if len(block.Raw) > 0 {
		var raw interface{}
		if err := json.Unmarshal(block.Raw, &raw); err == nil {
			appendReasoningTextFromNamedFields(raw, &parts)
		}
	}
	return strings.Join(dedupeNonEmptyStrings(parts), "\n")
}

func appendReasoningTextFromRaw(raw json.RawMessage, parts *[]string) {
	if len(raw) == 0 {
		return
	}
	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		text := strings.TrimSpace(string(raw))
		if text != "" {
			*parts = append(*parts, text)
		}
		return
	}
	appendReasoningText(value, parts)
}

func appendReasoningTextFromNamedFields(value interface{}, parts *[]string) {
	switch v := value.(type) {
	case map[string]interface{}:
		for _, key := range []string{"summary", "content", "text"} {
			if nested, ok := v[key]; ok {
				appendReasoningText(nested, parts)
			}
		}
	case []interface{}:
		appendReasoningText(v, parts)
	case string:
		if text := strings.TrimSpace(v); text != "" {
			*parts = append(*parts, text)
		}
	}
}

func appendReasoningText(value interface{}, parts *[]string) {
	switch v := value.(type) {
	case []interface{}:
		for _, item := range v {
			appendReasoningText(item, parts)
		}
	case map[string]interface{}:
		if text, ok := v["text"].(string); ok {
			if text = strings.TrimSpace(text); text != "" {
				*parts = append(*parts, text)
			}
		}
		for _, key := range []string{"summary", "content"} {
			if nested, ok := v[key]; ok {
				appendReasoningText(nested, parts)
			}
		}
	case string:
		if text := strings.TrimSpace(v); text != "" {
			*parts = append(*parts, text)
		}
	}
}

func dedupeNonEmptyStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

// MarshalAnthropicMessagesForRequest converts typed Anthropic messages to []interface{} for request bodies.
func MarshalAnthropicMessagesForRequest(messages []AnthropicMessage) []interface{} {
	result := make([]interface{}, 0, len(messages))
	for _, msg := range messages {
		if err := msg.Content.ValidateForMarshal(); err != nil {
			log.Printf("Warning: Invalid AnthropicContentUnion in message: %v", err)
		}

		msgMap := map[string]interface{}{
			"role": msg.Role,
		}

		if len(msg.Content.Contents) > 0 {
			contentArray := make([]interface{}, len(msg.Content.Contents))
			for i, c := range msg.Content.Contents {
				contentArray[i] = c.ToMap()
			}
			msgMap["content"] = contentArray
		} else if msg.Content.Text != nil && *msg.Content.Text != "" {
			msgMap["content"] = *msg.Content.Text
		}

		result = append(result, msgMap)
	}
	return result
}

// FromAnthropicTyped converts strongly typed Anthropic messages to TypedMessage.
func FromAnthropicTyped(messages []AnthropicMessage) []TypedMessage {
	result := make([]TypedMessage, 0, len(messages))

	for _, msg := range messages {
		typed := TypedMessage{Role: msg.Role}

		if msg.Content.Text != nil && *msg.Content.Text != "" {
			typed.Blocks = []Block{TextBlock{Text: *msg.Content.Text}}
		} else if len(msg.Content.Contents) > 0 {
			for _, block := range msg.Content.Contents {
				switch block.Type {
				case "text":
					typed.Blocks = append(typed.Blocks, TextBlock{Text: block.Text})
				case "thinking", "redacted_thinking":
					raw, _ := json.Marshal(block.ToMap())
					typed.Blocks = append(typed.Blocks, ReasoningBlock{
						Provider:  "anthropic",
						Type:      block.Type,
						Text:      block.Thinking,
						Signature: block.Signature,
						Raw:       raw,
					})
				case "tool_use":
					typed.Blocks = append(typed.Blocks, ToolUseBlock{
						ID:    block.ID,
						Name:  block.Name,
						Input: block.Input,
					})
				case "tool_result":
					typed.Blocks = append(typed.Blocks, ToolResultBlock{
						ToolUseID: block.ToolUseID,
						Content:   block.Content,
						IsError:   block.IsError,
					})
				case "image":
					if block.Source != nil {
						typed.Blocks = append(typed.Blocks, ImageBlock{
							URL:    block.Source.URL,
							Detail: "auto",
						})
					}
				case "input_audio":
					if block.InputAudio != nil {
						typed.Blocks = append(typed.Blocks, AudioBlock{
							ID:       block.InputAudio.ID,
							Data:     block.InputAudio.Data,
							Format:   block.InputAudio.Format,
							URL:      block.InputAudio.URL,
							Duration: block.InputAudio.Duration,
						})
					}
				case "file":
					if block.File != nil && block.File.FileID != "" {
						typed.Blocks = append(typed.Blocks, FileBlock{
							FileID: block.File.FileID,
						})
					}
				}
			}
		}

		result = append(result, typed)
	}

	return result
}
