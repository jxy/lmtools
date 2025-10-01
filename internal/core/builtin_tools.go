package core

import "lmtools/internal/prompts"

// GetBuiltinUniversalCommandTool returns the built-in universal_command tool definition
func GetBuiltinUniversalCommandTool() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "universal_command",
			Description: prompts.UniversalCommandDescription,
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "string",
						},
						"description": prompts.UniversalCommandParamDescription,
					},
					"environ": map[string]interface{}{
						"type":        "object",
						"description": prompts.UniversalCommandEnvDescription,
						"additionalProperties": map[string]interface{}{
							"type": "string",
						},
					},
					"workdir": map[string]interface{}{
						"type":        "string",
						"description": prompts.UniversalCommandWorkdirDescription,
					},
					"timeout": map[string]interface{}{
						"type":        "integer",
						"description": prompts.UniversalCommandTimeoutDescription,
					},
				},
				"required": []string{"command"},
			},
		},
	}
}
