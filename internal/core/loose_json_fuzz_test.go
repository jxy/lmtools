//go:build go1.18

package core

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzParseLooseJSONObjectAt tests the loose JSON parser with fuzzing to ensure
// it handles malformed inputs gracefully without panicking.
func FuzzParseLooseJSONObjectAt(f *testing.F) {
	// Add seed corpus with various edge cases
	f.Add(`{"key": "value"}`, 0)
	f.Add(`{'key': 'value'}`, 0)
	f.Add(`{"key": True}`, 0)
	f.Add(`{"key": False}`, 0)
	f.Add(`{"key": None}`, 0)
	f.Add(`{"key": "value",}`, 0)    // Trailing comma
	f.Add(`{"key": "val\nue"}`, 0)   // Escape sequence
	f.Add(`{"key": "val\u0041"}`, 0) // Unicode escape
	f.Add(`{"nested": {"inner": "value"}}`, 0)
	f.Add(`{"array": [1, 2, 3]}`, 0)
	f.Add(`{"mixed": [1, "two", True, None]}`, 0)
	f.Add(`{"empty": {}}`, 0)
	f.Add(`{"empty_array": []}`, 0)
	f.Add(`{"special": "\"\\/\b\f\n\r\t"}`, 0)
	f.Add(`{`, 0)                                                           // Incomplete
	f.Add(`{"unclosed": "string`, 0)                                        // Unclosed string
	f.Add(`{"key": }`, 0)                                                   // Missing value
	f.Add(`{: "value"}`, 0)                                                 // Missing key
	f.Add(`{"key" "value"}`, 0)                                             // Missing colon
	f.Add(`{"key": "value" "key2": "value2"}`, 0)                           // Missing comma
	f.Add(`{"key": "\xc3\x28"}`, 0)                                         // Invalid UTF-8
	f.Add(`{"key": "\uXXXX"}`, 0)                                           // Invalid unicode escape
	f.Add(`{"key": "\u"}`, 0)                                               // Incomplete unicode escape
	f.Add(`{"key": "\u12"}`, 0)                                             // Incomplete unicode escape
	f.Add(`{"key": "\u123"}`, 0)                                            // Incomplete unicode escape
	f.Add(strings.Repeat(`{"a":`, 1000)+`"b"`+strings.Repeat(`}`, 1000), 0) // Deep nesting
	f.Add(strings.Repeat(`"a"`, 10000), 0)                                  // Large input

	f.Fuzz(func(t *testing.T, input string, offset int) {
		// Ensure offset is within bounds
		if offset < 0 || offset >= len(input) {
			offset = 0
		}

		// The parser should never panic, regardless of input
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Parser panicked on input: %q at offset %d: %v", input, offset, r)
			}
		}()

		// Call the parser
		result, endIdx, ok := parseLooseJSONObjectAt(input, offset)

		// Validate the results
		if ok {
			// If parsing succeeded, validate the result
			if result == nil {
				t.Errorf("Parser returned ok=true but nil result for input: %q", input)
			}
			if endIdx < offset {
				t.Errorf("Parser returned endIdx (%d) < offset (%d) for input: %q", endIdx, offset, input)
			}
			if endIdx > len(input) {
				t.Errorf("Parser returned endIdx (%d) > len(input) (%d) for input: %q", endIdx, len(input), input)
			}

			// Verify the parsed segment is valid UTF-8
			if offset < len(input) && endIdx <= len(input) {
				segment := input[offset:endIdx]
				if !utf8.ValidString(segment) {
					t.Errorf("Parser accepted invalid UTF-8 segment: %q", segment)
				}
			}
		} else {
			// If parsing failed, result should be nil
			if result != nil {
				t.Errorf("Parser returned ok=false but non-nil result for input: %q", input)
			}
		}
	})
}
