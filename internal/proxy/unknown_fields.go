package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/logger"
	"reflect"
	"sort"
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

// logUnknownFields detects and logs unknown JSON fields (debug only).
// Performance: Only runs when debug logging is enabled due to expensive reflection.
func logUnknownFields(ctx context.Context, jsonData []byte, v interface{}, requestType string) {
	// Skip expensive reflection if debug logging is not enabled
	if !logger.From(ctx).IsDebugEnabled() {
		return
	}

	unknownFields, err := detectUnknownFields(jsonData, v)
	if err != nil {
		logger.From(ctx).Debugf("Failed to detect unknown fields in %s: %v", requestType, err)
		return
	}

	if len(unknownFields) > 0 {
		// Extract values for the unknown fields
		var jsonMap map[string]interface{}
		if err := json.Unmarshal(jsonData, &jsonMap); err != nil {
			logger.From(ctx).Debugf("Failed to unmarshal JSON for unknown field values in %s: %v", requestType, err)
			return
		}

		fieldInfo := make(map[string]interface{})
		for _, field := range unknownFields {
			fieldInfo[field] = jsonMap[field]
		}

		logger.From(ctx).Debugf("Unknown fields in %s (will be ignored): %+v", requestType, fieldInfo)
	}
}

func warnUnknownFields(ctx context.Context, jsonData []byte, v interface{}, source string) {
	warnUnknownFieldsWithDisposition(ctx, jsonData, v, source, "ignored")
}

func warnUnknownFieldsWithDisposition(ctx context.Context, jsonData []byte, v interface{}, source, disposition string) {
	unknownFields, err := detectUnknownFieldPaths(jsonData, v)
	if err != nil {
		logger.From(ctx).Debugf("Failed to detect unknown fields in %s: %v", source, err)
		return
	}
	if len(unknownFields) == 0 {
		return
	}
	if disposition == "" {
		disposition = "ignored"
	}
	logger.From(ctx).Warnf("Unknown JSON fields in %s (%s): %s", source, disposition, strings.Join(unknownFields, ", "))
}

func detectUnknownFieldPaths(jsonData []byte, v interface{}) ([]string, error) {
	var decoded interface{}
	if err := json.Unmarshal(jsonData, &decoded); err != nil {
		return nil, err
	}

	paths := detectUnknownFieldPathsForValue(decoded, reflect.TypeOf(v), "")
	sort.Strings(paths)
	return compactStrings(paths), nil
}

func detectUnknownFieldPathsForValue(value interface{}, targetType reflect.Type, prefix string) []string {
	targetType = dereferenceType(targetType)
	if targetType == nil {
		return nil
	}
	if shouldSkipUnknownFieldDetection(targetType) {
		return nil
	}

	switch typedValue := value.(type) {
	case map[string]interface{}:
		if targetType.Kind() != reflect.Struct {
			return nil
		}
		fieldTypes := getStructJSONFieldTypes(targetType)
		paths := make([]string, 0)
		for key, child := range typedValue {
			childType, ok := fieldTypes[key]
			childPath := joinJSONPath(prefix, key)
			if !ok {
				paths = append(paths, childPath)
				continue
			}
			paths = append(paths, detectUnknownFieldPathsForValue(child, childType, childPath)...)
		}
		return paths
	case []interface{}:
		switch targetType.Kind() {
		case reflect.Slice, reflect.Array:
			childType := targetType.Elem()
			childPath := prefix
			if childPath != "" {
				childPath += "[]"
			}
			paths := make([]string, 0)
			for _, child := range typedValue {
				paths = append(paths, detectUnknownFieldPathsForValue(child, childType, childPath)...)
			}
			return paths
		default:
			return nil
		}
	default:
		return nil
	}
}

func dereferenceType(targetType reflect.Type) reflect.Type {
	for targetType != nil && targetType.Kind() == reflect.Ptr {
		targetType = targetType.Elem()
	}
	return targetType
}

func shouldSkipUnknownFieldDetection(targetType reflect.Type) bool {
	if targetType == nil {
		return true
	}
	if targetType == reflect.TypeOf(json.RawMessage{}) {
		return true
	}
	switch targetType.Kind() {
	case reflect.Interface, reflect.Map:
		return true
	default:
		return false
	}
}

func getStructJSONFieldTypes(targetType reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	for i := 0; i < targetType.NumField(); i++ {
		field := targetType.Field(i)
		if field.PkgPath != "" {
			continue
		}

		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := field.Name
		if tag != "" {
			parts := strings.Split(tag, ",")
			if parts[0] == "-" {
				continue
			}
			if parts[0] != "" {
				name = parts[0]
			}
		}
		fields[name] = field.Type
	}
	return fields
}

func joinJSONPath(prefix, field string) string {
	if prefix == "" {
		return field
	}
	return prefix + "." + field
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := values[:0]
	var previous string
	for i, value := range values {
		if i > 0 && value == previous {
			continue
		}
		out = append(out, value)
		previous = value
	}
	return out
}
