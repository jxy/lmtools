package core

import "strings"

// DetectImageMediaType detects the media type from a URL's file extension.
// Returns the appropriate MIME type for common image formats.
// Defaults to "image/jpeg" for unknown extensions.
func DetectImageMediaType(url string) string {
	urlLower := strings.ToLower(url)
	switch {
	case strings.HasSuffix(urlLower, ".png"):
		return "image/png"
	case strings.HasSuffix(urlLower, ".jpg"), strings.HasSuffix(urlLower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(urlLower, ".webp"):
		return "image/webp"
	case strings.HasSuffix(urlLower, ".gif"):
		return "image/gif"
	case strings.HasSuffix(urlLower, ".bmp"):
		return "image/bmp"
	case strings.HasSuffix(urlLower, ".svg"):
		return "image/svg+xml"
	default:
		return "image/jpeg" // Default for unknown extensions
	}
}
