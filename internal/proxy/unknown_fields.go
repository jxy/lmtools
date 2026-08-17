package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/logger"
	"reflect"
	"sort"
	"strings"
	"sync"
)

func warnUnknownFields(ctx context.Context, jsonData []byte, v interface{}, source string) {
	warnUnknownFieldsWithDisposition(ctx, jsonData, v, source, "ignored")
}

func warnUnknownFieldsWithDisposition(ctx context.Context, jsonData []byte, v interface{}, source, disposition string) {
	log := logger.From(ctx)
	if !log.IsWarnEnabled() {
		return
	}
	unknownFields, truncated, err := detectUnknownFieldPaths(jsonData, v)
	if err != nil {
		log.Debugf("Failed to detect unknown fields in %s: %v", source, err)
		return
	}
	if len(unknownFields) == 0 {
		return
	}
	if disposition == "" {
		disposition = "ignored"
	}
	list := strings.Join(unknownFields, ", ")
	if truncated {
		list += ", and more"
	}
	log.Warnf("Unknown JSON fields in %s (%s): %s", source, disposition, list)
}

func detectUnknownFieldPaths(jsonData []byte, v interface{}) ([]string, bool, error) {
	paths, truncated, err := scanUnknownFieldPaths(jsonData, reflect.TypeOf(v))
	if err != nil {
		return nil, false, err
	}
	sort.Strings(paths)
	return paths, truncated, nil
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

// structJSONFieldTypes caches the field map per struct type. The scanner asks
// for one on entry to every object it descends into, and a body carrying a long
// array of messages descends into thousands, so building the map each time made
// the scanner's cost scale with the payload it was written to avoid scaling
// with. A type's fields do not change at runtime, and the map is read-only from
// here on, so one copy per type serves every request for the life of the
// process — bounded by the number of struct types in the binary.
var structJSONFieldTypes sync.Map

type structJSONFieldType struct {
	// name is the canonical, allocation-stable path segment returned by a map
	// lookup whose temporary string may point into request or decode storage.
	name       string
	targetType reflect.Type
}

func getStructJSONFieldTypes(targetType reflect.Type) map[string]structJSONFieldType {
	if cached, ok := structJSONFieldTypes.Load(targetType); ok {
		return cached.(map[string]structJSONFieldType)
	}
	fields := buildStructJSONFieldTypes(targetType)
	structJSONFieldTypes.Store(targetType, fields)
	return fields
}

func buildStructJSONFieldTypes(targetType reflect.Type) map[string]structJSONFieldType {
	fields := make(map[string]structJSONFieldType)
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
		fields[name] = structJSONFieldType{name: name, targetType: field.Type}
	}
	return fields
}

func joinJSONPath(prefix, field string) string {
	if prefix == "" {
		return field
	}
	return prefix + "." + field
}
