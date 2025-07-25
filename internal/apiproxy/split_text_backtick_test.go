package apiproxy

import (
	"strings"
	"testing"
)

func TestSplitTextForStreamingBacktick(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		chunkSize int
		want      []string
	}{
		{
			name:      "backtick in code",
			text:      "uses the `splitTextForStreaming` function instead of",
			chunkSize: 20,
			want: []string{
				"uses the `splitTextF",
				"orStreaming` functio",
				"n instead of",
			},
		},
		{
			name:      "multiple backticks",
			text:      "The `foo` and `bar` methods are used",
			chunkSize: 15,
			want: []string{
				"The `foo` and `",
				"bar` methods ar",
				"e used",
			},
		},
		{
			name:      "backtick at boundary",
			text:      "This is exactly20chr`code` here",
			chunkSize: 20,
			want: []string{
				"This is exactly20chr",
				"`code` here",
			},
		},
		{
			name:      "no spaces near backtick",
			text:      "NoSpacesHere`BacktickInMiddle`NoSpacesAfter",
			chunkSize: 20,
			want: []string{
				"NoSpacesHere`Backtic",
				"kInMiddle`NoSpacesAf",
				"ter",
			},
		},
		{
			name:      "very small chunks with backticks",
			text:      "a `b` c",
			chunkSize: 3,
			want: []string{
				"a `",
				"b` ",
				"c",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitTextForStreaming(tt.text, tt.chunkSize)

			// Check if we got the expected number of chunks
			if len(got) != len(tt.want) {
				t.Errorf("got %d chunks, want %d", len(got), len(tt.want))
				t.Logf("got: %q", got)
				t.Logf("want: %q", tt.want)
				return
			}

			// Check each chunk
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("chunk %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}

			// Verify reconstruction
			reconstructed := strings.Join(got, "")
			if reconstructed != tt.text {
				t.Errorf("reconstructed text doesn't match: got %q, want %q", reconstructed, tt.text)
			}
		})
	}
}
