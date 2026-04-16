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

func ParseBase64DataURL(raw string) (mediaType string, data string, ok bool) {
	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return "", "", false
	}

	comma := strings.Index(raw, ",")
	if comma <= len("data:") || comma == len(raw)-1 {
		return "", "", false
	}

	meta := raw[len("data:"):comma]
	parts := strings.Split(meta, ";")
	if len(parts) == 0 || parts[0] == "" {
		return "", "", false
	}

	hasBase64 := false
	for _, part := range parts[1:] {
		if strings.EqualFold(part, "base64") {
			hasBase64 = true
			break
		}
	}
	if !hasBase64 {
		return "", "", false
	}

	return parts[0], raw[comma+1:], true
}
