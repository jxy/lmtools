package proxy

import (
	"bufio"
	"io"
	"strings"
)

// TestSSE wraps the standard SSE scanner with Event() and Data() helpers for tests
// This is a centralized implementation to avoid duplication across test files
type TestSSE struct {
	scanner      *bufio.Scanner
	currentEvent string
	currentData  string
}

// NewTestSSEScanner creates a test SSE scanner using the standard NewSSEScanner
// This replaces the duplicate NewE2ESSEScanner implementations
func NewTestSSEScanner(r io.Reader) *TestSSE {
	return &TestSSE{
		scanner: NewSSEScanner(r),
	}
}

// Scan advances to the next SSE event
func (s *TestSSE) Scan() bool {
	s.currentEvent = ""
	s.currentData = ""

	for s.scanner.Scan() {
		line := s.scanner.Text()

		if line == "" {
			// Empty line signals end of event
			if s.currentEvent != "" || s.currentData != "" {
				return true
			}
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			s.currentEvent = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			s.currentData = strings.TrimPrefix(line, "data: ")
			if s.currentData == "[DONE]" {
				return false
			}
		}
	}

	return false
}

// Event returns the current event type
func (s *TestSSE) Event() string {
	return s.currentEvent
}

// Data returns the current data payload
func (s *TestSSE) Data() string {
	return s.currentData
}

// Err returns any error from the underlying scanner
func (s *TestSSE) Err() error {
	return s.scanner.Err()
}
