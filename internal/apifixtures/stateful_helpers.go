package apifixtures

import (
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

var statefulPlaceholderPattern = regexp.MustCompile(`\$\{([A-Za-z0-9_]+)\}`)

func SubstituteStatefulValue(value interface{}, vars map[string]string) interface{} {
	switch typed := value.(type) {
	case string:
		return SubstituteStatefulString(typed, vars)
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i := range typed {
			out[i] = SubstituteStatefulValue(typed[i], vars)
		}
		return out
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			out[key] = SubstituteStatefulValue(item, vars)
		}
		return out
	default:
		return value
	}
}

func SubstituteStatefulString(value string, vars map[string]string) string {
	return statefulPlaceholderPattern.ReplaceAllStringFunc(value, func(match string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(match, "${"), "}")
		if replacement, ok := vars[name]; ok {
			return replacement
		}
		return match
	})
}

func StatefulFieldsMatch(decoded map[string]interface{}, fields map[string]interface{}, vars map[string]string) bool {
	for path, rawWant := range fields {
		want := SubstituteStatefulValue(rawWant, vars)
		got, ok := LookupStatefulJSONPath(decoded, path)
		if !ok || !StatefulValuesEqual(got, want) {
			return false
		}
	}
	return true
}

func LookupStatefulJSONPath(decoded interface{}, path string) (interface{}, bool) {
	current := decoded
	for _, part := range strings.Split(path, ".") {
		switch typed := current.(type) {
		case map[string]interface{}:
			value, ok := typed[part]
			if !ok {
				return nil, false
			}
			current = value
		case []interface{}:
			if part == "length" {
				current = float64(len(typed))
				continue
			}
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			current = typed[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func StatefulValuesEqual(got, want interface{}) bool {
	gotNumber, gotIsNumber := statefulNumber(got)
	wantNumber, wantIsNumber := statefulNumber(want)
	if gotIsNumber && wantIsNumber {
		return gotNumber == wantNumber
	}
	return reflect.DeepEqual(got, want)
}

func statefulNumber(value interface{}) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}
