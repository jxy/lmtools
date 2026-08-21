package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ARCHITECTURAL NOTE: Centralized helper functions for safe map extraction.
// These replace duplicate implementations across the codebase and provide
// a single source of truth for type conversions from map[string]interface{}.

// FormatByteCount renders a byte limit the way an operator wrote it. The tool
// UI and the truncation note the model reads describe the same cap, so they
// spell it the same way.
func FormatByteCount(size int) string {
	const (
		kib = 1024
		mib = 1024 * kib
	)
	switch {
	case size > 0 && size%mib == 0:
		return fmt.Sprintf("%dMiB", size/mib)
	case size > 0 && size%kib == 0:
		return fmt.Sprintf("%dKiB", size/kib)
	default:
		return fmt.Sprintf("%d bytes", size)
	}
}

// MarshalJSONForDisplay renders a value for an operator's eyes. encoding/json
// escapes &, <, and > by default, on the assumption the output is going into a
// web page; a person reading an approval line or a suggested whitelist rule for
// `sh -c "make build && ./run > out 2>&1"` has to be able to read it as
// written to judge it.
func MarshalJSONForDisplay(value interface{}) string {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return fmt.Sprintf("%v", value)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// GetString safely extracts a string value from a map
func GetString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// GetInt safely extracts an int value from a map
func GetInt(m map[string]interface{}, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	case int64:
		return int(v)
	}
	return 0
}

// GetInt64 safely extracts an int64 value from a map
func GetInt64(m map[string]interface{}, key string) int64 {
	switch v := m[key].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}

// ensureAudioFormat ensures audio data has a format, defaulting to "wav" if needed.
// This centralizes the audio format defaulting logic used across conversions.
func ensureAudioFormat(audio *AudioData) {
	if audio != nil && audio.Format == "" && audio.Data != "" {
		audio.Format = "wav"
	}
}

// IsEmptyCollection reports whether v is an empty array or empty object.
// This is useful for checking if tool_calls or similar fields are empty,
// as different providers may use either [] or {} to represent empty collections.
func IsEmptyCollection(v interface{}) bool {
	switch t := v.(type) {
	case []interface{}:
		return len(t) == 0
	case map[string]interface{}:
		return len(t) == 0
	default:
		return false
	}
}
