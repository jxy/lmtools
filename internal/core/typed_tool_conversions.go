package core

import "encoding/json"

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
		openAITools = append(openAITools, OpenAITool{
			Type: "function",
			Function: OpenAIToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  marshalToolSchema(tool.InputSchema, nil),
			},
		})
	}
	return openAITools
}

// ConvertToolsToAnthropicTyped converts tool definitions to strongly typed Anthropic format.
func ConvertToolsToAnthropicTyped(tools []ToolDefinition) []AnthropicTool {
	anthropicTools := make([]AnthropicTool, 0, len(tools))
	for _, tool := range tools {
		anthropicTools = append(anthropicTools, AnthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: marshalToolSchema(tool.InputSchema, nil),
		})
	}
	return anthropicTools
}

// ConvertToolsToGoogleTyped converts tool definitions to strongly typed Google format.
func ConvertToolsToGoogleTyped(tools []ToolDefinition) []GoogleTool {
	if len(tools) == 0 {
		return []GoogleTool{}
	}

	declarations := make([]GoogleFunctionDeclaration, 0, len(tools))
	for _, tool := range tools {
		declarations = append(declarations, GoogleFunctionDeclaration{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  marshalToolSchema(tool.InputSchema, ConvertSchemaToGoogleFormat),
		})
	}

	return []GoogleTool{
		{
			FunctionDeclarations: declarations,
		},
	}
}
