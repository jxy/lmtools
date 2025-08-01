//go:build integration || e2e
// +build integration e2e

package apiproxy

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

