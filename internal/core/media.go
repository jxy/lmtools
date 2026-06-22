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
