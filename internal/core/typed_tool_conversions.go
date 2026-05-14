package core

import (
	"encoding/json"
	"strings"
)

var emptyToolSchemaJSON = json.RawMessage(`{"type":"object","properties":{}}`)

func defaultToolSchemaJSON() json.RawMessage {
	return append(json.RawMessage(nil), emptyToolSchemaJSON...)
}

func marshalToolSchema(inputSchema interface{}, transform func(interface{}) interface{}) json.RawMessage {
	schema := inputSchema
	if transform != nil && schema != nil {
		schema = transform(schema)
	}

	switch s := schema.(type) {
	case map[string]interface{}:
		if data, err := json.Marshal(s); err == nil {
			return json.RawMessage(data)
		}
	case json.RawMessage:
		if len(s) > 0 {
			return append(json.RawMessage(nil), s...)
		}
	case []byte:
		if len(s) > 0 {
			return json.RawMessage(append([]byte(nil), s...))
		}
	}

	return defaultToolSchemaJSON()
}

// ConvertToolsToOpenAITyped converts tool definitions to strongly typed OpenAI format.
func ConvertToolsToOpenAITyped(tools []ToolDefinition) []OpenAITool {
	openAITools := make([]OpenAITool, 0, len(tools))
	for _, tool := range tools {
		if tool.Type == "custom" {
			openAITools = append(openAITools, OpenAITool{
				Type: "custom",
				Custom: &OpenAICustomTool{
					Name:        tool.Name,
					Description: tool.Description,
					Format:      openAIChatCustomToolFormat(tool.Format),
				},
			})
			continue
		}
		openAITools = append(openAITools, OpenAITool{
			Type: "function",
			Function: OpenAIToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  marshalToolSchema(tool.InputSchema, nil),
				Strict:      tool.Strict,
			},
		})
	}
	return openAITools
}

// ConvertToolsToOpenAIChatCompatibleTyped converts tool definitions to the
// OpenAI Chat function-tool shape used by compatibility backends that do not
// support native OpenAI custom tools.
func ConvertToolsToOpenAIChatCompatibleTyped(tools []ToolDefinition) []OpenAITool {
	openAITools := make([]OpenAITool, 0, len(tools))
	for _, tool := range tools {
		if tool.Type == "custom" {
			openAITools = append(openAITools, OpenAITool{
				Type: "function",
				Function: OpenAIToolFunction{
					Name:        tool.Name,
					Description: customToolCompatibilityDescription(tool),
					Parameters:  marshalToolSchema(CustomToolInputSchema(), nil),
					Strict:      tool.Strict,
				},
			})
			continue
		}
		openAITools = append(openAITools, OpenAITool{
			Type: "function",
			Function: OpenAIToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  marshalToolSchema(tool.InputSchema, nil),
				Strict:      tool.Strict,
			},
		})
	}
	return openAITools
}

func openAIChatCustomToolFormat(format interface{}) interface{} {
	src, ok := format.(map[string]interface{})
	if !ok || len(src) == 0 {
		return format
	}
	out := make(map[string]interface{}, len(src)+1)
	for k, v := range src {
		out[k] = v
	}
	if out["type"] != "grammar" {
		return out
	}
	if _, ok := out["grammar"]; ok {
		return out
	}
	grammar := map[string]interface{}{}
	if syntax, ok := out["syntax"]; ok {
		grammar["syntax"] = syntax
		delete(out, "syntax")
	}
	if definition, ok := out["definition"]; ok {
		grammar["definition"] = definition
		delete(out, "definition")
	}
	if len(grammar) > 0 {
		out["grammar"] = grammar
	}
	return out
}

// ConvertToolsToAnthropicTyped converts tool definitions to strongly typed Anthropic format.
func ConvertToolsToAnthropicTyped(tools []ToolDefinition) []AnthropicTool {
	anthropicTools := make([]AnthropicTool, 0, len(tools))
	for _, tool := range tools {
		if tool.Type == "custom" {
			anthropicTools = append(anthropicTools, AnthropicTool{
				Name:        tool.Name,
				Description: customToolCompatibilityDescription(tool),
				InputSchema: marshalToolSchema(CustomToolInputSchema(), nil),
				Strict:      tool.Strict,
			})
			continue
		}
		anthropicTools = append(anthropicTools, AnthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: marshalToolSchema(tool.InputSchema, nil),
			Strict:      tool.Strict,
		})
	}
	return anthropicTools
}

func customToolCompatibilityDescription(tool ToolDefinition) string {
	parts := make([]string, 0, 3)
	if description := strings.TrimSpace(tool.Description); description != "" {
		parts = append(parts, description)
	}
	parts = append(parts, "This OpenAI custom tool is adapted for a function-tool backend. Pass the exact raw custom-tool input text in the `input` string field.")
	if format := customToolFormatDescription(tool.Format); format != "" {
		parts = append(parts, "Original custom tool format:\n"+format)
	}
	return strings.Join(parts, "\n\n")
}

func customToolFormatDescription(format interface{}) string {
	formatMap, ok := customToolFormatMap(format)
	if !ok {
		return ""
	}

	formatType, _ := formatMap["type"].(string)
	grammar := formatMap
	if nested, ok := customToolFormatMap(formatMap["grammar"]); ok {
		grammar = nested
	}
	syntax, _ := grammar["syntax"].(string)
	definition, _ := grammar["definition"].(string)

	lines := make([]string, 0, 3)
	if formatType != "" {
		lines = append(lines, "type: "+formatType)
	}
	if syntax != "" {
		lines = append(lines, "syntax: "+syntax)
	}
	if definition != "" {
		lines = append(lines, "definition:\n"+definition)
	}
	return strings.Join(lines, "\n")
}

func customToolFormatMap(value interface{}) (map[string]interface{}, bool) {
	switch typed := value.(type) {
	case map[string]interface{}:
		return typed, len(typed) > 0
	case map[string]string:
		out := make(map[string]interface{}, len(typed))
		for key, val := range typed {
			out[key] = val
		}
		return out, len(out) > 0
	default:
		return nil, false
	}
}

// ConvertToolsToGoogleTyped converts tool definitions to strongly typed Google format.
func ConvertToolsToGoogleTyped(tools []ToolDefinition) []GoogleTool {
	if len(tools) == 0 {
		return nil
	}

	declarations := make([]GoogleFunctionDeclaration, 0, len(tools))
	for _, tool := range tools {
		if tool.Type == "custom" {
			declarations = append(declarations, GoogleFunctionDeclaration{
				Name:        tool.Name,
				Description: customToolCompatibilityDescription(tool),
				Parameters:  marshalToolSchema(CustomToolInputSchema(), ConvertSchemaToGoogleFormat),
			})
			continue
		}
		declarations = append(declarations, GoogleFunctionDeclaration{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  marshalToolSchema(tool.InputSchema, ConvertSchemaToGoogleFormat),
		})
	}
	if len(declarations) == 0 {
		return nil
	}

	return []GoogleTool{
		{
			FunctionDeclarations: declarations,
		},
	}
}
