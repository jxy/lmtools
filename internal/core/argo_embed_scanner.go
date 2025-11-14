package core

import (
	"lmtools/internal/logger"
	"strings"
)

// scanJSONObjects scans content for JSON objects and identifies tool calls
// Returns sequences with position information needed for suffix extraction
func scanJSONObjects(content string, validTools []ToolDefinition) []EmbeddedSequence {
	var sequences []EmbeddedSequence
	currentPos := 0
	lastExtractedEnd := 0

	for currentPos < len(content) {
		// Find next potential JSON object
		jsonStart := findNextJSONStart(content, currentPos)
		if jsonStart < 0 {
			break
		}

		// Try to parse and validate JSON object
		parsedMap, jsonEnd, ok := parseJSONObjectAt(content, jsonStart)
		if !ok {
			currentPos = jsonStart + 1
			continue
		}

		// Check if it's a tool call
		call, isToolCall := parseEmbeddedCall(parsedMap, validTools)
		if !isToolCall || call == nil {
			currentPos = jsonStart + 1
			continue
		}

		// Found a tool call - extract prefix and add to sequences
		prefix := content[lastExtractedEnd:jsonStart]
		seq := EmbeddedSequence{
			Prefix:  prefix,
			Call:    call,
			jsonEnd: jsonEnd + 1, // Store the end position for suffix extraction
		}
		sequences = append(sequences, seq)

		lastExtractedEnd = jsonEnd + 1
		currentPos = lastExtractedEnd
	}

	return sequences
}

// findNextJSONStart finds the next '{' character starting from pos
func findNextJSONStart(content string, pos int) int {
	idx := strings.IndexByte(content[pos:], '{')
	if idx < 0 {
		return -1
	}
	return pos + idx
}

// parseJSONObjectAt attempts to parse a JSON object at the given position
func parseJSONObjectAt(content string, jsonStart int) (map[string]interface{}, int, bool) {
	// Check size limits
	if len(content)-jsonStart > MaxJSONObjectSize {
		logger.GetLogger().Debugf("parseEmbeddedToolCalls: limiting parse window at position %d", jsonStart)
	}

	return parseLooseJSONObjectAt(content, jsonStart)
}

// extractSuffix extracts the trailing content after the last tool call
func extractSuffix(content string, sequences []EmbeddedSequence) string {
	if len(sequences) == 0 {
		return ""
	}

	// Get the end position from the last sequence
	lastSeq := sequences[len(sequences)-1]
	if lastSeq.jsonEnd > 0 && lastSeq.jsonEnd < len(content) {
		return content[lastSeq.jsonEnd:]
	}

	return ""
}
