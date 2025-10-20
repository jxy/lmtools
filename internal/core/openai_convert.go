package core

import (
	"strings"
)

// ConvertBlocksToOpenAIContent converts typed blocks to OpenAI content format and tool calls.
// This is the single source of truth for OpenAI content conversion, used by both
// core.ToOpenAI and proxy.convertContentBlocksToOpenAI to ensure consistency.
//
// Returns:
// - content: either a string (for text-only) or []interface{} (for multimodal/mixed content)
// - toolCalls: array of OpenAI-formatted tool calls
func ConvertBlocksToOpenAIContent(blocks []Block) (content interface{}, toolCalls []OpenAIToolCall) {
	var contentParts []interface{}
	var textBuilder strings.Builder
	var hasNonText bool
	toolCalls = []OpenAIToolCall{}

	for _, block := range blocks {
		switch b := block.(type) {
		case TextBlock:
			// Accumulate text for potential string-only content
			textBuilder.WriteString(b.Text)
			// Also add to content parts for multimodal case
			contentParts = append(contentParts, map[string]interface{}{
				"type": "text",
				"text": b.Text,
			})

		case ImageBlock:
			hasNonText = true
			imagePart := map[string]interface{}{
				"type": "image_url",
				"image_url": map[string]interface{}{
					"url": b.URL,
				},
			}
			// Add detail if specified
			if b.Detail != "" {
				imagePart["image_url"].(map[string]interface{})["detail"] = b.Detail
			}
			contentParts = append(contentParts, imagePart)

		case AudioBlock:
			hasNonText = true
			audioMap := map[string]interface{}{
				"type": "input_audio",
			}
			inputAudioMap := make(map[string]interface{})

			// Include data and format if available
			if b.Data != "" {
				inputAudioMap["data"] = b.Data
				if b.Format != "" {
					inputAudioMap["format"] = b.Format
				} else {
					inputAudioMap["format"] = "wav" // Default format
				}
			} else if b.ID != "" {
				// Otherwise use ID if available
				inputAudioMap["id"] = b.ID
			}

			if len(inputAudioMap) > 0 {
				audioMap["input_audio"] = inputAudioMap
				contentParts = append(contentParts, audioMap)
			}

		case FileBlock:
			hasNonText = true
			contentParts = append(contentParts, map[string]interface{}{
				"type": "file",
				"file": map[string]interface{}{
					"file_id": b.FileID,
				},
			})

		case ToolUseBlock:
			// Convert to OpenAI tool call format
			toolCall := OpenAIToolCall{
				ID:   b.ID,
				Type: "function",
				Function: OpenAIFunctionCall{
					Name: b.Name,
				},
			}
			// Add arguments if present
			if len(b.Input) > 0 {
				toolCall.Function.Arguments = string(b.Input)
			}
			toolCalls = append(toolCalls, toolCall)

		case ToolResultBlock:
			// Tool results are handled separately in ToOpenAI
			// This function only handles content that goes into the message content field
			// Tool results become separate "tool" role messages in OpenAI format
			continue
		}
	}

	// Determine the appropriate content format
	if hasNonText {
		// Multimodal content always requires array format, even with tool calls
		return contentParts, toolCalls
	}

	if len(toolCalls) > 0 {
		// When there are tool calls with text-only content, return the text
		if textBuilder.Len() > 0 {
			return textBuilder.String(), toolCalls
		}
		// Tool calls only, no content
		return nil, toolCalls
	}

	// Text-only content can be a simple string
	if textBuilder.Len() > 0 {
		return textBuilder.String(), toolCalls
	}

	// No content
	return nil, toolCalls
}

// ConvertBlocksToOpenAIContentMap is a convenience wrapper that returns the tool calls
// as []map[string]interface{} for compatibility with existing code that expects untyped maps
func ConvertBlocksToOpenAIContentMap(blocks []Block) (content interface{}, toolCalls []map[string]interface{}) {
	typedContent, typedToolCalls := ConvertBlocksToOpenAIContent(blocks)

	// Convert typed tool calls to maps
	toolCallMaps := make([]map[string]interface{}, len(typedToolCalls))
	for i, tc := range typedToolCalls {
		toolCallMaps[i] = map[string]interface{}{
			"id":   tc.ID,
			"type": tc.Type,
			"function": map[string]interface{}{
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
			},
		}
	}

	return typedContent, toolCallMaps
}
