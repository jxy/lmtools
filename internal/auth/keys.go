package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// ReadKeyFile reads an API key from a file and returns it trimmed
func ReadKeyFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("no API key file specified")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read API key file %s: %w", path, err)
	}

	// Trim whitespace and newlines
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("API key file %s is empty or contains only whitespace", path)
	}

	return key, nil
}

// HashAPIKey creates a hash of an API key for use as a pseudo-username
// This allows session management for API key-based providers
func HashAPIKey(apiKey string) string {
	hash := sha256.Sum256([]byte(apiKey))
	// Use first 8 bytes of hash for a reasonable length identifier
	return "apikey_" + hex.EncodeToString(hash[:8])
}

// ValidateAPIKey performs basic validation on an API key
func ValidateAPIKey(key string, provider string) error {
	if key == "" {
		return fmt.Errorf("API key is empty")
	}

	// Provider-specific validation
	switch provider {
	case "openai":
		// OpenAI keys typically start with "sk-"
		if !strings.HasPrefix(key, "sk-") && !strings.Contains(key, "-") {
			// Allow Azure OpenAI keys which have different format
			// Just check it's not obviously invalid
			if len(key) < 20 {
				return fmt.Errorf("OpenAI API key appears to be invalid (too short)")
			}
		}
	case "anthropic":
		// Anthropic keys typically start with "sk-ant-"
		if !strings.HasPrefix(key, "sk-ant-") && len(key) < 20 {
			return fmt.Errorf("anthropic API key appears to be invalid")
		}
	case "gemini":
		// Gemini/Google API keys typically start with "AIza"
		if !strings.HasPrefix(key, "AIza") && len(key) < 20 {
			return fmt.Errorf("gemini API key appears to be invalid")
		}
	}

	return nil
}

// SetProviderHeaders sets authentication and required headers for a provider
func SetProviderHeaders(req *http.Request, provider string, apiKey string) {
	switch provider {
	case "openai":
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	case "anthropic":
		if apiKey != "" {
			req.Header.Set("x-api-key", apiKey)
		}
		// Always set version header for Anthropic
		req.Header.Set("anthropic-version", "2023-06-01")
	case "gemini":
		// Gemini uses API key in URL, not headers
	case "argo":
		// Argo doesn't use API key headers
	}
}

// SetRequestHeaders sets common headers based on request type
func SetRequestHeaders(req *http.Request, isJSON bool, isStreaming bool, provider string) {
	if isJSON && req.Method != "GET" {
		req.Header.Set("Content-Type", "application/json")
	}

	if provider == "argo" {
		req.Header.Set("Accept", "text/plain")
		req.Header.Set("Accept-Encoding", "identity")
	} else if isStreaming {
		req.Header.Set("Accept", "text/event-stream")
	}
}
