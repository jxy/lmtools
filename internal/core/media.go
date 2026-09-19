package core

import "strings"

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

// Base64DataURL assembles a data URL from a media type and an already
// base64-encoded payload, the inverse of ParseBase64DataURL.
func Base64DataURL(mediaType, encoded string) string {
	return "data:" + mediaType + ";base64," + encoded
}

// AnthropicImageSourceURL is the URL an ImageBlock carries for an Anthropic
// image source: the url of a url source, or a data URL assembled from a
// base64 source's media type and data. Reading only the url field turned
// every base64 image a client sent into an empty block.
func AnthropicImageSourceURL(sourceType, url, mediaType, data string) string {
	if strings.EqualFold(sourceType, "base64") || (url == "" && data != "") {
		return Base64DataURL(mediaType, data)
	}
	return url
}
