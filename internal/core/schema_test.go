package core

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestConvertSchemaToGoogleFormatEdgeCases tests the conversion of JSON Schema to Google's format
func TestConvertSchemaToGoogleFormatEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]interface{}
		expected map[string]interface{}
	}{
		{
			name: "basic types",
			input: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "User's name",
					},
					"age": map[string]interface{}{
						"type":        "integer",
						"description": "User's age",
					},
					"active": map[string]interface{}{
						"type":        "boolean",
						"description": "Is user active",
					},
				},
				"required": []string{"name"},
			},
			expected: map[string]interface{}{
				"type": "OBJECT",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "STRING",
						"description": "User's name",
					},
					"age": map[string]interface{}{
						"type":        "INTEGER",
						"description": "User's age",
					},
					"active": map[string]interface{}{
						"type":        "BOOLEAN",
						"description": "Is user active",
					},
				},
				"required": []string{"name"},
			},
		},
		{
			name: "nested object with additionalProperties",
			input: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"metadata": map[string]interface{}{
						"type": "object",
						"additionalProperties": map[string]interface{}{
							"type": "string",
						},
						"description": "Arbitrary metadata",
					},
				},
				"additionalProperties": false,
			},
			expected: map[string]interface{}{
				"type": "OBJECT",
				"properties": map[string]interface{}{
					"metadata": map[string]interface{}{
						"type":        "OBJECT",
						"description": "Arbitrary metadata",
						// additionalProperties should be removed for Google
					},
				},
				// top-level additionalProperties should also be removed
			},
		},
		{
			name: "array with items",
			input: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"tags": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "string",
						},
						"description": "List of tags",
					},
					"numbers": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "number",
						},
					},
				},
			},
			expected: map[string]interface{}{
				"type": "OBJECT",
				"properties": map[string]interface{}{
					"tags": map[string]interface{}{
						"type": "ARRAY",
						"items": map[string]interface{}{
							"type": "STRING",
						},
						"description": "List of tags",
					},
					"numbers": map[string]interface{}{
						"type": "ARRAY",
						"items": map[string]interface{}{
							"type": "NUMBER",
						},
					},
				},
			},
		},
		{
			name: "enum values",
			input: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"status": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"active", "inactive", "pending"},
						"description": "User status",
					},
					"priority": map[string]interface{}{
						"type": "integer",
						"enum": []int{1, 2, 3, 4, 5},
					},
				},
			},
			expected: map[string]interface{}{
				"type": "OBJECT",
				"properties": map[string]interface{}{
					"status": map[string]interface{}{
						"type":        "STRING",
						"enum":        []interface{}{"active", "inactive", "pending"},
						"description": "User status",
					},
					"priority": map[string]interface{}{
						"type": "INTEGER",
						"enum": []interface{}{1, 2, 3, 4, 5},
					},
				},
			},
		},
		{
			name: "deeply nested with required propagation",
			input: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"user": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"profile": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"name": map[string]interface{}{
										"type": "string",
									},
									"email": map[string]interface{}{
										"type": "string",
									},
								},
								"required": []string{"name", "email"},
							},
						},
						"required": []string{"profile"},
					},
				},
				"required": []string{"user"},
			},
			expected: map[string]interface{}{
				"type": "OBJECT",
				"properties": map[string]interface{}{
					"user": map[string]interface{}{
						"type": "OBJECT",
						"properties": map[string]interface{}{
							"profile": map[string]interface{}{
								"type": "OBJECT",
								"properties": map[string]interface{}{
									"name": map[string]interface{}{
										"type": "STRING",
									},
									"email": map[string]interface{}{
										"type": "STRING",
									},
								},
								"required": []string{"name", "email"},
							},
						},
						"required": []string{"profile"},
					},
				},
				"required": []string{"user"},
			},
		},
		{
			name: "complex array of objects",
			input: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"items": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"id": map[string]interface{}{
									"type": "integer",
								},
								"name": map[string]interface{}{
									"type": "string",
								},
							},
							"required":             []string{"id"},
							"additionalProperties": true,
						},
					},
				},
			},
			expected: map[string]interface{}{
				"type": "OBJECT",
				"properties": map[string]interface{}{
					"items": map[string]interface{}{
						"type": "ARRAY",
						"items": map[string]interface{}{
							"type": "OBJECT",
							"properties": map[string]interface{}{
								"id": map[string]interface{}{
									"type": "INTEGER",
								},
								"name": map[string]interface{}{
									"type": "STRING",
								},
							},
							"required": []string{"id"},
							// additionalProperties removed
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertSchemaToGoogleFormat(tt.input)

			// Compare JSON representations for better error messages
			resultJSON, _ := json.MarshalIndent(result, "", "  ")
			expectedJSON, _ := json.MarshalIndent(tt.expected, "", "  ")

			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("ConvertSchemaToGoogleFormat() mismatch\nGot:\n%s\nExpected:\n%s",
					resultJSON, expectedJSON)
			}
		})
	}
}

// TestConvertToolsForArgoModelEdgeCases tests the tool conversion for different Argo models
func TestConvertToolsForArgoModelEdgeCases(t *testing.T) {
	// Create test tool definitions
	basicTool := ToolDefinition{
		Name:        "get_weather",
		Description: "Get weather information",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"location": map[string]interface{}{
					"type":        "string",
					"description": "City name",
				},
			},
			"required": []string{"location"},
		},
	}

	complexTool := ToolDefinition{
		Name:        "search_files",
		Description: "Search for files",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type": "string",
				},
				"filters": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"extension": map[string]interface{}{
							"type": "string",
							"enum": []string{".go", ".js", ".py"},
						},
						"max_size": map[string]interface{}{
							"type": "integer",
						},
					},
					"additionalProperties": false,
				},
			},
			"required": []string{"query"},
		},
	}

	tests := []struct {
		name      string
		model     string
		tools     []ToolDefinition
		expectNil bool
		validate  func(t *testing.T, result interface{})
	}{
		{
			name:      "OpenAI model returns tools unchanged",
			model:     "gpt-4",
			tools:     []ToolDefinition{basicTool, complexTool},
			expectNil: false,
			validate: func(t *testing.T, result interface{}) {
				tools, ok := result.([]OpenAITool)
				if !ok {
					t.Fatal("Expected []OpenAITool for OpenAI tools")
				}
				if len(tools) != 2 {
					t.Errorf("Expected 2 tools, got %d", len(tools))
				}
				// Check that tools have the OpenAI format with type/function wrapper
				if tools[0].Type != "function" {
					t.Error("Expected OpenAI tool to have type 'function'")
				}
				if tools[0].Function.Name != "get_weather" {
					t.Errorf("Expected first tool name 'get_weather', got %v", tools[0].Function.Name)
				}
			},
		},
		{
			name:      "Anthropic model returns tools unchanged",
			model:     "claude-3-opus",
			tools:     []ToolDefinition{basicTool},
			expectNil: false,
			validate: func(t *testing.T, result interface{}) {
				tools, ok := result.([]AnthropicTool)
				if !ok {
					t.Fatal("Expected []AnthropicTool for Anthropic tools")
				}
				if len(tools) != 1 {
					t.Errorf("Expected 1 tool, got %d", len(tools))
				}
				// Check that tools have the Anthropic format
				if tools[0].Name != "get_weather" {
					t.Errorf("Expected tool name 'get_weather', got %v", tools[0].Name)
				}
				if tools[0].Description == "" || tools[0].InputSchema == nil {
					t.Error("Expected Anthropic tool to have description and input_schema fields")
				}
			},
		},
		{
			name:      "Google model converts to function declarations",
			model:     "gemini-pro",
			tools:     []ToolDefinition{basicTool, complexTool},
			expectNil: false,
			validate: func(t *testing.T, result interface{}) {
				googleTools, ok := result.([]GoogleTool)
				if !ok {
					t.Fatal("Expected []GoogleTool for Google tools")
				}
				if len(googleTools) != 1 {
					t.Errorf("Expected 1 GoogleTool container, got %d", len(googleTools))
				}

				// Check function declarations
				if len(googleTools[0].FunctionDeclarations) != 2 {
					t.Errorf("Expected 2 function declarations, got %d", len(googleTools[0].FunctionDeclarations))
				}

				// Check first function
				if googleTools[0].FunctionDeclarations[0].Name != "get_weather" {
					t.Errorf("Expected name 'get_weather', got %v", googleTools[0].FunctionDeclarations[0].Name)
				}

				// Check that additionalProperties was removed from complex tool
				params := googleTools[0].FunctionDeclarations[1].Parameters
				if params != nil {
					if props, ok := params["properties"].(map[string]interface{}); ok {
						if filters, ok := props["filters"].(map[string]interface{}); ok {
							if _, hasAdditional := filters["additionalProperties"]; hasAdditional {
								t.Error("additionalProperties should be removed for Google")
							}
						}
					}
				}
			},
		},
		{
			name:      "Unknown model defaults to OpenAI format",
			model:     "unknown-model",
			tools:     []ToolDefinition{basicTool},
			expectNil: false,
			validate: func(t *testing.T, result interface{}) {
				tools, ok := result.([]OpenAITool)
				if !ok {
					t.Fatal("Expected []OpenAITool for unknown model (defaults to OpenAI)")
				}
				if len(tools) != 1 {
					t.Errorf("Expected 1 tool, got %d", len(tools))
				}
				// Check that it uses OpenAI format
				if tools[0].Type != "function" {
					t.Error("Expected unknown model to default to OpenAI format with type 'function'")
				}
				if tools[0].Function.Name != "get_weather" {
					t.Errorf("Expected tool name 'get_weather', got %v", tools[0].Function.Name)
				}
			},
		},
		{
			name:      "Empty tools returns nil tools",
			model:     "gpt-4",
			tools:     []ToolDefinition{},
			expectNil: true,
			validate:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertToolsForProvider(tt.model, tt.tools, nil)

			if tt.expectNil {
				if result.Tools != nil {
					t.Errorf("Expected nil tools, got %v", result.Tools)
				}
			} else {
				if result.Tools == nil {
					t.Fatal("Expected non-nil tools")
				}
				if tt.validate != nil {
					tt.validate(t, result.Tools)
				}
			}
		})
	}
}

// TestSchemaEdgeCases tests specific edge cases in schema conversion
func TestSchemaEdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		input interface{} // Changed from map[string]interface{} to interface{}
		check func(t *testing.T, result interface{})
	}{
		{
			name:  "nil input returns nil",
			input: nil,
			check: func(t *testing.T, result interface{}) {
				if result != nil {
					t.Error("Expected nil for nil input")
				}
			},
		},
		{
			name:  "empty object",
			input: map[string]interface{}{},
			check: func(t *testing.T, result interface{}) {
				resultMap, ok := result.(map[string]interface{})
				if !ok {
					t.Error("Expected map[string]interface{} for empty object")
					return
				}
				if resultMap == nil {
					t.Error("Expected non-nil empty map")
				}
				if len(resultMap) != 0 {
					t.Errorf("Expected empty map, got %d keys", len(resultMap))
				}
			},
		},
		{
			name: "preserves unknown fields",
			input: map[string]interface{}{
				"type":         "object",
				"customField":  "value",
				"anotherField": 123,
			},
			check: func(t *testing.T, result interface{}) {
				resultMap, ok := result.(map[string]interface{})
				if !ok {
					t.Error("Expected map[string]interface{}")
					return
				}
				if resultMap["customField"] != "value" {
					t.Error("Custom field not preserved")
				}
				if resultMap["anotherField"] != 123 {
					t.Error("Another field not preserved")
				}
			},
		},
		{
			name: "handles malformed required field",
			input: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"field": map[string]interface{}{"type": "string"},
				},
				"required": "not-an-array", // malformed
			},
			check: func(t *testing.T, result interface{}) {
				resultMap, ok := result.(map[string]interface{})
				if !ok {
					t.Error("Expected map[string]interface{}")
					return
				}
				// Should preserve the malformed field as-is
				if resultMap["required"] != "not-an-array" {
					t.Error("Malformed required field not preserved")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertSchemaToGoogleFormat(tt.input)
			tt.check(t, result)
		})
	}
}
