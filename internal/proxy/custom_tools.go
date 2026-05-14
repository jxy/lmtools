package proxy

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"strings"
)

const maxOpenAIToolNameLen = 64

type responseToolNameMapping struct {
	Namespace string
	Name      string
	Type      string
}

type responseToolNameRegistry map[string]responseToolNameMapping

func openAICustomToolMap(value interface{}) map[string]interface{} {
	switch typed := value.(type) {
	case nil:
		return nil
	case map[string]interface{}:
		return cloneMapInterface(typed)
	case map[string]string:
		out := make(map[string]interface{}, len(typed))
		for k, v := range typed {
			out[k] = v
		}
		return out
	default:
		return nil
	}
}

func chatCustomToolFormatFromResponses(format interface{}) interface{} {
	src := openAICustomToolMap(format)
	if len(src) == 0 {
		return format
	}
	if src["type"] != "grammar" {
		return src
	}
	if _, ok := src["grammar"]; ok {
		return src
	}
	grammar := map[string]interface{}{}
	if syntax, ok := src["syntax"]; ok {
		grammar["syntax"] = syntax
		delete(src, "syntax")
	}
	if definition, ok := src["definition"]; ok {
		grammar["definition"] = definition
		delete(src, "definition")
	}
	if len(grammar) > 0 {
		src["grammar"] = grammar
	}
	return src
}

func responsesCustomToolFormatFromChat(format interface{}) interface{} {
	src := openAICustomToolMap(format)
	if len(src) == 0 {
		return format
	}
	if src["type"] != "grammar" {
		return src
	}
	grammar := openAICustomToolMap(src["grammar"])
	if len(grammar) == 0 {
		return src
	}
	delete(src, "grammar")
	if syntax, ok := grammar["syntax"]; ok {
		src["syntax"] = syntax
	}
	if definition, ok := grammar["definition"]; ok {
		src["definition"] = definition
	}
	return src
}

func responseCustomToolInput(value interface{}) string {
	text, _ := value.(string)
	return text
}

func rawJSONStringValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

func flattenNamespaceToolName(namespace, name string) string {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	flat := namespace + name
	if namespace != "" && name != "" && !strings.HasSuffix(namespace, "_") && !strings.HasSuffix(namespace, "-") {
		flat = namespace + "__" + name
	}
	if len(flat) <= maxOpenAIToolNameLen {
		return flat
	}
	sum := sha1.Sum([]byte(flat))
	suffix := "_" + hex.EncodeToString(sum[:])[:8]
	prefixLen := maxOpenAIToolNameLen - len(suffix)
	if prefixLen <= 0 {
		return suffix[1:]
	}
	return flat[:prefixLen] + suffix
}

func namespaceToolDescription(namespace, namespaceDescription, originalName, toolDescription string) string {
	parts := make([]string, 0, 3)
	namespaceLine := "Namespace: " + namespace + "."
	if namespaceDescription != "" {
		namespaceLine += " " + namespaceDescription
	}
	parts = append(parts, namespaceLine)
	if originalName != "" {
		parts = append(parts, "Original tool name: "+originalName+".")
	}
	if toolDescription != "" {
		parts = append(parts, toolDescription)
	}
	return strings.Join(parts, "\n")
}

func responseToolNameRegistryFromCoreTools(tools []core.ToolDefinition) responseToolNameRegistry {
	registry := make(responseToolNameRegistry)
	for _, tool := range tools {
		if tool.Name == "" {
			continue
		}
		toolType := tool.Type
		if toolType == "" {
			toolType = "function"
		}
		originalName := tool.OriginalName
		if originalName == "" {
			originalName = tool.Name
		}
		registry[tool.Name] = responseToolNameMapping{
			Namespace: tool.Namespace,
			Name:      originalName,
			Type:      toolType,
		}
	}
	if len(registry) == 0 {
		return nil
	}
	return registry
}

func (r responseToolNameRegistry) resolve(flatName, toolType string) (responseToolNameMapping, bool) {
	if len(r) == 0 {
		return responseToolNameMapping{}, false
	}
	mapping, ok := r[flatName]
	if !ok {
		return responseToolNameMapping{}, false
	}
	if toolType != "" && mapping.Type != "" && mapping.Type != toolType {
		return responseToolNameMapping{}, false
	}
	return mapping, true
}

func responseOutputToolName(item OpenAIResponsesOutputItem) string {
	if item.Namespace == "" {
		return item.Name
	}
	return flattenNamespaceToolName(item.Namespace, item.Name)
}

func anthropicCustomToolInput(input map[string]interface{}, inputString string) string {
	if inputString != "" {
		return inputString
	}
	if rawInput, ok := core.UnwrapCustomToolInputValue(input); ok {
		return rawInput
	}
	if input == nil {
		return ""
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func anthropicCustomToolInputFromJSON(input string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	if rawInput, ok := core.UnwrapCustomToolInput(json.RawMessage(input)); ok {
		return rawInput
	}
	return input
}

func toolSchemaToInterface(schema interface{}) interface{} {
	switch value := schema.(type) {
	case json.RawMessage:
		return rawJSONToInterface(value)
	case []byte:
		return rawJSONToInterface(json.RawMessage(value))
	default:
		return schema
	}
}

func duplicateFlattenedToolNameError(name string) error {
	return fmt.Errorf("responses namespace tool flattening produced duplicate Chat tool name %q", name)
}
