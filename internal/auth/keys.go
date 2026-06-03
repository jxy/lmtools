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

type ProviderKey struct {
	Provider string
	Value    string
}

type ProviderKeySet struct {
	AnthropicAPIKey string
	OpenAIAPIKey    string
	GoogleAPIKey    string
	ArgoAPIKey      string
}

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

func NewProviderKey(provider, value string) (ProviderKey, error) {
	normalized := constants.NormalizeProvider(provider)
	if !constants.IsValidProvider(normalized) {
		return ProviderKey{}, fmt.Errorf("unknown provider: %s", provider)
	}
	if strings.TrimSpace(value) == "" {
		return ProviderKey{}, errors.WrapError("validate API key", stdErrors.New("API key is empty"))
	}
	return ProviderKey{Provider: normalized, Value: value}, nil
}

func LoadProviderKeyFile(provider, path string) (ProviderKey, error) {
	key, err := ReadKeyFile(path)
	if err != nil {
		return ProviderKey{}, err
	}
	return NewProviderKey(provider, key)
}

func (k ProviderKey) Apply(req *http.Request) error {
	return ApplyProviderCredentials(req, k.Provider, k.Value)
}

func (k ProviderKey) Set() ProviderKeySet {
	switch k.Provider {
	case constants.ProviderAnthropic:
		return ProviderKeySet{AnthropicAPIKey: k.Value}
	case constants.ProviderOpenAI:
		return ProviderKeySet{OpenAIAPIKey: k.Value}
	case constants.ProviderGoogle:
		return ProviderKeySet{GoogleAPIKey: k.Value}
	case constants.ProviderArgo:
		return ProviderKeySet{ArgoAPIKey: k.Value}
	default:
		return ProviderKeySet{}
	}
}

func (s ProviderKeySet) KeyForProvider(provider string) string {
	switch constants.NormalizeProvider(provider) {
	case constants.ProviderAnthropic:
		return s.AnthropicAPIKey
	case constants.ProviderOpenAI:
		return s.OpenAIAPIKey
	case constants.ProviderGoogle:
		return s.GoogleAPIKey
	case constants.ProviderArgo:
		return s.ArgoAPIKey
	default:
		return ""
	}
}

// HashAPIKey creates a hash of an API key for use as a pseudo-username
// This allows session management for API key-based providers
func HashAPIKey(apiKey string) string {
	hash := sha256.Sum256([]byte(apiKey))
	// Use first 8 bytes of hash for a reasonable length identifier
	return "apikey_" + hex.EncodeToString(hash[:8])
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
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
			req.Header.Set("x-api-key", apiKey)
		}
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
