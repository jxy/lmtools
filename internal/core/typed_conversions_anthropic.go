package core

import "log"

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
				content = append(content, AnthropicContent{
					Type: "text",
					Text: b.Text,
				})
			case ThinkingBlock:
				content = append(content, AnthropicContent{
					Type:      "thinking",
					Thinking:  b.Thinking,
					Signature: b.Signature,
				})
			case ImageBlock:
				content = append(content, AnthropicContent{
					Type: "image",
					Source: &AnthropicImageSource{
						Type:      "url",
						URL:       b.URL,
						MediaType: DetectImageMediaType(b.URL),
					},
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
				content = append(content, AnthropicContent{
					Type:  "tool_use",
					ID:    b.ID,
					Name:  b.Name,
					Input: b.Input,
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
		}
		result = append(result, anthMsg)
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
				case "thinking":
					typed.Blocks = append(typed.Blocks, ThinkingBlock{
						Thinking:  block.Thinking,
						Signature: block.Signature,
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
