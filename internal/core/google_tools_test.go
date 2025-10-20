package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestBuildGoogleToolAwareRequest tests that Google requests include tool definitions
func TestBuildGoogleToolAwareRequest(t *testing.T) {
	cfg := &TestRequestConfig{
		Provider: "google",
		Model:    "gemini-1.5-pro",
		System:   "You are a helpful assistant",
	}

	messages := []TypedMessage{
		NewTextMessage("user", "Please help me list files"),
	}

	toolDefs := GetBuiltinUniversalCommandTool()

	// Build request with tools
	req, body, err := buildGoogleToolAwareRequest(context.Background(), cfg, messages, "gemini-1.5-pro", toolDefs, nil, false)
	if err != nil {
		t.Fatalf("Failed to build Google request: %v", err)
	}

	// Verify request was created
	if req == nil {
		t.Fatal("Expected request to be created")
	}

	// Parse request body
	var requestData map[string]interface{}
	if err := json.Unmarshal(body, &requestData); err != nil {
		t.Fatalf("Failed to parse request body: %v", err)
	}

	// Verify tools are included
	tools, ok := requestData["tools"]
	if !ok {
		t.Fatal("Expected 'tools' field in request")
	}

	toolsArray, ok := tools.([]interface{})
	if !ok || len(toolsArray) == 0 {
		t.Fatal("Expected non-empty tools array")
	}

	// Verify tool structure
	firstTool := toolsArray[0].(map[string]interface{})
	funcDecls, ok := firstTool["functionDeclarations"]
	if !ok {
		t.Fatal("Expected 'functionDeclarations' in tool")
	}

	funcDeclsArray := funcDecls.([]interface{})
	if len(funcDeclsArray) == 0 {
		t.Fatal("Expected at least one function declaration")
	}

	// Verify function declaration
	funcDecl := funcDeclsArray[0].(map[string]interface{})
	if funcDecl["name"] != "universal_command" {
		t.Errorf("Expected tool name 'universal_command', got %v", funcDecl["name"])
	}

	// Verify toolConfig is included
	toolConfig, ok := requestData["toolConfig"]
	if !ok {
		t.Fatal("Expected 'toolConfig' field in request")
	}

	toolConfigMap := toolConfig.(map[string]interface{})
	funcCallConfig, ok := toolConfigMap["functionCallConfig"]
	if !ok {
		t.Fatal("Expected 'functionCallConfig' in toolConfig")
	}

	funcCallConfigMap := funcCallConfig.(map[string]interface{})
	if funcCallConfigMap["mode"] != "AUTO" {
		t.Errorf("Expected mode 'AUTO', got %v", funcCallConfigMap["mode"])
	}
}

// TestGoogleToolResponseParsing tests parsing of Google tool call responses
func TestGoogleToolResponseParsing(t *testing.T) {
	// Test response with tool calls
	responseJSON := `{
		"candidates": [{
			"content": {
				"parts": [
					{
						"text": "I'll help you list the files."
					},
					{
						"functionCall": {
							"name": "universal_command",
							"args": {
								"command": ["ls", "-la"]
							}
						}
					}
				],
				"role": "model"
			},
			"finishReason": "STOP"
		}]
	}`

	response, err := parseResponse("google", []byte(responseJSON), false)
	if err != nil {
		t.Fatalf("Failed to parse Google response: %v", err)
	}

	// Verify text content
	if !strings.Contains(response.Text, "I'll help you list the files.") {
		t.Errorf("Expected text content, got: %s", response.Text)
	}

	// Verify tool calls
	if len(response.ToolCalls) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(response.ToolCalls))
	}

	toolCall := response.ToolCalls[0]
	if toolCall.Name != "universal_command" {
		t.Errorf("Expected tool name 'universal_command', got %s", toolCall.Name)
	}

	// Verify tool call arguments
	var args map[string]interface{}
	if err := json.Unmarshal(toolCall.Args, &args); err != nil {
		t.Fatalf("Failed to parse tool call args: %v", err)
	}

	command, ok := args["command"].([]interface{})
	if !ok || len(command) != 2 {
		t.Errorf("Expected command array with 2 elements, got %v", args["command"])
	}
}

// TestGoogleStreamingWithTools tests Google streaming response parsing with tool calls
func TestGoogleStreamingWithTools(t *testing.T) {
	// This test would require implementing the actual streaming parser
	// For now, we'll skip this test as the streaming implementation details
	// are internal to the handleGoogleStream function
	t.Skip("Google streaming parser is internal to handleGoogleStream")
}

// TestGoogleToolExecutionFlow tests the full flow of tool execution with Google
func TestGoogleToolExecutionFlow(t *testing.T) {
	// Test building initial request with tools
	cfg := &TestRequestConfig{
		Provider: "google",
		Model:    "gemini-1.5-pro",
		System:   "You are a helpful assistant",
	}

	// 1. Initial user message
	messages := []TypedMessage{
		NewTextMessage("user", "List the current directory"),
	}

	toolDefs := GetBuiltinUniversalCommandTool()

	// Build request
	_, body, err := buildGoogleToolAwareRequest(context.Background(), cfg, messages, "gemini-1.5-pro", toolDefs, nil, false)
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	// Verify tools are included
	var requestData map[string]interface{}
	if err := json.Unmarshal(body, &requestData); err != nil {
		t.Fatalf("Failed to unmarshal request body: %v", err)
	}
	if _, ok := requestData["tools"]; !ok {
		t.Fatal("Expected tools in request")
	}

	// 2. Simulate model response with tool call
	modelResponse := TypedMessage{
		Role: "assistant",
		Blocks: []Block{
			TextBlock{Text: "I'll list the current directory for you."},
			ToolUseBlock{
				ID:    "call_123",
				Name:  "universal_command",
				Input: json.RawMessage(`{"command":["ls","-la"]}`),
			},
		},
	}

	// 3. Add tool result
	toolResult := TypedMessage{
		Role: "user",
		Blocks: []Block{
			ToolResultBlock{
				ToolUseID: "call_123",
				Content:   "file1.txt\nfile2.go\nREADME.md",
			},
		},
	}

	// 4. Build follow-up request with tool result
	messagesWithTool := append(messages, modelResponse, toolResult)

	_, body2, err := buildGoogleToolAwareRequest(context.Background(), cfg, messagesWithTool, "gemini-1.5-pro", toolDefs, nil, false)
	if err != nil {
		t.Fatalf("Failed to build follow-up request: %v", err)
	}

	// Verify request includes tool results in proper format
	var requestData2 map[string]interface{}
	if err := json.Unmarshal(body2, &requestData2); err != nil {
		t.Fatalf("Failed to unmarshal request body: %v", err)
	}

	contents, ok := requestData2["contents"].([]interface{})
	if !ok || len(contents) != 3 {
		t.Fatalf("Expected 3 messages in contents, got %d", len(contents))
	}

	// Verify the assistant message has functionCall part
	assistantMsg := contents[1].(map[string]interface{})
	parts := assistantMsg["parts"].([]interface{})

	foundFunctionCall := false
	for _, part := range parts {
		partMap := part.(map[string]interface{})
		if _, ok := partMap["functionCall"]; ok {
			foundFunctionCall = true
			break
		}
	}

	if !foundFunctionCall {
		t.Error("Expected functionCall in assistant message")
	}

	// Verify the tool result message has functionResponse part
	toolResultMsg := contents[2].(map[string]interface{})
	parts = toolResultMsg["parts"].([]interface{})

	foundFunctionResponse := false
	for _, part := range parts {
		partMap := part.(map[string]interface{})
		if _, ok := partMap["functionResponse"]; ok {
			foundFunctionResponse = true
			break
		}
	}

	if !foundFunctionResponse {
		t.Error("Expected functionResponse in tool result message")
	}
}

// TestConvertSchemaToGoogleFormat tests the schema conversion for Google
func TestConvertSchemaToGoogleFormat(t *testing.T) {
	// Test with universal_command schema
	toolDefs := GetBuiltinUniversalCommandTool()
	if len(toolDefs) == 0 {
		t.Fatal("Expected at least one tool definition")
	}

	schema := toolDefs[0].InputSchema
	googleSchema := ConvertSchemaToGoogleFormat(schema)

	// Verify it's a map
	schemaMap, ok := googleSchema.(map[string]interface{})
	if !ok {
		t.Fatal("Expected schema to be converted to map")
	}

	// Verify type is present
	if schemaMap["type"] != "OBJECT" {
		t.Errorf("Expected type 'OBJECT', got %v", schemaMap["type"])
	}

	// Verify properties exist
	properties, ok := schemaMap["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected properties to be a map")
	}

	// Verify command property exists
	commandProp, ok := properties["command"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected command property")
	}

	// For Google, array items should be properly formatted
	if commandProp["type"] != "ARRAY" {
		t.Errorf("Expected command type 'ARRAY', got %v", commandProp["type"])
	}
}
