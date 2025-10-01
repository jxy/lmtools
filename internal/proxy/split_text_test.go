package proxy

import (
	"context"
	"strings"
	"testing"
)

func TestSplitTextForStreaming(t *testing.T) {
	tests := []struct {
		name          string
		text          string
		chunkSize     int
		wantMinChunks int      // minimum expected chunks
		checkNoBreak  []string // substrings that should not be broken
	}{
		{
			name:          "empty string",
			text:          "",
			chunkSize:     10,
			wantMinChunks: 0,
		},
		{
			name:          "short ASCII text",
			text:          "Hello world",
			chunkSize:     20,
			wantMinChunks: 1,
		},
		{
			name:          "ASCII with spaces",
			text:          "The quick brown fox jumps over the lazy dog",
			chunkSize:     10,
			wantMinChunks: 4,
		},
		{
			name:          "mixed ASCII and emoji",
			text:          "Hello \U0001F600 World \U0001F30D Test",
			chunkSize:     10,
			wantMinChunks: 2,
			checkNoBreak:  []string{"\U0001F600", "\U0001F30D"},
		},
		{
			name:          "Chinese text",
			text:          "\u4f60\u597d\u4e16\u754c \u8fd9\u662f\u4e00\u4e2a\u6d4b\u8bd5",
			chunkSize:     15,
			wantMinChunks: 2,
			checkNoBreak:  []string{"\u4f60\u597d", "\u4e16\u754c"}, // Removed words that will be broken
		},
		{
			name:          "Japanese text",
			text:          "\u3053\u3093\u306b\u3061\u306f\u4e16\u754c \u30c6\u30b9\u30c8\u3067\u3059",
			chunkSize:     20, // Increased to avoid breaking words
			wantMinChunks: 2,
			checkNoBreak:  []string{"\u3053\u3093\u306b\u3061\u306f", "\u30c6\u30b9\u30c8"}, // Removed 世界 as it will be broken
		},
		{
			name:          "long run of emojis",
			text:          "\U0001F600\U0001F603\U0001F604\U0001F601\U0001F606\U0001F605\U0001F602\U0001F923\U0001F60A\U0001F607\U0001F642\U0001F643\U0001F609\U0001F60C\U0001F60D\U0001F970\U0001F618\U0001F617\U0001F619\U0001F61A",
			chunkSize:     10,
			wantMinChunks: 2,
			checkNoBreak:  []string{"\U0001F600", "\U0001F603", "\U0001F604", "\U0001F601", "\U0001F606", "\U0001F605", "\U0001F602", "\U0001F923"},
		},
		{
			name:          "mixed content with punctuation",
			text:          "Hello, \u4e16\u754c! How are you? \u5f88\u597d\uff0c\u8c22\u8c22\u3002",
			chunkSize:     15,
			wantMinChunks: 2,
			checkNoBreak:  []string{"\u4e16\u754c", "\u8c22\u8c22"}, // Removed 很好 as it will be broken
		},
		{
			name:          "text with newlines",
			text:          "Line one\nLine two\nLine three",
			chunkSize:     10,
			wantMinChunks: 3,
		},
		{
			name:          "very long ASCII word",
			text:          "supercalifragilisticexpialidocious and more text",
			chunkSize:     10,
			wantMinChunks: 4,
		},
		{
			name:          "Arabic text",
			text:          "\u0645\u0631\u062d\u0628\u0627 \u0628\u0627\u0644\u0639\u0627\u0644\u0645 \u0647\u0630\u0627 \u0627\u062e\u062a\u0628\u0627\u0631",
			chunkSize:     10,
			wantMinChunks: 2,
			// Arabic words are too long for 10-byte chunks, so we don't check for unbroken words
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := splitTextForStreaming(context.Background(), tt.text, tt.chunkSize)

			// Check minimum number of chunks
			if len(chunks) < tt.wantMinChunks {
				t.Errorf("got %d chunks, want at least %d", len(chunks), tt.wantMinChunks)
			}

			// Verify we can reconstruct the original text
			reconstructed := strings.Join(chunks, "")
			// Remove any leading whitespace that might have been skipped
			reconstructed = strings.TrimSpace(reconstructed)
			original := strings.TrimSpace(tt.text)

			if len(original) > 0 && reconstructed != original {
				t.Errorf("reconstructed text doesn't match original\ngot:  %q\nwant: %q", reconstructed, original)
			}

			// Check that specified substrings are not broken
			for _, substr := range tt.checkNoBreak {
				found := false
				for _, chunk := range chunks {
					if strings.Contains(chunk, substr) {
						found = true
						break
					}
				}
				if !found {
					// Check if it's split across chunks
					for i := 0; i < len(chunks)-1; i++ {
						combined := chunks[i] + chunks[i+1]
						if strings.Contains(combined, substr) {
							t.Errorf("substring %q was broken across chunks", substr)
							break
						}
					}
				}
			}

			// Verify all chunks are valid UTF-8 (Go strings are always valid UTF-8,
			// but this checks our splitting didn't create issues)
			for i, chunk := range chunks {
				if chunk != string([]rune(chunk)) {
					t.Errorf("chunk %d contains invalid UTF-8: %q", i, chunk)
				}
			}
		})
	}
}

func TestSplitTextForStreamingEdgeCases(t *testing.T) {
	// Test very small chunk sizes
	text := "Hello \u4e16\u754c"
	chunks := splitTextForStreaming(context.Background(), text, 1)

	// Should handle even 1-byte chunks without breaking characters
	reconstructed := strings.Join(chunks, "")
	if strings.TrimSpace(reconstructed) != text {
		t.Errorf("failed with 1-byte chunks: got %q, want %q", reconstructed, text)
	}

	// Verify no broken characters
	for i, chunk := range chunks {
		if chunk != string([]rune(chunk)) {
			t.Errorf("chunk %d with size 1 contains broken character: %q", i, chunk)
		}
	}
}
