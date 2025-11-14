package core

import (
	"encoding/json"
	"fmt"
	"lmtools/internal/logger"
)

// Google schema type constants
const (
	GoogleTypeString  = "STRING"
	GoogleTypeNumber  = "NUMBER"
	GoogleTypeInteger = "INTEGER"
	GoogleTypeBoolean = "BOOLEAN"
	GoogleTypeArray   = "ARRAY"
	GoogleTypeObject  = "OBJECT"
)

// JSONSchema represents a JSON schema structure
type JSONSchema struct {
	Type                 string                `json:"type,omitempty"`
	Properties           map[string]JSONSchema `json:"properties,omitempty"`
	Items                *JSONSchema           `json:"items,omitempty"`
	Required             []string              `json:"required,omitempty"`
	Enum                 []interface{}         `json:"enum,omitempty"`
	Description          string                `json:"description,omitempty"`
	Default              interface{}           `json:"default,omitempty"`
	Minimum              *float64              `json:"minimum,omitempty"`
	Maximum              *float64              `json:"maximum,omitempty"`
	MinLength            *int                  `json:"minLength,omitempty"`
	MaxLength            *int                  `json:"maxLength,omitempty"`
	Pattern              string                `json:"pattern,omitempty"`
	AdditionalProperties *bool                 `json:"additionalProperties,omitempty"`
	Title                string                `json:"title,omitempty"`
	Schema               string                `json:"$schema,omitempty"`
	ID                   string                `json:"$id,omitempty"`
}

// ConvertSchemaToGoogleFormat converts a JSON schema to Google's format
// This centralizes the conversion logic to avoid duplication
func ConvertSchemaToGoogleFormat(schema interface{}) interface{} {
	// Handle different input types
	switch s := schema.(type) {
	case JSONSchema:
		return convertJSONSchemaToGoogle(s)
	case *JSONSchema:
		if s != nil {
			return convertJSONSchemaToGoogle(*s)
		}
		return nil
	case json.RawMessage:
		var js JSONSchema
		if err := json.Unmarshal(s, &js); err == nil {
			return convertJSONSchemaToGoogle(js)
		}
		// If unmarshal fails, try the old map-based approach
		var m map[string]interface{}
		if err := json.Unmarshal(s, &m); err == nil {
			return convertSchemaToGoogleFormatWithDepth(m, 0, 10)
		}
		return s
	case map[string]interface{}:
		return convertSchemaToGoogleFormatWithDepth(s, 0, 10)
	default:
		return schema
	}
}

func convertJSONSchemaToGoogle(schema JSONSchema) map[string]interface{} {
	result := make(map[string]interface{})

	if schema.Type != "" {
		t, err := convertTypeToGoogle(schema.Type)
		if err != nil {
			// Default to OBJECT for unknown types
			logger.GetLogger().Debugf("convertJSONSchemaToGoogle: unknown type %q: %v; defaulting to OBJECT", schema.Type, err)
			t = GoogleTypeObject
		}
		result["type"] = t
	}

	if len(schema.Properties) > 0 {
		props := make(map[string]interface{})
		for key, value := range schema.Properties {
			props[key] = convertJSONSchemaToGoogle(value)
		}
		result["properties"] = props
	}

	if schema.Items != nil {
		result["items"] = convertJSONSchemaToGoogle(*schema.Items)
	}

	if len(schema.Required) > 0 {
		result["required"] = schema.Required
	}

	if len(schema.Enum) > 0 {
		result["enum"] = schema.Enum
	}

	if schema.Description != "" {
		result["description"] = schema.Description
	}

	if schema.Default != nil {
		result["default"] = schema.Default
	}

	if schema.Minimum != nil {
		result["minimum"] = *schema.Minimum
	}

	if schema.Maximum != nil {
		result["maximum"] = *schema.Maximum
	}

	if schema.MinLength != nil {
		result["minLength"] = *schema.MinLength
	}

	if schema.MaxLength != nil {
		result["maxLength"] = *schema.MaxLength
	}

	if schema.Pattern != "" {
		result["pattern"] = schema.Pattern
	}

	// Google doesn't use additionalProperties, title, $schema, or $id

	return result
}

func convertSchemaToGoogleFormatWithDepth(schema interface{}, depth, maxDepth int) interface{} {
	if depth > maxDepth {
		return schema // Stop recursion at max depth
	}

	switch s := schema.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{})
		for key, value := range s {
			switch key {
			case "type":
				// Convert type to Google's TYPE enum format
				if typeStr, ok := value.(string); ok {
					t, err := convertTypeToGoogle(typeStr)
					if err != nil {
						// Default to OBJECT for unknown types
						logger.GetLogger().Debugf("convertSchemaToGoogleFormatWithDepth: unknown type %q: %v; defaulting to OBJECT", typeStr, err)
						t = GoogleTypeObject
					}
					result["type"] = t
				} else {
					result[key] = value
				}
			case "properties":
				// Recursively convert nested properties
				if props, ok := value.(map[string]interface{}); ok {
					convertedProps := make(map[string]interface{})
					for propKey, propValue := range props {
						convertedProps[propKey] = convertSchemaToGoogleFormatWithDepth(propValue, depth+1, maxDepth)
					}
					result["properties"] = convertedProps
				} else {
					result[key] = value
				}
			case "items":
				// Recursively convert array items
				result["items"] = convertSchemaToGoogleFormatWithDepth(value, depth+1, maxDepth)
			case "required":
				// Keep required fields as is
				result[key] = value
			case "enum":
				// Convert enum values to []interface{} for consistency
				if enumSlice, ok := value.([]interface{}); ok {
					result[key] = enumSlice
				} else if enumStrings, ok := value.([]string); ok {
					// Convert []string to []interface{}
					converted := make([]interface{}, len(enumStrings))
					for i, v := range enumStrings {
						converted[i] = v
					}
					result[key] = converted
				} else if enumInts, ok := value.([]int); ok {
					// Convert []int to []interface{}
					converted := make([]interface{}, len(enumInts))
					for i, v := range enumInts {
						converted[i] = v
					}
					result[key] = converted
				} else {
					// Keep as is if we can't convert
					result[key] = value
				}
			case "description":
				// Keep descriptions
				result[key] = value
			case "default":
				// Keep default values
				result[key] = value
			case "minimum", "maximum", "minLength", "maxLength":
				// Keep validation constraints
				result[key] = value
			case "pattern":
				// Keep regex patterns
				result[key] = value
			case "additionalProperties":
				// Google doesn't use additionalProperties, skip it
				continue
			case "$schema", "title", "$id":
				// Skip metadata fields that Google doesn't use
				continue
			default:
				// Keep any other fields as-is
				result[key] = value
			}
		}
		return result
	default:
		return schema
	}
}

// convertTypeToGoogle converts JSON schema types to Google's uppercase format.
// Returns an error for unknown types. Callers should handle errors appropriately:
// - Use default fallback (OBJECT) for lenient conversion
// - Propagate error for strict validation
func convertTypeToGoogle(jsonType string) (string, error) {
	// Google uses uppercase types
	switch jsonType {
	case "string":
		return GoogleTypeString, nil
	case "number":
		return GoogleTypeNumber, nil
	case "integer":
		return GoogleTypeInteger, nil
	case "boolean":
		return GoogleTypeBoolean, nil
	case "array":
		return GoogleTypeArray, nil
	case "object":
		return GoogleTypeObject, nil
	default:
		return "", fmt.Errorf("unknown JSON schema type: %s", jsonType)
	}
}
