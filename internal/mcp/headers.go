package mcp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// The Streamable HTTP binding of 2026-07-28 mirrors selected body fields
// into headers so intermediaries can route without parsing JSON. Mcp-Name
// carries the tool name; a tool schema may mark a primitive argument with
// x-mcp-header, and the client must then send it as Mcp-Param-{name}. This
// file validates the annotations when a tool is listed and extracts the
// values when it is called.

const (
	headerProtocolVersion = "MCP-Protocol-Version"
	headerMethod          = "Mcp-Method"
	headerName            = "Mcp-Name"
	headerSessionID       = "Mcp-Session-Id"
	headerParamPrefix     = "Mcp-Param-"
	extensionHeaderKey    = "x-mcp-header"

	base64SentinelPrefix = "=?base64?"
	base64SentinelSuffix = "?="
	maxSchemaDepth       = 64
)

// headerParam is one annotated argument: the header name and the chain of
// property keys that reaches the value in the call's arguments.
type headerParam struct {
	name string
	path []string
}

// headerValue is one Mcp-Param header ready to send.
type headerValue struct {
	Name  string
	Value string
}

// toolHeaderParams reads the x-mcp-header annotations of an input schema
// and returns them, or an error naming the first constraint the schema
// breaks. The rules are the specification's: a non-empty token name, unique
// without regard to case, on a string, integer, or boolean property that is
// reachable from the root through properties keys alone.
func toolHeaderParams(schema json.RawMessage) ([]headerParam, error) {
	if len(schema) == 0 {
		return nil, nil
	}
	var root interface{}
	if err := json.Unmarshal(schema, &root); err != nil {
		return nil, fmt.Errorf("input schema is not JSON: %w", err)
	}
	object, ok := root.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("input schema is not an object")
	}
	var params []headerParam
	seen := make(map[string]bool)
	err := walkSchema(object, nil, true, 0, func(node map[string]interface{}, path []string, reachable bool) error {
		raw, present := node[extensionHeaderKey]
		if !present {
			return nil
		}
		name, ok := raw.(string)
		if !ok || name == "" {
			return fmt.Errorf("%s must be a non-empty string", extensionHeaderKey)
		}
		if !isToken(name) {
			return fmt.Errorf("%s value %q is not a valid header name", extensionHeaderKey, name)
		}
		if !reachable {
			return fmt.Errorf("%s %q is not on a property reachable through properties alone", extensionHeaderKey, name)
		}
		switch schemaType(node) {
		case "string", "integer", "boolean":
		default:
			return fmt.Errorf("%s %q is on a property that is not a string, integer, or boolean", extensionHeaderKey, name)
		}
		lower := strings.ToLower(name)
		if seen[lower] {
			return fmt.Errorf("%s %q is used twice", extensionHeaderKey, name)
		}
		seen[lower] = true
		params = append(params, headerParam{name: name, path: append([]string(nil), path...)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return params, nil
}

// walkSchema visits every object in a schema tree. path is the chain of
// property names to the node and reachable reports whether every step from
// the root was a properties key, which is what makes an annotation valid.
func walkSchema(node map[string]interface{}, path []string, reachable bool, depth int,
	visit func(node map[string]interface{}, path []string, reachable bool) error,
) error {
	if depth > maxSchemaDepth {
		return fmt.Errorf("input schema nests deeper than %d levels", maxSchemaDepth)
	}
	if err := visit(node, path, reachable); err != nil {
		return err
	}
	for key, value := range node {
		if key == "properties" {
			properties, ok := value.(map[string]interface{})
			if !ok {
				continue
			}
			for name, child := range properties {
				childNode, ok := child.(map[string]interface{})
				if !ok {
					continue
				}
				if err := walkSchema(childNode, append(append([]string(nil), path...), name), reachable, depth+1, visit); err != nil {
					return err
				}
			}
			continue
		}
		if err := walkSchemaValue(value, path, depth+1, visit); err != nil {
			return err
		}
	}
	return nil
}

// walkSchemaValue descends into any other keyword: everything under it is
// unreachable for header purposes but still has to be checked for stray
// annotations.
func walkSchemaValue(value interface{}, path []string, depth int,
	visit func(node map[string]interface{}, path []string, reachable bool) error,
) error {
	switch typed := value.(type) {
	case map[string]interface{}:
		return walkSchema(typed, path, false, depth, visit)
	case []interface{}:
		for _, item := range typed {
			if err := walkSchemaValue(item, path, depth, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

func schemaType(node map[string]interface{}) string {
	typed, _ := node["type"].(string)
	return typed
}

// headerValuesFor extracts the Mcp-Param values of one call. An argument
// that is absent or null contributes no header, as the specification says;
// one that is not the primitive its annotation promised is the model's
// error, reported so it can be corrected.
func headerValuesFor(params []headerParam, args json.RawMessage) ([]headerValue, error) {
	if len(params) == 0 {
		return nil, nil
	}
	var root interface{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &root); err != nil {
			return nil, fmt.Errorf("arguments are not JSON: %w", err)
		}
	}
	values := make([]headerValue, 0, len(params))
	for _, param := range params {
		value, present := lookupPath(root, param.path)
		if !present || value == nil {
			continue
		}
		text, err := headerString(value)
		if err != nil {
			return nil, fmt.Errorf("argument %s: %w", strings.Join(param.path, "."), err)
		}
		values = append(values, headerValue{Name: headerParamPrefix + param.name, Value: EncodeHeaderValue(text)})
	}
	return values, nil
}

func lookupPath(root interface{}, path []string) (interface{}, bool) {
	current := root
	for _, key := range path {
		object, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = object[key]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// headerString renders a primitive the way the binding says: strings as
// they are, integers in decimal, booleans in lowercase.
func headerString(value interface{}) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case bool:
		return strconv.FormatBool(typed), nil
	case float64:
		if typed != math.Trunc(typed) || math.Abs(typed) > 1<<53-1 {
			return "", fmt.Errorf("value %v is not an integer in the safe range", typed)
		}
		return strconv.FormatInt(int64(typed), 10), nil
	default:
		return "", fmt.Errorf("value is not a string, integer, or boolean")
	}
}

// EncodeHeaderValue wraps a value in the Base64 sentinel when it cannot
// travel as a plain header: anything outside visible ASCII plus space and
// tab, leading or trailing whitespace, or a value that already looks like
// the sentinel.
func EncodeHeaderValue(value string) string {
	if isPlainHeaderValue(value) && !looksLikeSentinel(value) {
		return value
	}
	return base64SentinelPrefix + base64.StdEncoding.EncodeToString([]byte(value)) + base64SentinelSuffix
}

func isPlainHeaderValue(value string) bool {
	if value == "" {
		return true
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '\t' || (c >= 0x20 && c <= 0x7e) {
			continue
		}
		return false
	}
	first, last := value[0], value[len(value)-1]
	return first != ' ' && first != '\t' && last != ' ' && last != '\t'
}

func looksLikeSentinel(value string) bool {
	return strings.HasPrefix(value, base64SentinelPrefix) && strings.HasSuffix(value, base64SentinelSuffix)
}

// isToken reports whether s is an HTTP token (RFC 9110 section 5.6.2).
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}
