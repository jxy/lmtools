package core

import (
	"encoding/json"
	"fmt"
)

// parseArgoResponseWithTools parses Argo response with tool call support.
//
// DESIGN NOTE: This function does not take a context.Context parameter because:
// 1. It's a pure parsing function with no I/O operations that would benefit from cancellation
// 2. Adding context would require changes throughout the call chain for minimal benefit
// 3. Debug logging is handled by ExtractEmbeddedToolCalls when needed
func parseArgoResponseWithTools(data []byte, isEmbed bool) (string, []ToolCall, error) {
	return parseArgoResponseWithToolsOptions(data, isEmbed, ArgoResponseParseOptions{})
}

type ArgoResponseParseOptions struct {
	ExtractEmbeddedTools bool
	ToolDefs             []ToolDefinition
}

func parseArgoResponseWithToolsOptions(data []byte, isEmbed bool, opts ArgoResponseParseOptions) (string, []ToolCall, error) {
	if isEmbed {
		// Embedding responses don't have tool calls
		var embedResp struct {
			Embedding [][]float64 `json:"embedding"`
		}
		if err := json.Unmarshal(data, &embedResp); err != nil {
			return "", nil, fmt.Errorf("failed to unmarshal Argo embed response: %w", err)
		}
		if len(embedResp.Embedding) == 0 {
			return "", nil, fmt.Errorf("empty embedding response")
		}
		if len(embedResp.Embedding[0]) == 0 {
			return "[]", nil, nil
		}
		embeddingJSON, err := json.Marshal(embedResp.Embedding[0])
		if err != nil {
			return "", nil, fmt.Errorf("failed to marshal embedding: %w", err)
		}
		return string(embeddingJSON), nil, nil
	}

	// Parse into a struct with interface{} for Response field
	var argoResp struct {
		Response interface{} `json:"response"`
	}
	if err := json.Unmarshal(data, &argoResp); err != nil {
		return "", nil, fmt.Errorf("failed to unmarshal Argo response: %w", err)
	}

	// Handle different response types
	switch resp := argoResp.Response.(type) {
	case string:
		// Simple string response (no tools)
		return resp, nil, nil
	case map[string]interface{}:
		// Response with potential tool calls
		var content string
		var toolCalls []ToolCall
		// Track whether tool_calls field exists and whether it's an empty array
		toolCallsExists := false
		toolCallsPresentAndEmpty := false

		// Extract content text
		if contentStr, ok := resp["content"].(string); ok {
			content = contentStr
		}

		// Extract tool calls
		if toolCallsRaw, ok := resp["tool_calls"]; ok {
			toolCallsExists = true
			// Handle both array and single object formats
			var toolCallsArray []interface{}
			if arr, ok := toolCallsRaw.([]interface{}); ok {
				toolCallsArray = arr
				if len(arr) == 0 {
					toolCallsPresentAndEmpty = true
				}
			} else if obj, ok := toolCallsRaw.(map[string]interface{}); ok {
				// Single object - wrap in array
				toolCallsArray = []interface{}{obj}
			}

			for _, tc := range toolCallsArray {
				toolCallMap, ok := tc.(map[string]interface{})
				if !ok {
					continue
				}

				// Parse tool call (Anthropic format as shown in the example)
				var toolCall ToolCall

				// Store the assistant's content for context
				toolCall.AssistantContent = content

				// Get ID
				if id, ok := toolCallMap["id"].(string); ok {
					toolCall.ID = id
				}

				// Get name
				if name, ok := toolCallMap["name"].(string); ok {
					toolCall.Name = name
				}

				// Get input/args - check for different formats
				if fn, ok := toolCallMap["function"].(map[string]interface{}); ok {
					// OpenAI format - has "function" object with "name" and "arguments"
					if fnName, ok := fn["name"].(string); ok {
						toolCall.Name = fnName
					}
					if args, ok := fn["arguments"].(string); ok {
						// Arguments are already JSON string
						toolCall.Args = json.RawMessage(args)
					}
				} else if input, ok := toolCallMap["input"].(map[string]interface{}); ok {
					// Anthropic format - has "input" field
					argsJSON, err := json.Marshal(input)
					if err != nil {
						return "", nil, fmt.Errorf("failed to marshal tool input: %w", err)
					}
					toolCall.Args = argsJSON
				} else if args, ok := toolCallMap["args"].(map[string]interface{}); ok {
					// Google format - has "args" field
					argsJSON, err := json.Marshal(args)
					if err != nil {
						return "", nil, fmt.Errorf("failed to marshal tool args: %w", err)
					}
					toolCall.Args = argsJSON
				}

				toolCalls = append(toolCalls, toolCall)
			}
		}

		// Workaround: Argo sometimes embeds one or more tool_use JSON objects inside content
		// while providing an empty/missing tool_calls array. Extract all of them in order.
		if opts.ExtractEmbeddedTools && (toolCallsPresentAndEmpty || !toolCallsExists) && len(toolCalls) == 0 && content != "" {
			extractedContent, extractedCalls, err := ExtractEmbeddedToolCalls(content, opts.ToolDefs)
			if err == nil && len(extractedCalls) > 0 {
				content = extractedContent
				toolCalls = extractedCalls
			}
		}

		return content, toolCalls, nil
	default:
		return "", nil, fmt.Errorf("unexpected Argo response type: %T", argoResp.Response)
	}
}
