package session

import (
	"testing"
)

func TestIsMessageReference(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		expected bool
	}{
		// Session IDs (should return false)
		{
			name:     "simple session ID",
			id:       "0001",
			expected: false,
		},
		{
			name:     "sibling session ID",
			id:       "0001.s.0002",
			expected: false,
		},
		{
			name:     "nested sibling session ID",
			id:       "0001/0002.s.0003",
			expected: false,
		},
		{
			name:     "deeply nested sibling session ID",
			id:       "0001/0002.s.0003/0004.s.0005",
			expected: false,
		},

		// Message IDs (should return true)
		{
			name:     "simple message ID",
			id:       "0001/0002",
			expected: true,
		},
		{
			name:     "message ID in sibling session",
			id:       "0001/0002.s.0003/0004",
			expected: true,
		},
		{
			name:     "message ID with longer hex",
			id:       "0001/00002",
			expected: true,
		},
		{
			name:     "message ID with 8 char hex",
			id:       "0001/00000002",
			expected: true,
		},

		// Edge cases
		{
			name:     "empty string",
			id:       "",
			expected: false,
		},
		{
			name:     "just slash",
			id:       "/",
			expected: false,
		},
		{
			name:     "ending with slash",
			id:       "0001/",
			expected: false,
		},
		{
			name:     "invalid hex in message position",
			id:       "0001/gggg",
			expected: false,
		},
		{
			name:     "too short hex in message position",
			id:       "0001/123",
			expected: false,
		},
		{
			name:     "too long hex in message position",
			id:       "0001/123456789",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsMessageReference(tt.id)
			if result != tt.expected {
				t.Errorf("isMessageReference(%q) = %v, want %v", tt.id, result, tt.expected)
			}
		})
	}
}

func TestIsValidMessageID(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		expected bool
	}{
		// Valid message IDs
		{
			name:     "4 char hex",
			id:       "0001",
			expected: true,
		},
		{
			name:     "4 char hex with letters",
			id:       "a1b2",
			expected: true,
		},
		{
			name:     "5 char hex",
			id:       "00001",
			expected: true,
		},
		{
			name:     "8 char hex",
			id:       "00000001",
			expected: true,
		},

		// Invalid message IDs
		{
			name:     "too short",
			id:       "123",
			expected: false,
		},
		{
			name:     "too long",
			id:       "123456789",
			expected: false,
		},
		{
			name:     "contains uppercase",
			id:       "00A1",
			expected: false,
		},
		{
			name:     "contains non-hex",
			id:       "00g1",
			expected: false,
		},
		{
			name:     "contains dot",
			id:       "00.1",
			expected: false,
		},
		{
			name:     "empty string",
			id:       "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsValidMessageID(tt.id)
			if result != tt.expected {
				t.Errorf("isValidMessageID(%q) = %v, want %v", tt.id, result, tt.expected)
			}
		})
	}
}
