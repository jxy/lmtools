package core

import (
	"encoding/json"
)

// parseEmbeddedCall checks if a parsed JSON map represents a tool call.
// validTools: list of valid tool definitions; empty disables extraction.
// Returns the call and true if it's a valid tool call, nil and false otherwise.
func parseEmbeddedCall(raw map[string]interface{}, validTools []ToolDefinition) (*EmbeddedCall, bool) {
	// Try Anthropic format first (more common in Argo responses)
	if call := parseAnthropicEmbedded(raw, validTools); call != nil {
		return call, true
	}
	// Try OpenAI format as fallback
	if call := parseOpenAIEmbedded(raw, validTools); call != nil {
		return call, true
	}
	return nil, false
}

// parseAnthropicEmbedded attempts to parse raw JSON as an Anthropic-style tool_use block.
// Returns nil if the JSON doesn't match Anthropic format.
// Validates required fields: type="tool_use", name (non-empty string), id (non-empty string), input (object)
// Also validates tool name against validTools if provided.
func parseAnthropicEmbedded(raw map[string]interface{}, validTools []ToolDefinition) *EmbeddedCall {
	// Validate type field is exactly "tool_use"
	t, ok := raw["type"].(string)
	if !ok || t != "tool_use" {
		return nil
	}

	// Validate name field exists and is a non-empty string
	name, ok := raw["name"].(string)
	if !ok || name == "" {
		return nil
	}

	// Validate tool name against whitelist if provided
	if !IsValidToolName(name, validTools) {
		return nil
	}

	// Validate id field exists and is a non-empty string
	id, ok := raw["id"].(string)
	if !ok || id == "" {
		return nil
	}

	// Validate input field exists and is an object (map)
	input, ok := raw["input"].(map[string]interface{})
	if !ok {
		// input field must be present and must be an object
		return nil
	}

	// Marshal the input to JSON
	var argsJSON json.RawMessage
	if b, err := marshalPreservingEmptyArrays(input); err == nil {
		argsJSON = json.RawMessage(b)
	} else {
		// If we can't marshal the input, it's not a valid tool call
		return nil
	}

	return &EmbeddedCall{
		Style:    "anthropic",
		ID:       id,
		Name:     name,
		ArgsJSON: argsJSON,
	}
}

// parseOpenAIEmbedded attempts to parse raw JSON as an OpenAI-style function call.
// Returns nil if the JSON doesn't match OpenAI format.
// Validates required fields: name (non-empty string), arguments (object or JSON string)
// Also validates tool name against validTools if provided.
func parseOpenAIEmbedded(raw map[string]interface{}, validTools []ToolDefinition) *EmbeddedCall {
	// Check for nested function object first (standard OpenAI format)
	fnRaw, hasFunctionKey := raw["function"].(map[string]interface{})
	if hasFunctionKey {
		// Standard OpenAI format with nested "function" object
		name := GetString(fnRaw, "name")
		if name == "" {
			return nil
		}

		// Validate tool name against whitelist if provided
		if !IsValidToolName(name, validTools) {
			return nil
		}

		// Validate arguments field exists and is valid
		var argsJSON json.RawMessage
		switch v := fnRaw["arguments"].(type) {
		case string:
			// Arguments as JSON string - validate it's not empty
			if v == "" {
				return nil
			}
			argsJSON = json.RawMessage(v)
		case map[string]interface{}:
			// Arguments as object - marshal to JSON
			if b, err := marshalPreservingEmptyArrays(v); err == nil {
				argsJSON = json.RawMessage(b)
			} else {
				return nil
			}
		default:
			// arguments field is missing or has invalid type
			return nil
		}

		id := GetString(raw, "id")
		return &EmbeddedCall{
			Style:    "openai",
			ID:       id,
			Name:     name,
			ArgsJSON: argsJSON,
		}
	}

	// Alternative format: top-level name and arguments (some Argo responses)
	// This format MUST have both name and arguments fields
	name, hasName := raw["name"].(string)
	if !hasName || name == "" {
		return nil
	}

	// Validate tool name against whitelist if provided
	if !IsValidToolName(name, validTools) {
		return nil
	}

	// Check for arguments field - it must be present
	argVal, hasArguments := raw["arguments"]
	if !hasArguments {
		return nil
	}

	var argsJSON json.RawMessage
	switch v := argVal.(type) {
	case string:
		// Arguments as JSON string - validate it's not empty
		if v == "" {
			return nil
		}
		argsJSON = json.RawMessage(v)
	case map[string]interface{}:
		// Arguments as object - marshal to JSON
		if b, err := marshalPreservingEmptyArrays(v); err == nil {
			argsJSON = json.RawMessage(b)
		} else {
			return nil
		}
	default:
		// arguments has invalid type (null, number, boolean, array, etc.)
		return nil
	}

	id, _ := raw["id"].(string)
	return &EmbeddedCall{
		Style:    "openai",
		ID:       id,
		Name:     name,
		ArgsJSON: argsJSON,
	}
}

// marshalPreservingEmptyArrays marshals a map to JSON while preserving empty arrays.
// Go's json.Marshal converts empty []interface{} to null in certain contexts,
// so we need to ensure empty arrays stay as [] not null.
func marshalPreservingEmptyArrays(data interface{}) (json.RawMessage, error) {
	// Fix empty arrays recursively before marshaling
	fixed := fixEmptyArrays(data)
	return json.Marshal(fixed)
}

// fixEmptyArrays recursively walks through data structures and ensures
// empty slices are properly initialized (not nil) so they marshal as [] not null.
func fixEmptyArrays(v interface{}) interface{} {
	switch val := v.(type) {
	case []interface{}:
		if len(val) == 0 {
			// Convert nil slice to empty slice
			return []interface{}{}
		}
		// Recursively fix nested arrays
		for i, item := range val {
			val[i] = fixEmptyArrays(item)
		}
		return val
	case map[string]interface{}:
		if val == nil {
			return val
		}
		// Recursively fix map values
		for k, v := range val {
			val[k] = fixEmptyArrays(v)
		}
		return val
	default:
		return v
	}
}
