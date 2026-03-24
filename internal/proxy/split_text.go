package proxy

import (
	"context"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"strings"
	"unicode/utf8"
)

// splitTextForStreaming splits text into chunks suitable for streaming.
// It ensures that multi-byte UTF-8 characters are not broken across chunk boundaries.
// Invalid UTF-8 sequences are replaced with U+FFFD (�).
// The caller is responsible for logging any invalid sequences if needed.
func splitTextForStreaming(ctx context.Context, text string, chunkSize int) []string {
	if text == "" {
		return []string{}
	}

	// Clamp non-positive chunk sizes to a reasonable default
	if chunkSize <= 0 {
		chunkSize = constants.DefaultTextChunkSize
	}

	var chunks []string
	data := []byte(text)

	for len(data) > 0 {
		// If remaining data is smaller than chunk size, process it all
		if len(data) <= chunkSize {
			chunk, invalidCount := processInvalidUTF8(data)
			if invalidCount > 0 {
				logInvalidUTF8(ctx, data[:invalidCount])
			}
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			break
		}

		// Find the last valid UTF-8 boundary within chunkSize
		boundary := chunkSize

		// For very small chunk sizes, we need to ensure we can fit at least one rune
		if boundary < 4 { // Maximum UTF-8 sequence is 4 bytes
			// Process at least one rune (valid or invalid)
			r, size := utf8.DecodeRune(data)
			if r == utf8.RuneError && size == 1 {
				// Invalid byte - replace with U+FFFD
				logInvalidUTF8(ctx, data[:1])
				chunks = append(chunks, string(utf8.RuneError))
				data = data[1:]
			} else {
				// Valid rune
				chunks = append(chunks, string(data[:size]))
				data = data[size:]
			}
			continue
		}

		// Find a valid UTF-8 boundary
		for boundary > 0 && boundary < len(data) && !utf8.RuneStart(data[boundary]) {
			boundary--
		}

		// If we couldn't find a valid boundary, process at least one rune
		if boundary == 0 {
			r, size := utf8.DecodeRune(data)
			if r == utf8.RuneError && size == 1 {
				// Invalid byte - replace with U+FFFD
				logInvalidUTF8(ctx, data[:1])
				chunks = append(chunks, string(utf8.RuneError))
				data = data[1:]
			} else {
				// Valid rune
				chunks = append(chunks, string(data[:size]))
				data = data[size:]
			}
			continue
		}

		// Extract and process the chunk
		chunk, invalidCount := processInvalidUTF8(data[:boundary])
		if invalidCount > 0 {
			logInvalidUTF8(ctx, data[:invalidCount])
		}
		if chunk != "" {
			chunks = append(chunks, chunk)
		}

		// Move to the next chunk
		data = data[boundary:]
	}

	return chunks
}

// processInvalidUTF8 replaces invalid UTF-8 sequences with U+FFFD
// Returns the processed string and the count of invalid bytes encountered
func processInvalidUTF8(data []byte) (string, int) {
	if utf8.Valid(data) {
		return string(data), 0
	}

	// Process byte by byte, replacing invalid sequences
	var result strings.Builder
	var invalidBytes []byte
	totalInvalid := 0

	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			// Collect invalid bytes
			invalidBytes = append(invalidBytes, data[0])
			totalInvalid++
			data = data[1:]
		} else {
			// If we have accumulated invalid bytes, replace them
			if len(invalidBytes) > 0 {
				result.WriteRune(utf8.RuneError) // U+FFFD
				invalidBytes = nil
			}
			// Write the valid rune
			result.WriteRune(r)
			data = data[size:]
		}
	}

	// Handle any remaining invalid bytes
	if len(invalidBytes) > 0 {
		result.WriteRune(utf8.RuneError) // U+FFFD
	}

	return result.String(), totalInvalid
}

// logInvalidUTF8 logs invalid UTF-8 bytes in escaped form using context-scoped logger
func logInvalidUTF8(ctx context.Context, invalidBytes []byte) {
	var escaped strings.Builder
	for _, b := range invalidBytes {
		fmt.Fprintf(&escaped, "\\x%02X", b)
	}

	logger.From(ctx).Warnf("Invalid UTF-8 sequence in streaming text: %s", escaped.String())
}
