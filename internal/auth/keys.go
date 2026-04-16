package auth

import (
	"crypto/sha256"
	"encoding/hex"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/constants"
	"lmtools/internal/errors"
	"net/http"
	"os"
	"strings"
)

// ReadKeyFile reads an API key from a file and returns it trimmed
func ReadKeyFile(path string) (string, error) {
	if path == "" {
		return "", errors.WrapError("validate API key file", stdErrors.New("no API key file specified"))
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.WrapError("read API key file", err)
	}

	// Trim whitespace and newlines
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", errors.WrapError("validate API key", fmt.Errorf("API key file %s is empty or contains only whitespace", path))
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
		return errors.WrapError("validate API key", stdErrors.New("API key is empty"))
	}

	// Provider-specific validation
	switch provider {
	case constants.ProviderOpenAI:
		// OpenAI keys typically start with "sk-"
		if !strings.HasPrefix(key, "sk-") && !strings.Contains(key, "-") {
			// Allow Azure OpenAI keys which have different format
			// Just check it's not obviously invalid
			if len(key) < 20 {
				return errors.WrapError("validate API key", stdErrors.New("OpenAI API key appears to be invalid (too short)"))
			}
		}
	case constants.ProviderAnthropic:
		// Anthropic keys typically start with "sk-ant-"
		if !strings.HasPrefix(key, "sk-ant-") && len(key) < 20 {
			return errors.WrapError("validate API key", stdErrors.New("anthropic API key appears to be invalid"))
		}
	case constants.ProviderGoogle:
		// Google API keys typically start with "AIza"
		if !strings.HasPrefix(key, "AIza") && len(key) < 20 {
			return errors.WrapError("validate API key", stdErrors.New("google API key appears to be invalid"))
		}
	}

	return nil
}

// SetProviderHeaders sets authentication and required headers for a provider
func SetProviderHeaders(req *http.Request, provider string, apiKey string) {
	switch provider {
	case constants.ProviderOpenAI:
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	case constants.ProviderAnthropic:
		if apiKey != "" {
			req.Header.Set("x-api-key", apiKey)
		}
		// Always set version header for Anthropic
		req.Header.Set("anthropic-version", "2023-06-01")
	case constants.ProviderGoogle:
		if apiKey != "" {
			req.Header.Set("x-goog-api-key", apiKey)
		}
	case constants.ProviderArgo:
		// Argo doesn't use API key headers
	}
}

// ApplyProviderCredentials applies provider-specific authentication details.
func ApplyProviderCredentials(req *http.Request, provider string, apiKey string) error {
	SetProviderHeaders(req, provider, apiKey)
	return nil
}

// SetRequestHeaders sets common headers based on request type
func SetRequestHeaders(req *http.Request, isJSON bool, stream bool, provider string) {
	if isJSON && req.Method != "GET" {
		req.Header.Set("Content-Type", "application/json")
	}

	if stream {
		req.Header.Set("Accept", "text/event-stream")
		// Argo's streaming endpoint may compress responses, causing issues
		if provider == constants.ProviderArgo {
			req.Header.Set("Accept-Encoding", "identity")
		}
	}
}
