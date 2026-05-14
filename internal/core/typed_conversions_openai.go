package core

import (
	"encoding/json"
	"log"
	"strings"
)

// AdaptCustomToolBlocksForFunctionCompatibility rewrites custom tool-use
// history into ordinary function-call history for compatibility backends.
func AdaptCustomToolBlocksForFunctionCompatibility(messages []TypedMessage) []TypedMessage {
	if len(messages) == 0 {
		return messages
	}
	adapted := make([]TypedMessage, len(messages))
	for i, msg := range messages {
		adapted[i] = msg
		if len(msg.Blocks) == 0 {
			continue
		}
		blocks := make([]Block, len(msg.Blocks))
		for j, block := range msg.Blocks {
			switch b := block.(type) {
			case ToolUseBlock:
				blocks[j] = adaptCustomToolUseBlockForFunction(b)
			case *ToolUseBlock:
				if b == nil {
					blocks[j] = block
					continue
				}
				adaptedBlock := adaptCustomToolUseBlockForFunction(*b)
				blocks[j] = &adaptedBlock
			case ToolResultBlock:
				if b.Type == "custom" {
					b.Type = "function"
				}
				blocks[j] = b
			case *ToolResultBlock:
				if b == nil {
					blocks[j] = block
					continue
				}
				adaptedBlock := *b
				if adaptedBlock.Type == "custom" {
					adaptedBlock.Type = "function"
				}
				blocks[j] = &adaptedBlock
			default:
				blocks[j] = block
			}
		}
		adapted[i].Blocks = blocks
	}
	return adapted
}

func adaptCustomToolUseBlockForFunction(block ToolUseBlock) ToolUseBlock {
	if block.Type != "custom" {
		return block
	}
	block.Type = "function"
	block.Input = WrapCustomToolInput(CustomToolRawInput(block.InputString, block.Input))
	block.InputString = ""
	return block
}

// ToOpenAITyped converts TypedMessage to strongly typed OpenAI format.
func ToOpenAITyped(messages []TypedMessage) []OpenAIMessage {
	result := make([]OpenAIMessage, 0, len(messages))

	for _, msg := range messages {
		openAIMsg := OpenAIMessage{
			Role: msg.Role,
		}

		switch msg.Role {
		case string(RoleAssistant):
			content, toolCalls := ConvertBlocksToOpenAIContentTyped(msg.Blocks)
			openAIMsg.Content = content
			openAIMsg.ToolCalls = toolCalls

		case string(RoleUser):
			var hasToolResults bool
			for _, block := range msg.Blocks {
				if _, ok := block.(ToolResultBlock); ok {
					hasToolResults = true
					break
				}
			}

			if hasToolResults {
				for _, block := range msg.Blocks {
					switch b := block.(type) {
					case ToolResultBlock:
						content := b.Content
						toolMsg := OpenAIMessage{
							Role:       "tool",
							ToolCallID: b.ToolUseID,
							Content: OpenAIContentUnion{
								Text:     &content,
								Contents: nil,
							},
						}
						result = append(result, toolMsg)
					case TextBlock:
						text := b.Text
						userMsg := OpenAIMessage{
							Role: "user",
							Content: OpenAIContentUnion{
								Text:     &text,
								Contents: nil,
							},
						}
						result = append(result, userMsg)
					}
				}
				continue
			}

			content, _ := ConvertBlocksToOpenAIContentTyped(msg.Blocks)
			openAIMsg.Content = content

		default:
			for _, block := range msg.Blocks {
				if textBlock, ok := block.(TextBlock); ok {
					text := textBlock.Text
					openAIMsg.Content = OpenAIContentUnion{
						Text:     &text,
						Contents: nil,
					}
					break
				}
			}
		}

		result = append(result, openAIMsg)
	}

	return result
}

// FromOpenAITyped converts strongly typed OpenAI messages to TypedMessage.
func FromOpenAITyped(messages []OpenAIMessage) []TypedMessage {
	result := make([]TypedMessage, 0, len(messages))

	for _, msg := range messages {
		typed := TypedMessage{Role: msg.Role}

		switch msg.Role {
		case "tool":
			typed.Role = "user"
			var content string
			if msg.Content.Text != nil {
				content = *msg.Content.Text
			}
			typed.Blocks = []Block{
				ToolResultBlock{
					ToolUseID: msg.ToolCallID,
					Name:      msg.Name,
					Content:   content,
					IsError:   false,
				},
			}

		default:
			if msg.Content.Text != nil && *msg.Content.Text != "" {
				typed.Blocks = append(typed.Blocks, TextBlock{Text: *msg.Content.Text})
			} else if len(msg.Content.Contents) > 0 {
				for _, item := range msg.Content.Contents {
					switch item.Type {
					case "text":
						if item.Text != "" {
							typed.Blocks = append(typed.Blocks, TextBlock{Text: item.Text})
						}
					case "image_url":
						if item.ImageURL != nil {
							typed.Blocks = append(typed.Blocks, ImageBlock{
								URL:    item.ImageURL.URL,
								Detail: item.ImageURL.Detail,
							})
						}
					case "input_audio":
						if item.InputAudio != nil {
							audioBlock := AudioBlock{
								ID:       item.InputAudio.ID,
								Data:     item.InputAudio.Data,
								Format:   item.InputAudio.Format,
								URL:      item.InputAudio.URL,
								Duration: item.InputAudio.Duration,
							}
							if audioBlock.ID != "" || audioBlock.Data != "" || audioBlock.URL != "" {
								typed.Blocks = append(typed.Blocks, audioBlock)
							}
						}
					case "file":
						if item.File != nil && item.File.FileID != "" {
							typed.Blocks = append(typed.Blocks, FileBlock{FileID: item.File.FileID})
						}
					}
				}
			}

			for _, tc := range msg.ToolCalls {
				if tc.Type == "custom" {
					name := ""
					input := ""
					if tc.Custom != nil {
						name = tc.Custom.Name
						input = tc.Custom.Input
					}
					typed.Blocks = append(typed.Blocks, ToolUseBlock{
						ID:          tc.ID,
						Type:        "custom",
						Name:        name,
						Input:       jsonStringRawMessage(input),
						InputString: input,
					})
					continue
				}
				typed.Blocks = append(typed.Blocks, ToolUseBlock{
					ID:    tc.ID,
					Type:  "function",
					Name:  tc.Function.Name,
					Input: json.RawMessage(tc.Function.Arguments),
				})
			}
		}

		result = append(result, typed)
	}

	return result
}

// MarshalOpenAIMessagesForRequest converts typed OpenAI messages to []interface{} for request bodies.
func MarshalOpenAIMessagesForRequest(messages []OpenAIMessage) []interface{} {
	result := make([]interface{}, 0, len(messages))
	for _, msg := range messages {
		if err := msg.Content.ValidateForMarshal(); err != nil {
			log.Printf("Warning: Invalid OpenAIContentUnion in message: %v", err)
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

		if len(msg.ToolCalls) > 0 {
			msgMap["tool_calls"] = msg.ToolCalls
		}
		if msg.ToolCallID != "" {
			msgMap["tool_call_id"] = msg.ToolCallID
		}
		result = append(result, msgMap)
	}
	return result
}

// ConvertBlocksToOpenAIContentTyped converts blocks to strongly typed OpenAI content and tool calls.
func ConvertBlocksToOpenAIContentTyped(blocks []Block) (OpenAIContentUnion, []OpenAIToolCall) {
	var union OpenAIContentUnion
	var toolCalls []OpenAIToolCall
	var parts []OpenAIContent
	var textBuilder strings.Builder
	hasNonText := false

	for _, block := range blocks {
		switch v := block.(type) {
		case TextBlock:
			textBuilder.WriteString(v.Text)
			parts = append(parts, OpenAIContent{
				Type: "text",
				Text: v.Text,
			})

		case ImageBlock:
			hasNonText = true
			imagePart := OpenAIContent{
				Type: "image_url",
				ImageURL: &OpenAIImageURL{
					URL: v.URL,
				},
			}
			if v.Detail != "" {
				imagePart.ImageURL.Detail = v.Detail
			}
			parts = append(parts, imagePart)

		case AudioBlock:
			hasNonText = true
			audioPart := OpenAIContent{
				Type: "input_audio",
			}
			inputAudio := &AudioData{}

			if v.Data != "" {
				inputAudio.Data = v.Data
				if v.Format != "" {
					inputAudio.Format = v.Format
				} else {
					inputAudio.Format = "wav"
				}
			} else if v.ID != "" {
				inputAudio.ID = v.ID
			}

			if inputAudio.Data != "" || inputAudio.ID != "" {
				audioPart.InputAudio = inputAudio
				parts = append(parts, audioPart)
			}

		case FileBlock:
			hasNonText = true
			parts = append(parts, OpenAIContent{
				Type: "file",
				File: &FileData{
					FileID: v.FileID,
				},
			})

		case ToolUseBlock:
			if v.Type == "custom" {
				toolCall := OpenAIToolCall{
					ID:   v.ID,
					Type: "custom",
					Custom: &OpenAICustomCall{
						Name:  v.Name,
						Input: firstNonEmptyString(v.InputString, rawJSONStringValue(v.Input)),
					},
				}
				toolCalls = append(toolCalls, toolCall)
				continue
			}
			toolCall := OpenAIToolCall{
				ID:   v.ID,
				Type: "function",
				Function: OpenAIFunctionCall{
					Name: v.Name,
				},
			}
			if len(v.Input) > 0 {
				toolCall.Function.Arguments = string(v.Input)
			}
			toolCalls = append(toolCalls, toolCall)

		case ToolResultBlock:
			continue
		}
	}

	if hasNonText {
		union.Contents = parts
		return union, toolCalls
	}

	if len(toolCalls) > 0 {
		if textBuilder.Len() > 0 {
			text := textBuilder.String()
			union.Text = &text
			return union, toolCalls
		}
		return union, toolCalls
	}

	if textBuilder.Len() > 0 {
		text := textBuilder.String()
		union.Text = &text
		return union, toolCalls
	}

	return union, toolCalls
}

func jsonStringRawMessage(value string) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return data
}

func rawJSONStringValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
