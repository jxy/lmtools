//go:build integration

package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

// StreamEvent represents a parsed SSE event
type StreamEvent struct {
	Event string
	Data  interface{}
}

// validateStreamEvent parses and validates an SSE event by comparing JSON structures
// instead of raw strings. This makes tests less brittle to formatting changes.
func validateStreamEvent(t *testing.T, actual, expected string) {
	t.Helper()

	// Parse the actual event
	actualEvent := parseSSELine(t, actual)
	expectedEvent := parseSSELine(t, expected)

	// Compare event types
	if actualEvent.Event != expectedEvent.Event {
		t.Errorf("Event type mismatch\nExpected: %s\nActual: %s", expectedEvent.Event, actualEvent.Event)
		return
	}

	// Compare data if both are present
	if actualEvent.Data != nil && expectedEvent.Data != nil {
		actualJSON, err := json.Marshal(actualEvent.Data)
		if err != nil {
			t.Fatalf("Failed to marshal actual data: %v", err)
		}

		expectedJSON, err := json.Marshal(expectedEvent.Data)
		if err != nil {
			t.Fatalf("Failed to marshal expected data: %v", err)
		}

		if string(actualJSON) != string(expectedJSON) {
			t.Errorf("Event data mismatch\nExpected: %s\nActual: %s", string(expectedJSON), string(actualJSON))
		}
	}
}

// parseSSELine parses a single SSE line into event type and data
func parseSSELine(t *testing.T, line string) StreamEvent {
	t.Helper()

	line = strings.TrimSpace(line)

	// Handle event lines
	if strings.HasPrefix(line, "event: ") {
		return StreamEvent{
			Event: strings.TrimPrefix(line, "event: "),
		}
	}

	// Handle data lines
	if strings.HasPrefix(line, "data: ") {
		dataStr := strings.TrimPrefix(line, "data: ")

		// Special case for [DONE]
		if dataStr == "[DONE]" {
			return StreamEvent{
				Event: "done",
				Data:  nil,
			}
		}

		// Try to parse as JSON
		var data interface{}
		if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
			// If not valid JSON, treat as string
			return StreamEvent{
				Event: "data",
				Data:  dataStr,
			}
		}

		return StreamEvent{
			Event: "data",
			Data:  data,
		}
	}

	// Unknown format
	return StreamEvent{
		Event: "unknown",
		Data:  line,
	}
}

// parseSSEStream parses a complete SSE stream into events
func parseSSEStream(t *testing.T, stream string) []StreamEvent {
	t.Helper()

	lines := strings.Split(stream, "\n")
	var events []StreamEvent

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		// Check if this is an event line
		if strings.HasPrefix(line, "event: ") {
			event := StreamEvent{
				Event: strings.TrimPrefix(line, "event: "),
			}

			// Check if next line is data
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "data: ") {
				dataLine := strings.TrimPrefix(lines[i+1], "data: ")
				if dataLine != "[DONE]" {
					var data interface{}
					if err := json.Unmarshal([]byte(dataLine), &data); err == nil {
						event.Data = data
					} else {
						event.Data = dataLine
					}
				}
				i++ // Skip the data line
			}

			events = append(events, event)
		} else if strings.HasPrefix(line, "data: ") {
			// Standalone data line
			events = append(events, parseSSELine(t, line))
		}
	}

	return events
}

// extractEventTypes extracts just the event types from an SSE stream
func extractEventTypes(t *testing.T, stream string) []string {
	t.Helper()

	events := parseSSEStream(t, stream)
	var eventTypes []string

	for _, e := range events {
		if e.Event != "data" && e.Event != "unknown" {
			eventTypes = append(eventTypes, e.Event)
		}
	}

	return eventTypes
}

// assertContainsEvents checks that the stream contains all expected event types
func assertContainsEvents(t *testing.T, stream string, expectedEvents []string) {
	t.Helper()

	actualEvents := extractEventTypes(t, stream)
	eventMap := make(map[string]bool)

	for _, e := range actualEvents {
		eventMap[e] = true
	}

	for _, expected := range expectedEvents {
		if !eventMap[expected] {
			t.Errorf("Missing expected event: %s\nActual events: %v", expected, actualEvents)
		}
	}
}

// ValidateOpenAIStreamOutput checks the stdout against expected SSE chunks
func ValidateOpenAIStreamOutput(t *testing.T, stdout string, expectedChunks []string) {
	t.Helper()

	lines := strings.Split(stdout, "\n")
	// Maintain a cursor to enforce ordering
	startIdx := 0

	for _, exp := range expectedChunks {
		if strings.HasPrefix(exp, "data: ") {
			expData := strings.TrimSpace(strings.TrimPrefix(exp, "data: "))
			if expData == "[DONE]" {
				// Ensure a [DONE] marker exists after the current cursor
				found := false
				for i := startIdx; i < len(lines); i++ {
					if strings.TrimSpace(lines[i]) == "data: [DONE]" || strings.TrimSpace(lines[i]) == "[DONE]" {
						startIdx = i + 1
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Missing [DONE] marker in stdout")
				}
				continue
			}

			// Find a matching data line (by JSON structure) in order
			matchLine, nextIdx := findMatchingDataLineOrdered(t, lines, startIdx, expData)
			if matchLine == "" {
				t.Errorf("Could not find matching data line for expected chunk: %s", exp)
				continue
			}
			validateStreamEvent(t, matchLine, "data: "+expData)
			startIdx = nextIdx
		} else if strings.HasPrefix(exp, "event: ") {
			expEvent := strings.TrimSpace(strings.TrimPrefix(exp, "event: "))
			matchLine, nextIdx := findMatchingEventLineOrdered(lines, startIdx, expEvent)
			if matchLine == "" {
				t.Errorf("Could not find matching event line for expected event: %s", expEvent)
				continue
			}
			validateStreamEvent(t, matchLine, exp)
			startIdx = nextIdx
		}
	}
}

// ValidateAnthropicStreamOutput checks the stdout against expected Anthropic SSE events
// Similar to ValidateOpenAIStreamOutput but tailored for Anthropic's event structure
func ValidateAnthropicStreamOutput(t *testing.T, stdout string, expectedEvents []AnthropicTestEvent) {
	t.Helper()

	lines := strings.Split(stdout, "\n")
	// Maintain a cursor to enforce ordering
	startIdx := 0

	for _, exp := range expectedEvents {
		// Find the event line
		eventLine, eventIdx := findMatchingEventLineOrdered(lines, startIdx, exp.EventType)
		if eventLine == "" {
			t.Errorf("Could not find event: %s", exp.EventType)
			continue
		}

		// Find the corresponding data line (should be right after event)
		if eventIdx < len(lines) && strings.HasPrefix(lines[eventIdx], "data: ") {
			dataStr := strings.TrimSpace(strings.TrimPrefix(lines[eventIdx], "data: "))

			// If expected data is provided, validate it
			if exp.ExpectedData != nil {
				var actualData interface{}
				if err := json.Unmarshal([]byte(dataStr), &actualData); err != nil {
					t.Errorf("Failed to parse data for event %s: %v", exp.EventType, err)
					continue
				}

				// Normalize both expected and actual for comparison
				expNorm, _ := json.Marshal(exp.ExpectedData)
				actualNorm, _ := json.Marshal(actualData)

				if string(expNorm) != string(actualNorm) {
					t.Errorf("Data mismatch for event %s\nExpected: %s\nActual: %s",
						exp.EventType, string(expNorm), string(actualNorm))
				}
			}
			startIdx = eventIdx + 1
		} else {
			t.Errorf("No data line found after event: %s", exp.EventType)
			startIdx = eventIdx
		}
	}
}

// AnthropicTestEvent represents an expected Anthropic SSE event for testing
type AnthropicTestEvent struct {
	EventType    string
	ExpectedData interface{} // Optional: if nil, only checks event exists
}

// findMatchingDataLineOrdered scans stdout lines from startIdx to find a data line
// whose JSON, once parsed, matches the expected JSON (order-insensitive by re-marshal).
func findMatchingDataLineOrdered(t *testing.T, lines []string, startIdx int, expData string) (string, int) {
	t.Helper()

	// Parse expected JSON
	var expVal interface{}
	if err := json.Unmarshal([]byte(expData), &expVal); err != nil {
		// Not valid JSON in expected; fall back to string equality search
		for i := startIdx; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], "data: ") && strings.TrimSpace(strings.TrimPrefix(lines[i], "data: ")) == expData {
				return lines[i], i + 1
			}
		}
		return "", startIdx
	}
	expNorm, _ := json.Marshal(expVal)

	for i := startIdx; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "data: ") {
			continue
		}
		dataStr := strings.TrimSpace(strings.TrimPrefix(lines[i], "data: "))
		if dataStr == "[DONE]" {
			continue
		}
		var got interface{}
		if err := json.Unmarshal([]byte(dataStr), &got); err != nil {
			continue
		}
		gotNorm, _ := json.Marshal(got)
		if string(gotNorm) == string(expNorm) {
			return lines[i], i + 1
		}
	}
	return "", startIdx
}

// findMatchingEventLineOrdered scans stdout lines from startIdx to find the next matching event line.
func findMatchingEventLineOrdered(lines []string, startIdx int, event string) (string, int) {
	for i := startIdx; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "event: ") && strings.TrimSpace(strings.TrimPrefix(lines[i], "event: ")) == event {
			return lines[i], i + 1
		}
	}
	return "", startIdx
}
