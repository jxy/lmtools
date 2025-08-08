package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// detectUnknownFields compares the raw JSON with the struct fields to find unknown fields
func detectUnknownFields(jsonData []byte, v interface{}) ([]string, error) {
	// Parse JSON into a map
	var jsonMap map[string]interface{}
	if err := json.Unmarshal(jsonData, &jsonMap); err != nil {
		return nil, err
	}

	// Get struct field names using reflection
	structFields := getStructFieldJSONNames(v)

	// Find fields in JSON that are not in struct
	var unknownFields []string
	for key := range jsonMap {
		if !contains(structFields, key) {
			unknownFields = append(unknownFields, key)
		}
	}

	return unknownFields, nil
}

// getStructFieldJSONNames returns all JSON field names from a struct
func getStructFieldJSONNames(v interface{}) []string {
	var fields []string

	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct {
		return fields
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		jsonTag := field.Tag.Get("json")
		if jsonTag == "" {
			continue
		}

		// Handle json tags like "field,omitempty"
		parts := strings.Split(jsonTag, ",")
		if parts[0] != "" && parts[0] != "-" {
			fields = append(fields, parts[0])
		}
	}

	return fields
}

// contains checks if a string slice contains a string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// logUnknownFields logs any unknown fields found in the request
func logUnknownFields(ctx context.Context, jsonData []byte, v interface{}, requestType string) {
	unknownFields, err := detectUnknownFields(jsonData, v)
	if err != nil {
		LogDebugCtx(ctx, fmt.Sprintf("Failed to detect unknown fields in %s: %v", requestType, err))
		return
	}

	if len(unknownFields) > 0 {
		// Extract values for the unknown fields
		var jsonMap map[string]interface{}
		if err := json.Unmarshal(jsonData, &jsonMap); err != nil {
			LogDebugCtx(ctx, fmt.Sprintf("Failed to unmarshal JSON for unknown field values in %s: %v", requestType, err))
			return
		}

		fieldInfo := make(map[string]interface{})
		for _, field := range unknownFields {
			fieldInfo[field] = jsonMap[field]
		}

		LogDebugCtx(ctx, fmt.Sprintf("Unknown fields in %s (will be ignored): %v", requestType, fieldInfo))
	}
}
