// Package core provides core functionality for the lmtools application.
package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/logger"
)

// DESIGN NOTE: Embedded Tool Call Format Support
//
// This module handles extraction of tool calls that Argo embeds directly in content strings
// rather than in the tool_calls array. We support both Anthropic and OpenAI formats.
//
// The extraction process:
// 1. Scan the content for JSON objects (starting with '{')
// 2. Parse each JSON object using the loose parser (handles Python-style syntax)
// 3. Check if it's a valid tool call (Anthropic or OpenAI format)
// 4. Extract the tool calls and reconstruct the content without them
//
// The module is split into focused sub-modules:
// - argo_embed_scanner.go: Scanning and finding JSON objects in content
// - argo_embed_parser.go: Parsing embedded tool calls (Anthropic/OpenAI formats)
// - argo_embed_builder.go: Building final content and tool calls
// - argo_embed.go: Main API and coordination

const (
	// MaxJSONObjectSize limits the size of a single JSON object we'll attempt to parse
	MaxJSONObjectSize = 256 * 1024 // 256KB
	// MaxContentSize limits the total content size we'll scan
	MaxContentSize = 1024 * 1024 // 1MB
)

// IsValidToolName checks if a tool name exists in the list of valid tool definitions.
// If validTools is nil or empty, returns false to avoid extracting unrequested tools.
func IsValidToolName(name string, validTools []ToolDefinition) bool {
	if len(validTools) == 0 {
		return false
	}

	for _, tool := range validTools {
		if tool.Name == name {
			return true
		}
	}

	logger.GetLogger().Debugf("IsValidToolName: rejected unknown tool name: %s", name)
	return false
}

// EmbeddedCall represents a tool call embedded in content by Argo
type EmbeddedCall struct {
	Style    string          // "anthropic" or "openai"
	ID       string          // Tool call ID
	Name     string          // Tool/function name
	ArgsJSON json.RawMessage // Normalized marshaled arguments
	Trimmed  string          // Content prefix before the JSON block; colon trimmed
}

// EmbeddedSequence represents an ordered pair of prefix text and a following call.
// Prefix preserves original formatting.
type EmbeddedSequence struct {
	Prefix  string
	Call    *EmbeddedCall
	jsonEnd int // End position of the JSON object in the original content (internal use only)
}

// ExtractEmbeddedToolCallsWithSequence extracts embedded tool calls and returns the raw sequences.
// This is a specialized function for the proxy layer that needs access to the prefix/suffix structure.
// Parameters:
// - content: the text content to scan
// - validTools: list of valid tool definitions; empty disables extraction
// Returns:
// - sequences: array of embedded sequences with prefix text and tool calls
// - suffix: remaining text after all tool calls
// - error: nil if successful (even if no tool calls found)
func ExtractEmbeddedToolCallsWithSequence(content string, validTools []ToolDefinition) ([]EmbeddedSequence, string, error) {
	if content == "" {
		return nil, content, nil
	}

	// Validate content size
	if !validateContentSize(content) {
		return nil, content, nil
	}

	// Unwrap nested response if needed
	contentToProcess := unwrapIfNeeded(content)

	// Parse embedded tool calls
	return parseEmbeddedToolCalls(contentToProcess, validTools)
}

// ExtractEmbeddedToolCalls extracts tool calls embedded in content strings by Argo.
// validTools: list of valid tool definitions; empty disables extraction.
// Returns:
// - content: The text content with tool call JSON extracted
// - toolCalls: Extracted tool calls in standard format
// - error: nil if successful (even if no tool calls found)
func ExtractEmbeddedToolCalls(content string, validTools []ToolDefinition) (string, []ToolCall, error) {
	if content == "" {
		return content, nil, nil
	}

	// Step 1: Validate content size
	if !validateContentSize(content) {
		return content, nil, nil
	}

	// Step 2: Unwrap nested response if needed
	contentToProcess := unwrapIfNeeded(content)

	// Step 3: Scan and parse embedded tool calls
	seq, suffix, err := scanForToolCalls(contentToProcess, validTools)
	if err != nil {
		// No embedded tool calls found - return original content unchanged
		logger.GetLogger().Debugf("ExtractEmbeddedToolCalls: no tool calls found: %v", err)
		return content, nil, nil
	}

	// Step 4: Build final content and tool calls
	extractedContent, toolCalls := buildContentAndToolsFromEmbedded(seq, suffix)
	logger.GetLogger().Debugf("ExtractEmbeddedToolCalls: extracted %d tool calls", len(toolCalls))

	return extractedContent, toolCalls, nil
}

// validateContentSize checks if content is within size limits for processing
func validateContentSize(content string) bool {
	if len(content) > MaxContentSize {
		logger.GetLogger().Debugf("ExtractEmbeddedToolCalls: content too large (%d bytes), skipping", len(content))
		return false
	}
	return true
}

// unwrapIfNeeded checks if content is wrapped in Argo response structure and unwraps it
func unwrapIfNeeded(content string) string {
	if unwrapped, isWrapped := tryUnwrapArgoResponse(content); isWrapped {
		logger.GetLogger().Debugf("ExtractEmbeddedToolCalls: unwrapped nested response structure")
		return unwrapped
	}
	return content
}

// scanForToolCalls is a renamed version of parseEmbeddedToolCalls for clarity
func scanForToolCalls(content string, validTools []ToolDefinition) ([]EmbeddedSequence, string, error) {
	return parseEmbeddedToolCalls(content, validTools)
}

// tryUnwrapArgoResponse attempts to unwrap content from a nested Argo response structure.
// Returns (unwrapped content, true) if successful, or ("", false) if not wrapped.
func tryUnwrapArgoResponse(content string) (string, bool) {
	var outer map[string]interface{}
	if err := json.Unmarshal([]byte(content), &outer); err != nil {
		return "", false
	}

	resp, ok := outer["response"].(map[string]interface{})
	if !ok {
		return "", false
	}

	s, ok := resp["content"].(string)
	if !ok {
		return "", false
	}

	// Check if tool_calls is present and empty (or missing)
	if tc, present := resp["tool_calls"]; present && !IsEmptyCollection(tc) {
		return "", false
	}

	return s, true
}

// parseEmbeddedToolCalls scans content for embedded tool call JSON objects.
// validTools: list of valid tool definitions; empty disables extraction.
// Returns a sequence of (prefix, call) pairs and the trailing suffix.
// Returns an error if no embedded calls are found.
func parseEmbeddedToolCalls(content string, validTools []ToolDefinition) ([]EmbeddedSequence, string, error) {
	if len(content) > MaxContentSize {
		return nil, "", fmt.Errorf("content too large: %d bytes (max %d)", len(content), MaxContentSize)
	}

	sequences := scanJSONObjects(content, validTools)

	if len(sequences) == 0 {
		return nil, "", errors.New("no embedded tool calls found")
	}

	// Extract trailing suffix after the last tool call
	suffix := extractSuffix(content, sequences)

	logger.GetLogger().Debugf("parseEmbeddedToolCalls: found %d tool calls (Argo workaround path)", len(sequences))
	return sequences, suffix, nil
}
