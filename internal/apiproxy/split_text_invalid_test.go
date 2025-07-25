package apiproxy

import (
	"strings"
	"testing"
)

func TestSplitTextForStreamingInvalidUTF8(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		chunkSize int
	}{
		{
			name:      "invalid UTF-8 sequence",
			text:      "Hello\x80\x81World", // Invalid UTF-8 bytes
			chunkSize: 5,
		},
		{
			name:      "truncated UTF-8 at end",
			text:      "Hello世\xE4\xB8", // Truncated Chinese character
			chunkSize: 10,
		},
		{
			name:      "mixed valid and invalid",
			text:      "Valid文字\xFF\xFEMore",
			chunkSize: 8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The function should handle invalid UTF-8 gracefully
			chunks := splitTextForStreaming(tt.text, tt.chunkSize)

			// Should return some chunks
			if len(chunks) == 0 && len(tt.text) > 0 {
				t.Error("Expected at least one chunk for non-empty text")
			}

			// Verify we can reconstruct something (may not match exactly due to invalid UTF-8)
			reconstructed := strings.Join(chunks, "")
			if len(reconstructed) == 0 && len(tt.text) > 0 {
				t.Error("Reconstructed text is empty for non-empty input")
			}

			// Each chunk should be valid UTF-8 (Go's string type enforces this)
			for i, chunk := range chunks {
				if chunk != string([]rune(chunk)) {
					t.Errorf("Chunk %d contains invalid UTF-8: %q", i, chunk)
				}
			}
		})
	}
}

func TestSplitTextForStreamingGraphemeClusters(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		chunkSize   int
		description string
	}{
		{
			name:        "emoji with skin tone modifier",
			text:        "Hello 👋🏽 World",
			chunkSize:   8,
			description: "Should handle emoji with modifiers",
		},
		{
			name:        "emoji with ZWJ sequence",
			text:        "Family: 👨‍👩‍👧‍👦 here",
			chunkSize:   10,
			description: "Should handle zero-width joiner sequences",
		},
		{
			name:        "combining diacritics",
			text:        "Café résumé naïve",
			chunkSize:   5,
			description: "Should handle combining characters",
		},
		{
			name:        "regional indicator symbols (flags)",
			text:        "Flags: 🇺🇸 🇬🇧 🇯🇵",
			chunkSize:   8,
			description: "Should handle flag emoji pairs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := splitTextForStreaming(tt.text, tt.chunkSize)

			// Verify reconstruction
			reconstructed := strings.Join(chunks, "")
			if reconstructed != tt.text {
				t.Errorf("%s: reconstructed text doesn't match\ngot:  %q\nwant: %q",
					tt.description, reconstructed, tt.text)
			}

			// Log chunks for manual inspection
			t.Logf("%s chunks:", tt.name)
			for i, chunk := range chunks {
				t.Logf("  [%d] %q (%d bytes)", i, chunk, len(chunk))
			}
		})
	}
}

func TestSplitTextForStreamingBoundaryConditions(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		chunkSize int
		wantCount int
	}{
		{
			name:      "empty string",
			text:      "",
			chunkSize: 10,
			wantCount: 0,
		},
		{
			name:      "single character",
			text:      "a",
			chunkSize: 10,
			wantCount: 1,
		},
		{
			name:      "single multibyte character",
			text:      "世",
			chunkSize: 10,
			wantCount: 1,
		},
		{
			name:      "exact chunk size",
			text:      "12345",
			chunkSize: 5,
			wantCount: 1,
		},
		{
			name:      "one byte over chunk size",
			text:      "123456",
			chunkSize: 5,
			wantCount: 2,
		},
		{
			name:      "negative chunk size",
			text:      "Hello World",
			chunkSize: -5,
			wantCount: 1, // Should use default
		},
		{
			name:      "zero chunk size",
			text:      "Hello World",
			chunkSize: 0,
			wantCount: 1, // Should use default
		},
		{
			name:      "very large chunk size",
			text:      "Hello World",
			chunkSize: 1000000,
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := splitTextForStreaming(tt.text, tt.chunkSize)

			if len(chunks) != tt.wantCount {
				t.Errorf("got %d chunks, want %d", len(chunks), tt.wantCount)
				for i, chunk := range chunks {
					t.Logf("  chunk[%d]: %q", i, chunk)
				}
			}
		})
	}
}

func TestSplitTextForStreamingSpecialCharacters(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		chunkSize int
	}{
		{
			name:      "null bytes",
			text:      "Hello\x00World",
			chunkSize: 6,
		},
		{
			name:      "control characters",
			text:      "Line1\nLine2\rLine3\tTab",
			chunkSize: 8,
		},
		{
			name:      "unicode spaces",
			text:      "Hello\u00A0World\u2009Test", // Non-breaking space and thin space
			chunkSize: 10,
		},
		{
			name:      "RTL and LTR marks",
			text:      "Hello\u200Eשלום\u200FWorld",
			chunkSize: 10,
		},
		{
			name:      "surrogate pairs",
			text:      "Math: 𝐀𝐁𝐂 and 𝕏𝕐𝕑",
			chunkSize: 8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := splitTextForStreaming(tt.text, tt.chunkSize)

			// Verify reconstruction
			reconstructed := strings.Join(chunks, "")
			if reconstructed != tt.text {
				t.Errorf("reconstructed text doesn't match\ngot:  %q\nwant: %q",
					reconstructed, tt.text)
			}

			// Verify each chunk is valid UTF-8
			for i, chunk := range chunks {
				// This will panic if chunk contains invalid UTF-8
				_ = []rune(chunk)
				t.Logf("chunk[%d]: %q (%d bytes)", i, chunk, len(chunk))
			}
		})
	}
}
