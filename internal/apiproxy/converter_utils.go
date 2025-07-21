package apiproxy

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
)

// extractSystemContent extracts system content from various formats
func extractSystemContent(system json.RawMessage) (string, error) {
	// Try as string
	var str string
	if err := json.Unmarshal(system, &str); err == nil {
		return str, nil
	}

	// Try as array of content blocks
	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(system, &blocks); err == nil {
		var texts []string
		for _, block := range blocks {
			if block.Type == "text" {
				texts = append(texts, block.Text)
			}
		}
		return strings.Join(texts, "\n"), nil
	}

	// Try as single content block
	var block AnthropicContentBlock
	if err := json.Unmarshal(system, &block); err == nil {
		if block.Type == "text" {
			return block.Text, nil
		}
	}

	return "", fmt.Errorf("unsupported system content format")
}

// generateToolUseID generates a unique ID for tool use using UUID v4
func generateToolUseID() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		// Fallback to a simple counter-based ID if random fails
		return fmt.Sprintf("toolu_fallback_%d", generateFallbackID())
	}

	// Set version (4) and variant bits
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("toolu_%x%x%x%x%x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

var fallbackCounter uint64

// generateFallbackID generates a simple counter-based ID
func generateFallbackID() uint64 {
	// This is not thread-safe but only used as fallback
	fallbackCounter++
	return fallbackCounter
}
