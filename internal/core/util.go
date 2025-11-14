package core

// ARCHITECTURAL NOTE: Centralized helper functions for safe map extraction.
// These replace duplicate implementations across the codebase and provide
// a single source of truth for type conversions from map[string]interface{}.

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

// GetBool safely extracts a bool value from a map
func GetBool(m map[string]interface{}, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
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
