package proxy

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitJSONForStreaming_NoSplitInsideUnicodeEscape(t *testing.T) {
	ctx := context.Background()
	// JSON with a unicode escape \u1234 and other content
	s := `{"command":"echo \u1234 done"}`
	chunks := splitJSONForStreaming(ctx, s, 10)
	// Reconstruct
	joined := ""
	for _, c := range chunks {
		if !utf8.ValidString(c) {
			t.Fatalf("Chunk has invalid UTF-8: %q", c)
		}
		joined += c
	}
	if joined != s {
		t.Fatalf("Reconstructed mismatch\nGot:  %q\nWant: %q", joined, s)
	}
	// Ensure no chunk ends with partial \u sequence
	for i, c := range chunks {
		if len(c) == 0 {
			continue
		}
		// If the last bytes look like "\\u" or partial hex, that's a failure
		if c[len(c)-1] == 'u' && len(c) >= 2 && c[len(c)-2] == '\\' {
			t.Fatalf("Chunk %d ends mid unicode escape: %q", i, c)
		}
	}
}

func TestSplitJSONForStreaming_NoSplitInsideSimpleEscape(t *testing.T) {
	ctx := context.Background()
	s := `{"text":"line1\nline2 and a tab\tand a quote\" and a backslash\\"}`
	chunks := splitJSONForStreaming(ctx, s, 8)
	joined := ""
	for _, c := range chunks {
		joined += c
	}
	if joined != s {
		t.Fatalf("Reconstructed mismatch\nGot:  %q\nWant: %q", joined, s)
	}
	// Ensure no chunk ends with a single backslash that would start an escape
	for i, c := range chunks {
		if len(c) == 0 {
			continue
		}
		if c[len(c)-1] == '\\' {
			t.Fatalf("Chunk %d ends with backslash (mid escape): %q", i, c)
		}
	}
}

func TestSplitJSONForStreaming_HandlesMultibyteUTF8(t *testing.T) {
	ctx := context.Background()
	s := `{"args":"Hello 中文 🚀"}`
	chunks := splitJSONForStreaming(ctx, s, 7)
	joined := ""
	for _, c := range chunks {
		if !utf8.ValidString(c) {
			t.Fatalf("Chunk has invalid UTF-8: %q", c)
		}
		joined += c
	}
	if joined != s {
		t.Fatalf("Reconstructed mismatch\nGot:  %q\nWant: %q", joined, s)
	}
}

func TestSplitJSONForStreaming_MidEscapeAcrossChunkBoundary(t *testing.T) {
	ctx := context.Background()
	// Carefully chosen size so that a unicode escape would be split if not guarded
	s := `{"command":"cd /path/to/project \u0026 make test with tab\tand quote\" and backslash\\ end"}`
	chunks := splitJSONForStreaming(ctx, s, 20)
	// Ensure joined equals original
	joined := ""
	for _, c := range chunks {
		joined += c
	}
	if joined != s {
		t.Fatalf("Reconstructed mismatch\nGot:  %q\nWant: %q", joined, s)
	}
	// Verify that no chunk ends right after "\\u" or within the 4 hex digits
	// Check each chunk end for trailing "\\u", "\\u0", etc., and no dangling backslashes
	for i, c := range chunks {
		if len(c) == 0 {
			continue
		}
		// Search from end for a potential unicode sequence start
		// Simple heuristic: if chunk ends with backslash, it's bad.
		if c[len(c)-1] == '\\' {
			t.Fatalf("Chunk %d ends with backslash (mid escape): %q", i, c)
		}
	}
}

// New test: ensure splitting does not break a doubled backslash escape like \\t
func TestSplitJSONForStreaming_NoSplitDoubledBackslash(t *testing.T) {
	ctx := context.Background()
	// JSON string contains a tab escape, which appears as \\t inside a JSON string literal
	s := `{"input":"Before tab \\t after"}`
	chunks := splitJSONForStreaming(ctx, s, 5)
	joined := ""
	for _, c := range chunks {
		joined += c
	}
	if joined != s {
		t.Fatalf("Reconstructed mismatch\nGot:  %q\nWant: %q", joined, s)
	}
	// Ensure no chunk ends with a single backslash or with \\ followed by end of chunk
	for i, c := range chunks {
		if len(c) == 0 {
			continue
		}
		// Dangling single backslash at end is forbidden
		if c[len(c)-1] == '\\' {
			t.Fatalf("Chunk %d ends with backslash (mid escape): %q", i, c)
		}
		// Also check pattern where a chunk ends with the first of a doubled backslash
		// by scanning adjacent chunks
		if i < len(chunks)-1 {
			if strings.HasSuffix(c, "\\") && strings.HasPrefix(chunks[i+1], "t") {
				t.Fatalf("Split occurred between doubled backslash and escape char: %q | %q", c, chunks[i+1])
			}
		}
	}
}
