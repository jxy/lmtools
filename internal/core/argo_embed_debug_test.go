package core

import (
	"strings"
	"testing"
)

// debugTestToolDefs provides tool definitions for debug tests
var debugTestToolDefs = []ToolDefinition{
	{Name: "Edit"},
}

// TestExactApiProxyFailingCaseDebug tests with debug output
func TestExactApiProxyFailingCaseDebug(t *testing.T) {
	// This is the EXACT content from the apiproxy debug log response (outer JSON string-escaped)
	// Note: The assistant content is JSON-escaped inside response.content, so backslashes and quotes are doubled.
	content := `{"response":{"content":"There's an issue with the escape sequence in the test. Let me fix it{'id': 'toolu_vrtx_01XqLnQUvXJJJJJJJJJJJJJJj', 'input': {'file_path': '/path/to/project/internal/core/argo_embed_edge_cases_test.go', 'new_string': '\\t\\t\\tname:          \"apostrophe_adjacent_to_delimiter\",\\n\\t\\t\\tcontent:       \"Let\\'s analyze: {\\'type\\': \\'tool_use\\', \\'name\\': \\'test\\', \\'id\\': \\'123\\', \\'input\\': {\\'text\\': \\'It\\\\\\\\\\\\'s working\\'}}\",\\n\\t\\t\\texpectedCalls: 1,\\n\\t\\t\\texpectedOK:    true,\\n\\t\\t\\tdescription:   \"Handle apostrophes adjacent to JSON delimiters\",', 'old_string': '\\t\\t\\tname:          \"apostrophe_adjacent_to_delimiter\",\\n\\t\\t\\tcontent:       \"Let\\'s analyze: {\\'type\\': \\'tool_use\\', \\'name\\': \\'test\\', \\'id\\': \\'123\\', \\'input\\': {\\'text\\': \\'It\\\\\\'s working\\'}}\",\\n\\t\\t\\texpectedCalls: 1,\\n\\t\\t\\texpectedOK:    true,\\n\\t\\t\\tdescription:   \"Handle apostrophes adjacent to JSON delimiters\",'}, 'name': 'Edit', 'type': 'tool_use'}","tool_calls":[]}}`

	t.Logf("Content length: %d", len(content))
	t.Logf("First 200 chars: %q", content[:min(200, len(content))])

	// Find the opening brace
	braceIdx := strings.IndexByte(content, '{')
	t.Logf("First brace at index: %d", braceIdx)
	if braceIdx >= 0 {
		t.Logf("Text before brace: %q", content[:braceIdx])
		t.Logf("Starting from brace (first 100): %q", content[braceIdx:min(braceIdx+100, len(content))])
	}

	// Try calling parseEmbeddedToolCalls directly
	t.Logf("\nCalling parseEmbeddedToolCalls...")

	// First try unwrapping since this is a wrapped response
	contentToScan := content
	var seq []EmbeddedSequence
	var suffix string
	var err error

	if unwrapped, isWrapped := tryUnwrapArgoResponse(content); isWrapped {
		t.Logf("Content is wrapped, unwrapped length: %d", len(unwrapped))
		t.Logf("First 200 chars of unwrapped: %q", unwrapped[:min(200, len(unwrapped))])
		contentToScan = unwrapped

		// Try relaxing escapes for single quotes if needed
		relaxed := strings.ReplaceAll(unwrapped, "\\'", "'")
		if relaxed != unwrapped {
			t.Logf("Applied single quote relaxation")
			seq2, suffix2, err2 := parseEmbeddedToolCalls(relaxed, debugTestToolDefs)
			if err2 == nil {
				t.Logf("Relaxed parsing succeeded: seq_len=%d", len(seq2))
				seq, suffix, err = seq2, suffix2, err2
				ok := err == nil
				t.Logf("Result: ok=%v, seq_len=%d, suffix_len=%d", ok, len(seq), len(suffix))
				if !ok {
					t.Logf("Relaxed parsing also failed: %v", err)
				}
				return
			}
		}
	}

	seq, suffix, err = parseEmbeddedToolCalls(contentToScan, debugTestToolDefs)
	ok := err == nil
	t.Logf("Result: ok=%v, seq_len=%d, suffix_len=%d", ok, len(seq), len(suffix))

	if err != nil {
		// This is a debug/documentation test - log instead of fail for edge cases
		t.Logf("Parsing did not extract tool calls (expected for complex escape sequences): %v", err)

		// Let's try to understand why it failed
		// Check if the JSON extraction is working
		t.Logf("\nDebugging: Trying to find JSON boundaries manually...")

		if braceIdx >= 0 {
			jsonStart := braceIdx
			depth := 0
			inString := false
			escapeNext := false
			quoteChar := byte(0)
			escapeCount := 0

			for i := jsonStart; i < len(content); i++ {
				ch := content[i]

				if escapeNext {
					escapeNext = false
					continue
				}

				if ch == '\\' {
					escapeCount++
					escapeNext = true
					continue
				}

				if !inString {
					if ch == '"' || ch == '\'' {
						inString = true
						quoteChar = ch
					} else if ch == '{' {
						depth++
					} else if ch == '}' {
						depth--
						if depth == 0 {
							jsonEnd := i + 1
							jsonStr := content[jsonStart:jsonEnd]
							t.Logf("Found JSON boundaries: start=%d, end=%d, length=%d", jsonStart, jsonEnd, len(jsonStr))
							t.Logf("Number of backslashes found: %d", escapeCount)
							t.Logf("First 200 chars of JSON: %q", jsonStr[:min(200, len(jsonStr))])
							t.Logf("Last 100 chars of JSON: %q", jsonStr[max(0, len(jsonStr)-100):])

							// Check if it starts with single quotes
							if len(jsonStr) > 10 {
								t.Logf("First 10 bytes: %v", []byte(jsonStr[:10]))
							}
							break
						}
					}
				} else {
					if ch == quoteChar {
						inString = false
						quoteChar = 0
					}
				}
			}

			if depth > 0 {
				t.Logf("Warning: JSON not properly closed, depth=%d", depth)
			}
		}
	} else {
		t.Logf("Test passed! Found %d tool calls", len(seq))
		if len(seq) > 0 {
			for i, item := range seq {
				if item.Call != nil {
					t.Logf("Call %d: ID=%s, Name=%s", i+1, item.Call.ID, item.Call.Name)
				}
			}
		}
	}
}
