package proxy

import (
	"fmt"
	"net/url"
)

// buildProviderURL safely constructs a provider URL by appending a path to a base URL.
// It properly handles query parameters, fragments, and trailing slashes.
// This function ensures that:
// - Query parameters and fragments in the base URL are preserved
// - Path joining is done correctly without double slashes
// - The resulting URL is valid and properly formatted
func buildProviderURL(baseURL, pathToAppend string) (string, error) {
	// Parse the base URL
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid base URL: %w", err)
	}

	// Use url.JoinPath for safer path joining
	u.Path, err = url.JoinPath(u.Path, pathToAppend)
	if err != nil {
		return "", fmt.Errorf("invalid path join: %w", err)
	}

	return u.String(), nil
}

// sanitizeURLForLogging removes sensitive information from a URL for safe logging.
// It removes user credentials and query parameters that might contain tokens.
func sanitizeURLForLogging(urlStr string) string {
	u, err := url.Parse(urlStr)
	if err != nil {
		// If we can't parse it, return a generic message
		return "[invalid URL]"
	}

	// Remove credentials and sensitive query params
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""

	return u.String()
}

// buildGoogleModelURL constructs a Google API URL for a specific model and action
// baseURL: The base Google API URL (e.g., "https://generativelanguage.googleapis.com/v1beta/models")
// model: The model name (e.g., "gemini-pro")
// action: The action to perform (e.g., "generateContent", "streamGenerateContent")
func buildGoogleModelURL(baseURL, model, action string) (string, error) {
	// Use buildProviderURL to safely construct the URL
	modelPath := fmt.Sprintf("%s:%s", model, action)
	return buildProviderURL(baseURL, modelPath)
}
