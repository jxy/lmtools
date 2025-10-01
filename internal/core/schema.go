package core

// ConvertSchemaToGoogleFormat converts a JSON schema to Google's format
// This centralizes the conversion logic to avoid duplication
func ConvertSchemaToGoogleFormat(schema interface{}) interface{} {
	return convertSchemaToGoogleFormatWithDepth(schema, 0, 10)
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
					result["type"] = convertTypeToGoogle(typeStr)
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

func convertTypeToGoogle(jsonType string) string {
	// Google uses uppercase types
	switch jsonType {
	case "string":
		return "STRING"
	case "number":
		return "NUMBER"
	case "integer":
		return "INTEGER"
	case "boolean":
		return "BOOLEAN"
	case "array":
		return "ARRAY"
	case "object":
		return "OBJECT"
	default:
		return "STRING" // Default to string for unknown types
	}
}
