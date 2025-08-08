package proxy

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitTextForStreamingEdgeCasesExtended(t *testing.T) {
	// Additional edge cases from review
	edgeCases := []struct {
		name      string
		text      string
		chunkSize int
		wantPanic bool
	}{
		{
			name:      "negative chunk size",
			text:      "Hello world",
			chunkSize: -1,
			wantPanic: false, // Should handle gracefully with default
		},
		{
			name:      "zero chunk size",
			text:      "Hello world",
			chunkSize: 0,
			wantPanic: false, // Should handle gracefully with default
		},
		{
			name:      "chunk size larger than text",
			text:      "Hi",
			chunkSize: 100,
		},
		{
			name:      "single ASCII character",
			text:      "A",
			chunkSize: 10,
		},
		{
			name:      "single multibyte character",
			text:      "\u4e16",
			chunkSize: 1,
		},
		{
			name:      "combining characters",
			text:      "e\u0301", // é as e + combining acute
			chunkSize: 1,
		},
		{
			name:      "zero-width joiners",
			text:      "\U0001F468\u200d\U0001F469\u200d\U0001F467\u200d\U0001F466", // Family emoji with ZWJ
			chunkSize: 4,
		},
		{
			name:      "long string of spaces",
			text:      "     ",
			chunkSize: 2,
		},
		{
			name:      "text ending with space",
			text:      "Hello world ",
			chunkSize: 5,
		},
		{
			name:      "only non-ASCII characters",
			text:      "\u4e16\u754c\u4f60\u597d",
			chunkSize: 3,
		},
		{
			name:      "very small chunk with emoji",
			text:      "\U0001F600\U0001F603\U0001F604",
			chunkSize: 1,
		},
		{
			name:      "RTL text (Arabic)",
			text:      "\u0645\u0631\u062d\u0628\u0627",
			chunkSize: 2,
		},
		{
			name:      "mixed LTR and RTL",
			text:      "Hello \u0645\u0631\u062d\u0628\u0627 World",
			chunkSize: 5,
		},
	}

	for _, tc := range edgeCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test that it doesn't panic
			var chunks []string
			panicked := false
			func() {
				defer func() {
					if r := recover(); r != nil {
						panicked = true
					}
				}()
				chunks = splitTextForStreaming(tc.text, tc.chunkSize)
			}()

			if panicked && !tc.wantPanic {
				t.Errorf("unexpected panic for %s", tc.name)
			}
			if !panicked && tc.wantPanic {
				t.Errorf("expected panic for %s but didn't panic", tc.name)
			}

			// If it didn't panic, verify the output
			if !panicked && len(tc.text) > 0 {
				reconstructed := strings.Join(chunks, "")
				if reconstructed != tc.text {
					t.Errorf("failed to reconstruct text: got %q, want %q", reconstructed, tc.text)
				}

				// Verify all chunks are valid UTF-8
				for i, chunk := range chunks {
					if !isValidUTF8(chunk) {
						t.Errorf("chunk %d contains invalid UTF-8: %q", i, chunk)
					}
				}

				// Verify no empty chunks (except for empty input)
				for i, chunk := range chunks {
					if chunk == "" {
						t.Errorf("chunk %d is empty", i)
					}
				}
			}
		})
	}
}

// Helper function to check if a string is valid UTF-8
func isValidUTF8(s string) bool {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return false
		}
		i += size
	}
	return true
}

// Test performance with large texts
func TestSplitTextForStreamingPerformance(t *testing.T) {
	// Generate a large mixed text
	var sb strings.Builder
	for i := 0; i < 1000; i++ {
		sb.WriteString("Hello \u4e16\u754c ")
		sb.WriteString("\U0001F600\U0001F603\U0001F604 ")
		sb.WriteString("Testing performance ")
	}
	largeText := sb.String()

	// Test with various chunk sizes
	chunkSizes := []int{10, 20, 50, 100, 200}

	for _, chunkSize := range chunkSizes {
		t.Run(fmt.Sprintf("chunkSize_%d", chunkSize), func(t *testing.T) {
			chunks := splitTextForStreaming(largeText, chunkSize)

			// Verify reconstruction
			reconstructed := strings.Join(chunks, "")
			if reconstructed != largeText {
				t.Errorf("failed to reconstruct large text with chunk size %d", chunkSize)
			}

			// Verify all chunks are valid UTF-8
			for i, chunk := range chunks {
				if !isValidUTF8(chunk) {
					t.Errorf("chunk %d contains invalid UTF-8 with chunk size %d", i, chunkSize)
				}
			}

			// Check that chunks are reasonably sized
			for i, chunk := range chunks {
				if i < len(chunks)-1 { // All but last chunk
					// Chunks should be close to target size (within 50%)
					if len(chunk) > chunkSize*3/2 {
						t.Errorf("chunk %d is too large (%d bytes) for target size %d", i, len(chunk), chunkSize)
					}
				}
			}
		})
	}
}
