package core

import (
	"testing"
)

func TestGetString(t *testing.T) {
	tests := []struct {
		name     string
		m        map[string]interface{}
		key      string
		expected string
	}{
		{
			name:     "existing string value",
			m:        map[string]interface{}{"key": "value"},
			key:      "key",
			expected: "value",
		},
		{
			name:     "missing key",
			m:        map[string]interface{}{"other": "value"},
			key:      "key",
			expected: "",
		},
		{
			name:     "nil map",
			m:        nil,
			key:      "key",
			expected: "",
		},
		{
			name:     "empty map",
			m:        map[string]interface{}{},
			key:      "key",
			expected: "",
		},
		{
			name:     "wrong type - int",
			m:        map[string]interface{}{"key": 123},
			key:      "key",
			expected: "",
		},
		{
			name:     "wrong type - bool",
			m:        map[string]interface{}{"key": true},
			key:      "key",
			expected: "",
		},
		{
			name:     "wrong type - float",
			m:        map[string]interface{}{"key": 3.14},
			key:      "key",
			expected: "",
		},
		{
			name:     "wrong type - nil value",
			m:        map[string]interface{}{"key": nil},
			key:      "key",
			expected: "",
		},
		{
			name:     "empty string value",
			m:        map[string]interface{}{"key": ""},
			key:      "key",
			expected: "",
		},
		{
			name:     "unicode string",
			m:        map[string]interface{}{"key": "Hello 世界 🌍"},
			key:      "key",
			expected: "Hello 世界 🌍",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetString(tt.m, tt.key)
			if result != tt.expected {
				t.Errorf("GetString() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestGetInt(t *testing.T) {
	tests := []struct {
		name     string
		m        map[string]interface{}
		key      string
		expected int
	}{
		{
			name:     "int value",
			m:        map[string]interface{}{"key": 42},
			key:      "key",
			expected: 42,
		},
		{
			name:     "float64 value",
			m:        map[string]interface{}{"key": float64(42.0)},
			key:      "key",
			expected: 42,
		},
		{
			name:     "float64 with decimals (truncated)",
			m:        map[string]interface{}{"key": float64(42.9)},
			key:      "key",
			expected: 42,
		},
		{
			name:     "int64 value",
			m:        map[string]interface{}{"key": int64(42)},
			key:      "key",
			expected: 42,
		},
		{
			name:     "missing key",
			m:        map[string]interface{}{"other": 42},
			key:      "key",
			expected: 0,
		},
		{
			name:     "nil map",
			m:        nil,
			key:      "key",
			expected: 0,
		},
		{
			name:     "wrong type - string",
			m:        map[string]interface{}{"key": "42"},
			key:      "key",
			expected: 0,
		},
		{
			name:     "wrong type - bool",
			m:        map[string]interface{}{"key": true},
			key:      "key",
			expected: 0,
		},
		{
			name:     "negative int",
			m:        map[string]interface{}{"key": -42},
			key:      "key",
			expected: -42,
		},
		{
			name:     "zero value",
			m:        map[string]interface{}{"key": 0},
			key:      "key",
			expected: 0,
		},
		{
			name:     "max int value",
			m:        map[string]interface{}{"key": int(^uint(0) >> 1)},
			key:      "key",
			expected: int(^uint(0) >> 1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetInt(tt.m, tt.key)
			if result != tt.expected {
				t.Errorf("GetInt() = %d, want %d", result, tt.expected)
			}
		})
	}
}

func TestGetInt64(t *testing.T) {
	tests := []struct {
		name     string
		m        map[string]interface{}
		key      string
		expected int64
	}{
		{
			name:     "int64 value",
			m:        map[string]interface{}{"key": int64(42)},
			key:      "key",
			expected: 42,
		},
		{
			name:     "int value",
			m:        map[string]interface{}{"key": 42},
			key:      "key",
			expected: 42,
		},
		{
			name:     "float64 value",
			m:        map[string]interface{}{"key": float64(42.0)},
			key:      "key",
			expected: 42,
		},
		{
			name:     "float64 with decimals (truncated)",
			m:        map[string]interface{}{"key": float64(42.9)},
			key:      "key",
			expected: 42,
		},
		{
			name:     "missing key",
			m:        map[string]interface{}{"other": 42},
			key:      "key",
			expected: 0,
		},
		{
			name:     "nil map",
			m:        nil,
			key:      "key",
			expected: 0,
		},
		{
			name:     "wrong type - string",
			m:        map[string]interface{}{"key": "42"},
			key:      "key",
			expected: 0,
		},
		{
			name:     "wrong type - bool",
			m:        map[string]interface{}{"key": true},
			key:      "key",
			expected: 0,
		},
		{
			name:     "negative int64",
			m:        map[string]interface{}{"key": int64(-42)},
			key:      "key",
			expected: -42,
		},
		{
			name:     "large int64",
			m:        map[string]interface{}{"key": int64(9223372036854775807)},
			key:      "key",
			expected: 9223372036854775807,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetInt64(tt.m, tt.key)
			if result != tt.expected {
				t.Errorf("GetInt64() = %d, want %d", result, tt.expected)
			}
		})
	}
}

func TestGetBool(t *testing.T) {
	tests := []struct {
		name     string
		m        map[string]interface{}
		key      string
		expected bool
	}{
		{
			name:     "true value",
			m:        map[string]interface{}{"key": true},
			key:      "key",
			expected: true,
		},
		{
			name:     "false value",
			m:        map[string]interface{}{"key": false},
			key:      "key",
			expected: false,
		},
		{
			name:     "missing key",
			m:        map[string]interface{}{"other": true},
			key:      "key",
			expected: false,
		},
		{
			name:     "nil map",
			m:        nil,
			key:      "key",
			expected: false,
		},
		{
			name:     "wrong type - string",
			m:        map[string]interface{}{"key": "true"},
			key:      "key",
			expected: false,
		},
		{
			name:     "wrong type - int",
			m:        map[string]interface{}{"key": 1},
			key:      "key",
			expected: false,
		},
		{
			name:     "wrong type - float",
			m:        map[string]interface{}{"key": 1.0},
			key:      "key",
			expected: false,
		},
		{
			name:     "nil value",
			m:        map[string]interface{}{"key": nil},
			key:      "key",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetBool(tt.m, tt.key)
			if result != tt.expected {
				t.Errorf("GetBool() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestEnsureAudioFormat(t *testing.T) {
	tests := []struct {
		name           string
		audio          *AudioData
		expectedFormat string
	}{
		{
			name:           "nil audio",
			audio:          nil,
			expectedFormat: "",
		},
		{
			name:           "audio with existing format",
			audio:          &AudioData{Format: "mp3", Data: "base64data"},
			expectedFormat: "mp3",
		},
		{
			name:           "audio with empty format and data",
			audio:          &AudioData{Format: "", Data: "base64data"},
			expectedFormat: "wav",
		},
		{
			name:           "audio with empty format and no data",
			audio:          &AudioData{Format: "", Data: ""},
			expectedFormat: "",
		},
		{
			name:           "audio with format but no data",
			audio:          &AudioData{Format: "mp3", Data: ""},
			expectedFormat: "mp3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ensureAudioFormat(tt.audio)
			if tt.audio != nil && tt.audio.Format != tt.expectedFormat {
				t.Errorf("ensureAudioFormat() resulted in Format = %q, want %q", tt.audio.Format, tt.expectedFormat)
			}
		})
	}
}

func TestIsEmptyCollection(t *testing.T) {
	tests := []struct {
		name     string
		v        interface{}
		expected bool
	}{
		{
			name:     "empty array",
			v:        []interface{}{},
			expected: true,
		},
		{
			name:     "non-empty array",
			v:        []interface{}{"item"},
			expected: false,
		},
		{
			name:     "empty map",
			v:        map[string]interface{}{},
			expected: true,
		},
		{
			name:     "non-empty map",
			v:        map[string]interface{}{"key": "value"},
			expected: false,
		},
		{
			name:     "nil value",
			v:        nil,
			expected: false,
		},
		{
			name:     "string (not a collection)",
			v:        "string",
			expected: false,
		},
		{
			name:     "int (not a collection)",
			v:        42,
			expected: false,
		},
		{
			name:     "bool (not a collection)",
			v:        true,
			expected: false,
		},
		{
			name:     "array with nil element",
			v:        []interface{}{nil},
			expected: false,
		},
		{
			name:     "map with nil value",
			v:        map[string]interface{}{"key": nil},
			expected: false,
		},
		{
			name:     "nested empty array",
			v:        []interface{}{[]interface{}{}},
			expected: false,
		},
		{
			name:     "nested empty map",
			v:        map[string]interface{}{"key": map[string]interface{}{}},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsEmptyCollection(tt.v)
			if result != tt.expected {
				t.Errorf("IsEmptyCollection() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// Benchmarks for hot-path functions

func BenchmarkGetString(b *testing.B) {
	m := map[string]interface{}{
		"key1": "value1",
		"key2": "value2",
		"key3": 123,
		"key4": true,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GetString(m, "key1")
		_ = GetString(m, "key3") // wrong type
		_ = GetString(m, "missing")
	}
}

func BenchmarkGetInt(b *testing.B) {
	m := map[string]interface{}{
		"int":     42,
		"float64": float64(42.5),
		"int64":   int64(42),
		"string":  "42",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GetInt(m, "int")
		_ = GetInt(m, "float64")
		_ = GetInt(m, "int64")
		_ = GetInt(m, "string") // wrong type
		_ = GetInt(m, "missing")
	}
}

func BenchmarkGetInt64(b *testing.B) {
	m := map[string]interface{}{
		"int64":   int64(42),
		"int":     42,
		"float64": float64(42.5),
		"string":  "42",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GetInt64(m, "int64")
		_ = GetInt64(m, "int")
		_ = GetInt64(m, "float64")
		_ = GetInt64(m, "string") // wrong type
		_ = GetInt64(m, "missing")
	}
}

func BenchmarkGetBool(b *testing.B) {
	m := map[string]interface{}{
		"true":   true,
		"false":  false,
		"int":    1,
		"string": "true",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GetBool(m, "true")
		_ = GetBool(m, "false")
		_ = GetBool(m, "int") // wrong type
		_ = GetBool(m, "missing")
	}
}

func BenchmarkIsEmptyCollection(b *testing.B) {
	emptyArray := []interface{}{}
	nonEmptyArray := []interface{}{"item"}
	emptyMap := map[string]interface{}{}
	nonEmptyMap := map[string]interface{}{"key": "value"}
	notCollection := "string"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsEmptyCollection(emptyArray)
		_ = IsEmptyCollection(nonEmptyArray)
		_ = IsEmptyCollection(emptyMap)
		_ = IsEmptyCollection(nonEmptyMap)
		_ = IsEmptyCollection(notCollection)
		_ = IsEmptyCollection(nil)
	}
}
