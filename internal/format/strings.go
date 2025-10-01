package format

import (
	"encoding/json"
	"strings"
)

// Format constants for display
const (
	MaxArgPreview        = 20  // Maximum characters for tool argument preview
	MaxResultPreview     = 30  // Maximum characters for tool result preview
	MaxToolOutputDisplay = 500 // Maximum characters to display for tool output
)

// Truncate truncates a string to a maximum length
func Truncate(s string, maxLen int) string {
	// Remove newlines and extra spaces
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")

	if len(s) <= maxLen {
		return s
	}

	return s[:maxLen-3] + "..."
}

// PrettyJSONArgs pretty-prints JSON arguments with proper indentation
func PrettyJSONArgs(args json.RawMessage, indent string) string {
	if len(args) == 0 {
		return ""
	}

	var prettyArgs interface{}
	if err := json.Unmarshal(args, &prettyArgs); err != nil {
		// If unmarshaling fails, return raw string
		return string(args)
	}

	formatted, err := json.MarshalIndent(prettyArgs, indent, "  ")
	if err != nil {
		// If formatting fails, return raw string
		return string(args)
	}

	return string(formatted)
}
