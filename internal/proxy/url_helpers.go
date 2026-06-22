package proxy

import "net/url"

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
