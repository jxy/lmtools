package core

import (
	"testing"
)

// TestConvertSchemaToGoogleFormat_UnknownTypeInMap tests that unknown types in map-based
// schema conversion default to OBJECT, matching the behavior of typed conversion
func TestConvertSchemaToGoogleFormat_UnknownTypeInMap(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]interface{}
		expected string // Expected "type" field value
	}{
		{
			name: "unknown type defaults to OBJECT",
			input: map[string]interface{}{
				"type": "mystery",
				"properties": map[string]interface{}{
					"field1": map[string]interface{}{"type": "string"},
				},
			},
			expected: "OBJECT",
		},
		{
			name: "nested unknown type defaults to OBJECT",
			input: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"nested": map[string]interface{}{
						"type": "unknown_type",
					},
				},
			},
			expected: "OBJECT", // The root type
		},
		{
			name: "empty type defaults to OBJECT",
			input: map[string]interface{}{
				"type":       "",
				"properties": map[string]interface{}{},
			},
			expected: "OBJECT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertSchemaToGoogleFormat(tt.input)
			resultMap, ok := result.(map[string]interface{})
			if !ok {
				t.Fatalf("Expected map[string]interface{}, got %T", result)
			}

			// Check that the type field has the expected value
			if gotType, ok := resultMap["type"].(string); !ok {
				t.Errorf("Type field is not a string: %v", resultMap["type"])
			} else if gotType != tt.expected {
				t.Errorf("Expected type %q, got %q", tt.expected, gotType)
			}

			// For nested unknown type test, also check the nested field
			if tt.name == "nested unknown type defaults to OBJECT" {
				if props, ok := resultMap["properties"].(map[string]interface{}); ok {
					if nested, ok := props["nested"].(map[string]interface{}); ok {
						if nestedType, ok := nested["type"].(string); !ok || nestedType != "OBJECT" {
							t.Errorf("Nested unknown type should default to OBJECT, got %v", nestedType)
						}
					}
				}
			}
		})
	}
}
