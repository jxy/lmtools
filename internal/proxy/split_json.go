package proxy

import (
	"context"
	"lmtools/internal/constants"
	"lmtools/internal/logger"
	"unicode/utf8"
)

// splitJSONForStreaming splits a JSON string into chunks suitable for streaming.
// It ensures:
//   - Multi-byte UTF-8 runes are not broken across boundaries
//   - Backslash escape sequences inside JSON strings are not split (e.g., \n, \", \\
//   - Unicode escape sequences (\uXXXX) are not split across chunks
//
// The returned chunks, when concatenated, reconstruct the original input.
func splitJSONForStreaming(ctx context.Context, s string, chunkSize int) []string {
	if s == "" {
		return []string{}
	}

	if chunkSize <= 0 {
		chunkSize = constants.DefaultJSONChunkSize
	}

	data := []byte(s)

	// Precompute safe split points across the entire JSON string in a single pass,
	// carrying string/escape state between indices. A split at index i means the
	// next chunk starts at i. We avoid splits when an escape is pending (after '\\')
	// or during a \uXXXX sequence. We also require that the next byte is a UTF-8
	// rune start (or i == len(data)).
	safe := make([]bool, len(data)+1) // safe[i] indicates splitting before data[i] is safe
	safe[0] = true                    // starting at 0 is always safe

	inString := false
	escapePending := false
	unicodeHexRemaining := 0
	backslashRun := 0 // count of consecutive backslashes immediately preceding current byte

	for i := 0; i < len(data); i++ {
		b := data[i]

		if unicodeHexRemaining > 0 {
			// Consume hex digits (assume valid JSON input)
			unicodeHexRemaining--
			backslashRun = 0
		} else if escapePending {
			// The character following a backslash completes the escape.
			if b == 'u' {
				unicodeHexRemaining = 4
			}
			escapePending = false
			backslashRun = 0
		} else {
			if b == '"' {
				// Toggle string state only if the quote is not escaped.
				if backslashRun%2 == 0 {
					inString = !inString
				}
				backslashRun = 0
			} else if inString && b == '\\' {
				// Start an escape inside a string
				escapePending = true
				backslashRun++
			} else {
				// Reset backslash run on any non-backslash
				backslashRun = 0
			}
		}

		// Determine if splitting before the next byte is safe
		next := i + 1
		if next <= len(data) {
			// Not safe if we are in the middle of an escape (\ or \uXXXX)
			midEscape := escapePending || unicodeHexRemaining > 0
			// Next chunk must start at a rune boundary (or at end)
			nextIsRuneStart := next == len(data) || utf8.RuneStart(data[next])
			safe[next] = !midEscape && nextIsRuneStart
		}
	}

	// Build chunks greedily, honoring chunkSize while only cutting at safe indices.
	var chunks []string
	pos := 0
	for pos < len(data) {
		// Desired end
		target := pos + chunkSize
		if target > len(data) {
			target = len(data)
		}

		// Choose the largest safe index in [pos+1, target]
		end := -1
		for i := target; i >= pos+1; i-- {
			if safe[i] {
				end = i
				break
			}
		}

		// If none found (e.g., very small chunkSize during an escape), extend forward
		if end == -1 {
			for i := target + 1; i <= len(data); i++ {
				if safe[i] {
					end = i
					break
				}
			}
		}

		// Fallback: advance by a full rune at least to avoid infinite loop
		if end == -1 {
			// Decode next rune from pos and advance
			_, size := utf8.DecodeRune(data[pos:])
			if size < 1 {
				size = 1
			}
			end = pos + size
		}

		chunks = append(chunks, string(data[pos:end]))
		pos = end
	}

	// Log chunking result for debugging
	logger.From(ctx).Debugf("splitJSONForStreaming produced %d chunks", len(chunks))
	return chunks
}
