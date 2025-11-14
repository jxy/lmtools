package core

import (
	"encoding/json"
	"lmtools/internal/constants"
	"os"
	"strings"
	"testing"
	"time"
)

// testRequestConfigWithTools extends testRequestConfig to add tools support
type testRequestConfigWithTools struct {
	provider   string
	model      string
	system     string
	enableTool bool
	user       string
	env        string
}

func (t *testRequestConfigWithTools) GetUser() string               { return t.user }
func (t *testRequestConfigWithTools) GetProvider() string           { return t.provider }
func (t *testRequestConfigWithTools) GetModel() string              { return t.model }
func (t *testRequestConfigWithTools) GetSystem() string             { return t.system }
func (t *testRequestConfigWithTools) IsSystemExplicitlySet() bool   { return false }
func (t *testRequestConfigWithTools) GetInput() string              { return "" }
func (t *testRequestConfigWithTools) IsStreamChat() bool            { return false }
func (t *testRequestConfigWithTools) IsEmbed() bool                 { return false }
func (t *testRequestConfigWithTools) GetEnv() string                { return t.env }
func (t *testRequestConfigWithTools) GetMaxTokens() int             { return 0 }
func (t *testRequestConfigWithTools) GetAPIKey() string             { return "test-api-key" }
func (t *testRequestConfigWithTools) GetAPIKeyFile() string         { return "" }
func (t *testRequestConfigWithTools) GetProviderURL() string        { return "http://test.example.com" }
func (t *testRequestConfigWithTools) IsToolEnabled() bool           { return t.enableTool }
func (t *testRequestConfigWithTools) GetEffectiveSystem() string    { return t.system }
func (t *testRequestConfigWithTools) GetToolWhitelist() string      { return "" }
func (t *testRequestConfigWithTools) GetToolBlacklist() string      { return "" }
func (t *testRequestConfigWithTools) GetToolAutoApprove() bool      { return false }
func (t *testRequestConfigWithTools) GetToolNonInteractive() bool   { return false }
func (t *testRequestConfigWithTools) GetToolTimeout() time.Duration { return 30 * time.Second }
func (t *testRequestConfigWithTools) GetMaxToolRounds() int         { return 32 }
func (t *testRequestConfigWithTools) GetMaxToolParallel() int       { return 4 }
func (t *testRequestConfigWithTools) GetToolMaxOutputBytes() int    { return 1024 * 1024 }
func (t *testRequestConfigWithTools) GetResume() string             { return "" }
func (t *testRequestConfigWithTools) GetBranch() string             { return "" }

func TestBuildArgoChatRequestWithTools(t *testing.T) {
	// Create a sample tools file content
	toolsJSON := `[
		{
			"name": "get_weather",
			"description": "Get the current weather in a location",
			"input_schema": {
				"type": "object",
				"properties": {
					"location": {
						"type": "string",
						"description": "The city and state, e.g. San Francisco, CA"
					},
					"unit": {
						"type": "string",
						"enum": ["celsius", "fahrenheit"],
						"description": "The unit of temperature"
					}
				},
				"required": ["location"]
			}
		}
	]`

	// Write tools to a temporary file
	toolsFile := t.TempDir() + "/tools.json"
	if err := writeFile(toolsFile, []byte(toolsJSON)); err != nil {
		t.Fatalf("Failed to write tools file: %v", err)
	}

	tests := []struct {
		name        string
		model       string
		provider    string
		verifyTools func(t *testing.T, req map[string]interface{})
	}{
		{
			name:     "GPT model should use OpenAI format",
			model:    "gpt5",
			provider: "argo",
			verifyTools: func(t *testing.T, req map[string]interface{}) {
				// Tools should be in OpenAI format (wrapped with type/function)
				// The tools might be stored as []interface{} during JSON marshaling
				toolsInterface, ok := req["tools"].([]interface{})
				if !ok {
					t.Fatalf("Tools should be []interface{}, got %T", req["tools"])
				}

				if len(toolsInterface) != 1 {
					t.Fatalf("Expected 1 tool, got %d", len(toolsInterface))
				}

				// Convert to map for checking
				tool, ok := toolsInterface[0].(map[string]interface{})
				if !ok {
					t.Fatalf("Tool should be map[string]interface{}, got %T", toolsInterface[0])
				}

				// Check OpenAI format structure
				if tool["type"] != "function" {
					t.Errorf("Expected type='function', got %v", tool["type"])
				}

				function, ok := tool["function"].(map[string]interface{})
				if !ok {
					t.Fatal("Expected function field to be a map")
				}

				if function["name"] != "universal_command" {
					t.Errorf("Expected name='universal_command', got %v", function["name"])
				}

				// Check tool_choice - should be string "auto" for OpenAI
				if req["tool_choice"] != "auto" {
					t.Errorf("Expected tool_choice='auto', got %v", req["tool_choice"])
				}
			},
		},
		{
			name:     "Gemini model should use Google format",
			model:    "gemini25pro",
			provider: "argo",
			verifyTools: func(t *testing.T, req map[string]interface{}) {
				// Tools should be in Google format with functionDeclarations
				toolsInterface, ok := req["tools"].([]interface{})
				if !ok {
					t.Fatalf("Tools should be []interface{}, got %T", req["tools"])
				}

				if len(toolsInterface) != 1 {
					t.Fatalf("Expected 1 tool, got %d", len(toolsInterface))
				}

				tool, ok := toolsInterface[0].(map[string]interface{})
				if !ok {
					t.Fatalf("Tool should be map[string]interface{}, got %T", toolsInterface[0])
				}

				// Check for functionDeclarations field (Google format)
				funcDecls, ok := tool["functionDeclarations"].([]interface{})
				if !ok {
					t.Fatalf("Expected functionDeclarations field to be an array, got %T", tool["functionDeclarations"])
				}

				if len(funcDecls) != 1 {
					t.Fatalf("Expected 1 function declaration, got %d", len(funcDecls))
				}

				funcDecl, ok := funcDecls[0].(map[string]interface{})
				if !ok {
					t.Fatalf("Function declaration should be map[string]interface{}, got %T", funcDecls[0])
				}

				// Check function declaration structure
				if funcDecl["name"] != "universal_command" {
					t.Errorf("Expected name='universal_command', got %v", funcDecl["name"])
				}

				// Check parameters field exists
				params, ok := funcDecl["parameters"].(map[string]interface{})
				if !ok {
					t.Fatal("Expected parameters field to be a map")
				}

				// Check that type is uppercase
				if params["type"] != "OBJECT" {
					t.Errorf("Expected type='OBJECT' (uppercase), got %v", params["type"])
				}

				// Check properties
				props, ok := params["properties"].(map[string]interface{})
				if !ok {
					t.Fatal("Expected properties to be a map")
				}

				// Check command property has uppercase type
				cmdProp, ok := props["command"].(map[string]interface{})
				if !ok {
					t.Fatal("Expected command property to be a map")
				}
				if cmdProp["type"] != "ARRAY" {
					t.Errorf("Expected command type='ARRAY' (uppercase), got %v", cmdProp["type"])
				}

				// Google doesn't use tool_choice
				if req["tool_choice"] != nil {
					t.Errorf("Expected tool_choice=nil for Google, got %v", req["tool_choice"])
				}
			},
		},
		{
			name:     "Claude model should use Anthropic format",
			model:    "claude-opus-4",
			provider: "argo",
			verifyTools: func(t *testing.T, req map[string]interface{}) {
				// Tools should be in Anthropic format (with input_schema)
				toolsInterface, ok := req["tools"].([]interface{})
				if !ok {
					t.Fatalf("Tools should be []interface{}, got %T", req["tools"])
				}

				if len(toolsInterface) != 1 {
					t.Fatalf("Expected 1 tool, got %d", len(toolsInterface))
				}

				tool, ok := toolsInterface[0].(map[string]interface{})
				if !ok {
					t.Fatalf("Tool should be map[string]interface{}, got %T", toolsInterface[0])
				}

				// Check Anthropic format structure
				if tool["name"] != "universal_command" {
					t.Errorf("Expected name='universal_command', got %v", tool["name"])
				}

				// Check input_schema field exists
				schema, ok := tool["input_schema"].(map[string]interface{})
				if !ok {
					t.Fatal("Expected input_schema field to be a map")
				}

				// Check that type is lowercase (original format)
				if schema["type"] != "object" {
					t.Errorf("Expected type='object' (lowercase), got %v", schema["type"])
				}

				// Check tool_choice for Anthropic
				// After JSON marshaling, it might be map[string]interface{}
				toolChoice, ok := req["tool_choice"].(map[string]interface{})
				if !ok {
					// Try as map[string]string
					toolChoiceStr, ok2 := req["tool_choice"].(map[string]string)
					if !ok2 {
						t.Fatalf("Expected tool_choice to be map[string]interface{} or map[string]string, got %T", req["tool_choice"])
					}
					// Convert to interface map
					toolChoice = make(map[string]interface{})
					for k, v := range toolChoiceStr {
						toolChoice[k] = v
					}
				}
				if toolChoice["type"] != "auto" {
					t.Errorf("Expected tool_choice type='auto', got %v", toolChoice["type"])
				}
			},
		},
		{
			name:     "O3 model should use OpenAI format",
			model:    "o3-mini",
			provider: "argo",
			verifyTools: func(t *testing.T, req map[string]interface{}) {
				// O3 models should use OpenAI format
				toolsInterface, ok := req["tools"].([]interface{})
				if !ok {
					t.Fatalf("Tools should be []interface{}, got %T", req["tools"])
				}

				if len(toolsInterface) == 0 {
					t.Fatal("Expected at least one tool")
				}

				tool, ok := toolsInterface[0].(map[string]interface{})
				if !ok {
					t.Fatalf("Tool should be map[string]interface{}, got %T", toolsInterface[0])
				}

				if tool["type"] != "function" {
					t.Errorf("Expected type='function' for O3 model, got %v", tool["type"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test config
			cfg := &testRequestConfigWithTools{
				user:       "testuser",
				model:      tt.model,
				provider:   tt.provider,
				system:     "You are a helpful assistant",
				enableTool: true,
			}

			// Build the request
			messages := []TypedMessage{NewTextMessage("user", "Test message")}
			httpReq, body, err := buildArgoChatRequestTyped(cfg, messages, false)
			if err != nil {
				t.Fatalf("Failed to build request: %v", err)
			}

			// Verify HTTP request was created
			if httpReq == nil {
				t.Fatal("Expected HTTP request to be created")
			}

			// Parse the request body
			var req map[string]interface{}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("Failed to parse request body: %v", err)
			}

			// Verify basic fields
			if req["user"] != "testuser" {
				t.Errorf("Expected user='testuser', got %v", req["user"])
			}
			if req["model"] != tt.model {
				t.Errorf("Expected model='%s', got %v", tt.model, req["model"])
			}

			// Run specific verification for tools format
			tt.verifyTools(t, req)
		})
	}
}

func TestConvertToolsForProvider(t *testing.T) {
	// Create sample tool definition
	tools := []ToolDefinition{
		{
			Name:        "test_tool",
			Description: "A test tool",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"param1": map[string]interface{}{
						"type":        "string",
						"description": "First parameter",
					},
				},
				"required": []string{"param1"},
			},
		},
	}

	tests := []struct {
		model            string
		expectedProvider string
	}{
		{"gpt5", "openai"},
		{"gpt-4", "openai"},
		{"o1-preview", "openai"},
		{"o3-mini", "openai"},
		{"gemini25pro", "google"},
		{"gemini-1.5-flash", "google"},
		{"claude-opus-4", "anthropic"},
		{"claude-3-haiku", "anthropic"},
		{"unknown-model", "openai"}, // Should default to OpenAI
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			// Test determineArgoModelProvider
			provider := DetermineArgoModelProvider(tt.model)
			if provider != tt.expectedProvider {
				t.Errorf("Expected provider=%s for model=%s, got %s", tt.expectedProvider, tt.model, provider)
			}

			// Test convertToolsForProvider
			converted := ConvertToolsForProvider(tt.model, tools, nil)
			convertedTools := converted.Tools
			toolChoice := converted.ToolChoice

			switch tt.expectedProvider {
			case "openai":
				// Verify OpenAI format with typed structures
				toolsList, ok := convertedTools.([]OpenAITool)
				if !ok {
					t.Fatalf("Expected []OpenAITool for OpenAI, got %T", convertedTools)
				}
				if len(toolsList) == 0 || toolsList[0].Type != "function" {
					t.Error("OpenAI tools should have type='function'")
				}
				if toolsList[0].Function.Name != "test_tool" {
					t.Errorf("Expected tool name='test_tool', got %v", toolsList[0].Function.Name)
				}
				// Check tool choice is string "auto" for OpenAI
				if toolChoice != "auto" {
					t.Errorf("Expected tool_choice='auto' for OpenAI, got %v", toolChoice)
				}

			case "google":
				// Verify Google format with typed structures
				toolsList, ok := convertedTools.([]GoogleTool)
				if !ok {
					t.Fatalf("Expected []GoogleTool for Google, got %T", convertedTools)
				}
				if len(toolsList) == 0 {
					t.Fatal("Expected at least one tool")
				}
				// Check function declarations
				if len(toolsList[0].FunctionDeclarations) == 0 {
					t.Fatal("Expected at least one function declaration")
				}
				decl := toolsList[0].FunctionDeclarations[0]
				if decl.Name != "test_tool" {
					t.Errorf("Expected tool name='test_tool', got %v", decl.Name)
				}
				// Check for parameters field and uppercase types
				params := decl.Parameters
				if params == nil {
					t.Fatal("Google tools should have parameters field")
				}
				// Convert params to map to check the type field
				var paramsMap map[string]interface{}
				if err := json.Unmarshal(params, &paramsMap); err != nil {
					t.Fatalf("Failed to unmarshal parameters: %v", err)
				}
				if paramsMap["type"] != "OBJECT" {
					t.Errorf("Expected type='OBJECT' (uppercase) for Google, got %v", paramsMap["type"])
				}
				if toolChoice != nil {
					t.Errorf("Expected tool_choice=nil for Google, got %v", toolChoice)
				}

			case "anthropic":
				// Verify Anthropic format with typed structures
				toolsList, ok := convertedTools.([]AnthropicTool)
				if !ok {
					t.Fatalf("Expected []AnthropicTool for Anthropic, got %T", convertedTools)
				}
				if len(toolsList) == 0 {
					t.Fatal("Expected at least one tool")
				}
				if toolsList[0].Name != "test_tool" {
					t.Errorf("Expected tool name='test_tool', got %v", toolsList[0].Name)
				}
				// Check for input_schema field
				if toolsList[0].InputSchema == nil {
					t.Error("Anthropic tools should have input_schema field")
				}
				toolChoiceMap, ok := toolChoice.(AnthropicToolChoice)
				if !ok || toolChoiceMap.Type != "auto" {
					t.Errorf("Expected tool_choice={Type:'auto'} for Anthropic, got %v", toolChoice)
				}
			}
		})
	}
}

func TestBuildToolResultRequestForArgo(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		provider string
		wantErr  bool
		errMsg   string
	}{
		{
			name:     "GPT model routes to OpenAI builder",
			model:    "gpt5",
			provider: "argo",
			wantErr:  false,
		},
		{
			name:     "Claude model routes to Anthropic builder",
			model:    "claude-opus-4",
			provider: "argo",
			wantErr:  false,
		},
		{
			name:     "Gemini model via Argo now supported",
			model:    "gemini25pro",
			provider: "argo",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &testRequestConfigWithTools{
				model:    tt.model,
				provider: tt.provider,
				system:   "Test system",
			}

			// Tool definitions and system are now passed directly to request builders

			// Create typed messages for the request
			typedMessages := []TypedMessage{
				{
					Role:   "user",
					Blocks: []Block{TextBlock{Text: "Test message"}},
				},
			}

			_, _, err := BuildToolResultRequest(cfg, tt.model, "", nil, typedMessages)

			if tt.wantErr {
				if err == nil {
					t.Fatal("Expected error but got none")
				}
				if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("Expected error containing '%s', got '%v'", tt.errMsg, err)
				}
			} else {
				if err != nil {
					t.Fatalf("Unexpected error: %v", err)
				}
			}
		})
	}
}

// Helper function to check if string contains substring
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// writeFile is a helper to write content to a file
func writeFile(path string, content []byte) error {
	return os.WriteFile(path, content, constants.FilePerm)
}

func TestParseArgoResponseWithTools(t *testing.T) {
	tests := []struct {
		name          string
		response      string
		isEmbed       bool
		expectedText  string
		expectedTools int
		expectedErr   bool
	}{
		{
			name:          "Simple string response",
			response:      `{"response": "This is a simple text response"}`,
			isEmbed:       false,
			expectedText:  "This is a simple text response",
			expectedTools: 0,
			expectedErr:   false,
		},
		{
			name: "Response with tool calls",
			response: `{
				"response": {
					"content": "I'll list the files in the current directory for you.",
					"tool_calls": [
						{
							"id": "toolu_vrtx_01PcYjSNXjozVW3xg83JUBCo",
							"input": {"command": ["ls", "-la"]},
							"name": "universal_command",
							"type": "tool_use"
						}
					]
				}
			}`,
			isEmbed:       false,
			expectedText:  "I'll list the files in the current directory for you.",
			expectedTools: 1,
			expectedErr:   false,
		},
		{
			name: "Response with multiple tool calls",
			response: `{
				"response": {
					"content": "I'll check both directories.",
					"tool_calls": [
						{
							"id": "tool1",
							"input": {"command": ["ls", "/tmp"]},
							"name": "universal_command"
						},
						{
							"id": "tool2",
							"input": {"command": ["ls", "/home"]},
							"name": "universal_command"
						}
					]
				}
			}`,
			isEmbed:       false,
			expectedText:  "I'll check both directories.",
			expectedTools: 2,
			expectedErr:   false,
		},
		{
			name: "Response with tool call using args field",
			response: `{
				"response": {
					"content": "Checking the weather.",
					"tool_calls": [
						{
							"id": "weather_tool",
							"args": {"location": "San Francisco"},
							"name": "get_weather"
						}
					]
				}
			}`,
			isEmbed:       false,
			expectedText:  "Checking the weather.",
			expectedTools: 1,
			expectedErr:   false,
		},
		{
			name:          "Empty response object",
			response:      `{"response": {}}`,
			isEmbed:       false,
			expectedText:  "",
			expectedTools: 0,
			expectedErr:   false,
		},
		{
			name:          "Embed mode with string response",
			response:      `{"embedding": [[0.1, 0.2, 0.3]]}`,
			isEmbed:       true,
			expectedText:  "[0.1,0.2,0.3]",
			expectedTools: 0,
			expectedErr:   false,
		},
		{
			name:          "Invalid JSON",
			response:      `{"response": }`,
			isEmbed:       false,
			expectedText:  "",
			expectedTools: 0,
			expectedErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, tools, err := parseArgoResponseWithTools([]byte(tt.response), tt.isEmbed)

			if tt.expectedErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if text != tt.expectedText {
				t.Errorf("Expected text='%s', got '%s'", tt.expectedText, text)
			}

			if len(tools) != tt.expectedTools {
				t.Errorf("Expected %d tools, got %d", tt.expectedTools, len(tools))
			}

			// Verify tool details for specific tests
			if tt.name == "Response with tool calls" && len(tools) > 0 {
				tool := tools[0]
				if tool.ID != "toolu_vrtx_01PcYjSNXjozVW3xg83JUBCo" {
					t.Errorf("Expected tool ID to be 'toolu_vrtx_01PcYjSNXjozVW3xg83JUBCo', got '%s'", tool.ID)
				}
				if tool.Name != "universal_command" {
					t.Errorf("Expected tool name to be 'universal_command', got '%s'", tool.Name)
				}

				// Verify args
				var args map[string]interface{}
				if err := json.Unmarshal(tool.Args, &args); err != nil {
					t.Fatalf("Failed to unmarshal tool args: %v", err)
				}

				if cmd, ok := args["command"].([]interface{}); !ok || len(cmd) != 2 {
					t.Error("Expected command array with 2 elements")
				}
			}
		})
	}
}

func TestBuildArgoToolResultRequest(t *testing.T) {
	// Create a sample tools file content
	toolsJSON := `[
		{
			"name": "get_weather",
			"description": "Get the current weather in a location",
			"input_schema": {
				"type": "object",
				"properties": {
					"location": {
						"type": "string",
						"description": "The city and state, e.g. San Francisco, CA"
					}
				},
				"required": ["location"]
			}
		}
	]`

	// Write tools to a temporary file
	toolsFile := t.TempDir() + "/tools.json"
	if err := writeFile(toolsFile, []byte(toolsJSON)); err != nil {
		t.Fatalf("Failed to write tools file: %v", err)
	}

	// Test configuration
	cfg := &testRequestConfigWithTools{
		user:       "testuser",
		model:      "claudesonnet4",
		system:     "Test system prompt",
		env:        "dev",
		enableTool: true,
	}

	// Test tool results are now embedded in typedMessages
	additionalText := "Note: Some outputs were truncated"

	tests := []struct {
		name          string
		model         string
		expectedError bool
		errorContains string
		validateBody  func(t *testing.T, body []byte)
	}{
		{
			name:  "Claude model formats tool results correctly",
			model: "claudesonnet4",
			validateBody: func(t *testing.T, body []byte) {
				var req map[string]interface{}
				err := json.Unmarshal(body, &req)
				if err != nil {
					t.Fatalf("Failed to unmarshal request: %v", err)
				}

				// Check basic request structure
				if req["user"] != "testuser" {
					t.Errorf("Expected user='testuser', got '%v'", req["user"])
				}
				if req["model"] != "claudesonnet4" {
					t.Errorf("Expected model='claudesonnet4', got '%v'", req["model"])
				}

				messages, ok := req["messages"].([]interface{})
				if !ok {
					t.Fatal("Expected messages to be an array")
				}

				// Debug: print all messages to understand what we're getting
				t.Logf("Total messages: %d", len(messages))
				for i, msg := range messages {
					if m, ok := msg.(map[string]interface{}); ok {
						t.Logf("Message %d: role=%v, has content=%v, has tool_use=%v",
							i, m["role"], m["content"] != nil, false)
						// Check if it has tool_use blocks
						if content, ok := m["content"].([]interface{}); ok {
							for _, block := range content {
								if b, ok := block.(map[string]interface{}); ok {
									if b["type"] == "tool_use" {
										t.Logf("  - Has tool_use block with id=%v", b["id"])
									}
								}
							}
						}
						// Check if it has tool_calls (OpenAI format)
						if toolCalls, ok := m["tool_calls"].([]interface{}); ok {
							t.Logf("  - Has %d tool_calls", len(toolCalls))
						}
					}
				}

				// Should have: system, user, assistant (with tool_use), user (tool results)
				if len(messages) != 4 {
					t.Errorf("Expected 4 messages, got %d", len(messages))
				}

				// Check system message
				if msg0, ok := messages[0].(map[string]interface{}); ok {
					if msg0["role"] != "system" {
						t.Errorf("Expected first message role='system', got '%v'", msg0["role"])
					}
					if msg0["content"] != "Test system prompt" {
						t.Errorf("Expected system content='Test system prompt', got '%v'", msg0["content"])
					}
				}

				// Check original user message
				if msg1, ok := messages[1].(map[string]interface{}); ok {
					if msg1["role"] != "user" {
						t.Errorf("Expected second message role='user', got '%v'", msg1["role"])
					}
				}

				// Check assistant message with tool_use blocks
				if msg2, ok := messages[2].(map[string]interface{}); ok {
					if msg2["role"] != "assistant" {
						t.Errorf("Expected third message role='assistant', got '%v'", msg2["role"])
					}
					// Should have content array with tool_use blocks
					if content, ok := msg2["content"].([]interface{}); ok {
						if len(content) < 2 {
							t.Errorf("Expected at least 2 content blocks in assistant message, got %d", len(content))
						}
					}
				}

				// Check user message with tool results
				if msg3, ok := messages[3].(map[string]interface{}); ok {
					if msg3["role"] != "user" {
						t.Errorf("Expected fourth message role='user', got '%v'", msg3["role"])
					}
					// Content should be an array of tool_result blocks
					if content, ok := msg3["content"].([]interface{}); ok {
						// Should have at least 2 tool results
						if len(content) < 2 {
							t.Errorf("Expected at least 2 tool result blocks, got %d", len(content))
						}
						for _, block := range content {
							if b, ok := block.(map[string]interface{}); ok {
								if b["type"] == "tool_result" {
									// Check tool_use_id is present
									if id, ok := b["tool_use_id"].(string); !ok || (id != "tool_123" && id != "tool_456") {
										t.Errorf("Expected valid tool_use_id, got '%v'", b["tool_use_id"])
									}
								}
							}
						}
					}
				}

				// Check tools are included for follow-up
				if req["tools"] == nil {
					t.Error("Expected tools to be non-nil")
				}
				if req["tool_choice"] == nil {
					t.Error("Expected tool_choice to be non-nil")
				}
			},
		},
		{
			name:  "GPT model formats tool results correctly",
			model: "gpt5",
			validateBody: func(t *testing.T, body []byte) {
				var req map[string]interface{}
				err := json.Unmarshal(body, &req)
				if err != nil {
					t.Fatalf("Failed to unmarshal request: %v", err)
				}

				// Check basic request structure
				if req["user"] != "testuser" {
					t.Errorf("Expected user='testuser', got '%v'", req["user"])
				}
				if req["model"] != "gpt5" {
					t.Errorf("Expected model='gpt5', got '%v'", req["model"])
				}

				messages, ok := req["messages"].([]interface{})
				if !ok {
					t.Fatal("Expected messages to be an array")
				}

				// Debug: print all messages to understand what we're getting
				t.Logf("Total messages: %d", len(messages))
				for i, msg := range messages {
					if m, ok := msg.(map[string]interface{}); ok {
						t.Logf("Message %d: role=%v, has content=%v, has tool_use=%v",
							i, m["role"], m["content"] != nil, false)
						// Check if it has tool_use blocks
						if content, ok := m["content"].([]interface{}); ok {
							for _, block := range content {
								if b, ok := block.(map[string]interface{}); ok {
									if b["type"] == "tool_use" {
										t.Logf("  - Has tool_use block with id=%v", b["id"])
									}
								}
							}
						}
						// Check if it has tool_calls (OpenAI format)
						if toolCalls, ok := m["tool_calls"].([]interface{}); ok {
							t.Logf("  - Has %d tool_calls", len(toolCalls))
						}
					}
				}

				// Should have: system, user, assistant (with tool_calls), tool result 1, tool result 2, user (additional text)
				if len(messages) != 6 {
					t.Errorf("Expected 6 messages, got %d", len(messages))
				}

				// Check system message
				if msg0, ok := messages[0].(map[string]interface{}); ok {
					if msg0["role"] != "system" {
						t.Errorf("Expected first message role='system', got '%v'", msg0["role"])
					}
					if msg0["content"] != "Test system prompt" {
						t.Errorf("Expected system content='Test system prompt', got '%v'", msg0["content"])
					}
				}

				// Check original user message
				if msg1, ok := messages[1].(map[string]interface{}); ok {
					if msg1["role"] != "user" {
						t.Errorf("Expected second message role='user', got '%v'", msg1["role"])
					}
				}

				// Check assistant message with tool_calls
				if msg2, ok := messages[2].(map[string]interface{}); ok {
					if msg2["role"] != "assistant" {
						t.Errorf("Expected third message role='assistant', got '%v'", msg2["role"])
					}
					// Should have tool_calls array
					if toolCalls, ok := msg2["tool_calls"].([]interface{}); ok {
						if len(toolCalls) != 2 {
							t.Errorf("Expected 2 tool calls, got %d", len(toolCalls))
						}
					}
				}

				// Check tool result messages (should have role="tool")
				if msg3, ok := messages[3].(map[string]interface{}); ok {
					if msg3["role"] != "tool" {
						t.Errorf("Expected fourth message role='tool', got '%v'", msg3["role"])
					}
					if msg3["tool_call_id"] != "tool_123" {
						t.Errorf("Expected tool_call_id='tool_123', got '%v'", msg3["tool_call_id"])
					}
					if content, ok := msg3["content"].(string); ok {
						if !strings.Contains(content, "file1.txt") {
							t.Error("Expected content to contain 'file1.txt'")
						}
					}
				}

				if msg4, ok := messages[4].(map[string]interface{}); ok {
					if msg4["role"] != "tool" {
						t.Errorf("Expected fifth message role='tool', got '%v'", msg4["role"])
					}
					if msg4["tool_call_id"] != "tool_456" {
						t.Errorf("Expected tool_call_id='tool_456', got '%v'", msg4["tool_call_id"])
					}
					if content, ok := msg4["content"].(string); ok {
						if !strings.Contains(content, "Error:") {
							t.Error("Expected content to contain 'Error:'")
						}
					}
				}

				// Check additional text message
				if msg5, ok := messages[5].(map[string]interface{}); ok {
					if msg5["role"] != "user" {
						t.Errorf("Expected sixth message role='user', got '%v'", msg5["role"])
					}
					if msg5["content"] != additionalText {
						t.Errorf("Expected content='%s', got '%v'", additionalText, msg5["content"])
					}
				}

				// Check tools are included for follow-up
				if req["tools"] == nil {
					t.Error("Expected tools to be non-nil")
				}
				if req["tool_choice"] == nil {
					t.Error("Expected tool_choice to be non-nil")
				}
			},
		},
		{
			name:          "Gemini model via Argo now supported",
			model:         "gemini25pro",
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Update model in config
			testCfg := &testRequestConfigWithTools{
				user:       cfg.user,
				model:      tt.model,
				system:     cfg.system,
				env:        cfg.env,
				enableTool: true,
				provider:   "argo",
			}

			// Set up request cache for the test
			// originalMessages are no longer needed with the simplified ToolConversation

			// Tool definitions are now passed directly to request builders
			// System and stream settings come from cfg

			// Simulate what handleToolExecution does: build typed messages that would be accumulated
			// Format depends on the model type (Anthropic vs OpenAI format)
			var typedMessages []TypedMessage

			// Start with system message and initial user message
			typedMessages = append(typedMessages, NewTextMessage("system", cfg.system))
			typedMessages = append(typedMessages, NewTextMessage("user", "Initial request"))

			if strings.HasPrefix(strings.ToLower(tt.model), "gpt") || strings.HasPrefix(strings.ToLower(tt.model), "o3") || strings.HasPrefix(strings.ToLower(tt.model), "o1") {
				// OpenAI format for GPT/O models
				// Assistant message with tool calls
				assistantBlocks := []Block{
					TextBlock{Text: "I'll list the files in the current directory for you."},
					ToolUseBlock{
						ID:    "tool_123",
						Name:  "universal_command",
						Input: json.RawMessage(`{"command": "ls"}`),
					},
					ToolUseBlock{
						ID:    "tool_456",
						Name:  "universal_command",
						Input: json.RawMessage(`{"command": "cat nonexistent.txt"}`),
					},
				}
				// Note: For OpenAI format, we need to include the tool calls in the assistant message
				// The actual tool calls will be added by the build function based on the results
				typedMessages = append(typedMessages, TypedMessage{
					Role:   "assistant",
					Blocks: assistantBlocks,
				})

				// Tool results should be in a user message with ToolResultBlocks
				// The ToOpenAI converter will split these into separate tool messages
				toolResultBlocks := []Block{
					ToolResultBlock{
						ToolUseID: "tool_123",
						Content:   "file1.txt\nfile2.txt",
					},
					ToolResultBlock{
						ToolUseID: "tool_456",
						Content:   "Error: File not found\n[output truncated]",
						IsError:   true,
					},
				}

				if additionalText != "" {
					toolResultBlocks = append(toolResultBlocks, TextBlock{Text: additionalText})
				}

				typedMessages = append(typedMessages, TypedMessage{
					Role:   "user",
					Blocks: toolResultBlocks,
				})
			} else {
				// Anthropic format for Claude models
				assistantBlocks := []Block{
					TextBlock{Text: "I'll list the files in the current directory for you."},
					ToolUseBlock{
						ID:    "tool_123",
						Name:  "universal_command",
						Input: json.RawMessage(`{"command": "ls"}`),
					},
					ToolUseBlock{
						ID:    "tool_456",
						Name:  "universal_command",
						Input: json.RawMessage(`{"command": "cat nonexistent.txt"}`),
					},
				}
				typedMessages = append(typedMessages, TypedMessage{
					Role:   "assistant",
					Blocks: assistantBlocks,
				})

				// Tool results in a single user message
				toolResultBlocks := []Block{
					ToolResultBlock{
						ToolUseID: "tool_123",
						Content:   "file1.txt\nfile2.txt",
					},
					ToolResultBlock{
						ToolUseID: "tool_456",
						Content:   "Error: File not found\n[output truncated]",
						IsError:   true,
					},
				}
				if additionalText != "" {
					toolResultBlocks = append(toolResultBlocks, TextBlock{Text: additionalText})
				}
				typedMessages = append(typedMessages, TypedMessage{
					Role:   "user",
					Blocks: toolResultBlocks,
				})
			}
			req, body, err := BuildToolResultRequest(testCfg, tt.model, "", nil, typedMessages)

			if tt.expectedError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("Expected error to contain '%s', got '%v'", tt.errorContains, err)
				}
				if req != nil {
					t.Error("Expected req to be nil on error")
				}
				if body != nil {
					t.Error("Expected body to be nil on error")
				}
			} else {
				if err != nil {
					t.Fatalf("Unexpected error: %v", err)
				}
				if req == nil {
					t.Fatal("Expected req to be non-nil")
				}
				if body == nil {
					t.Fatal("Expected body to be non-nil")
				}

				// Check HTTP request properties
				if req.Method != "POST" {
					t.Errorf("Expected method='POST', got '%s'", req.Method)
				}
				if !strings.Contains(req.URL.String(), "https://apps-dev.inside.anl.gov/argoapi/api/v1/resource/chat/") {
					t.Errorf("Expected URL to contain Argo chat endpoint, got '%s'", req.URL.String())
				}

				// Validate body content
				if tt.validateBody != nil {
					tt.validateBody(t, body)
				}
			}
		})
	}
}

func TestBuildArgoToolResultRequestWithoutTools(t *testing.T) {
	// Test configuration without tools file
	cfg := &testRequestConfigWithTools{
		user:       "testuser",
		model:      "claudesonnet4",
		system:     "Test system prompt",
		env:        "dev",
		enableTool: false, // No tools enabled
	}

	// Create typed messages for the test
	typedMessages := []TypedMessage{
		NewTextMessage("system", cfg.system),
		NewTextMessage("user", "Test message"),
	}

	req, body, err := BuildToolResultRequest(cfg, cfg.model, cfg.system, nil, typedMessages)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if req == nil {
		t.Fatal("Expected req to be non-nil")
	}
	if body == nil {
		t.Fatal("Expected body to be non-nil")
	}

	// Parse the request body
	var parsedReq map[string]interface{}
	err = json.Unmarshal(body, &parsedReq)
	if err != nil {
		t.Fatalf("Failed to unmarshal request: %v", err)
	}

	// Should not include tools when no tools file is specified
	if parsedReq["tools"] != nil {
		t.Error("Expected tools to be nil when no tools file specified")
	}
	if parsedReq["tool_choice"] != nil {
		t.Error("Expected tool_choice to be nil when no tools file specified")
	}
}
