package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
)

// Helper function to extract JSON body from an HTTP request
func extractJSONBody(req *http.Request) (map[string]interface{}, error) {
	if req == nil || req.Body == nil {
		return nil, fmt.Errorf("request or body is nil")
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %w", err)
	}

	// Reset body for potential reuse
	req.Body = io.NopCloser(bytes.NewReader(body))

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	return result, nil
}

// Helper to create test config with API key
func createTestConfig(provider, model string, enableTool bool) (*TestRequestConfig, string, error) {
	// Create a temporary API key file
	apiKeyFile, err := os.CreateTemp("", "test-api-key-*.txt")
	if err != nil {
		return nil, "", err
	}
	if _, err := apiKeyFile.WriteString("test-api-key"); err != nil {
		os.Remove(apiKeyFile.Name())
		return nil, "", err
	}
	apiKeyFile.Close()

	cfg := &TestRequestConfig{
		User:              "testuser",
		Model:             model,
		System:            "Test system prompt",
		APIKeyFile:        apiKeyFile.Name(),
		Provider:          provider,
		IsToolEnabledFlag: enableTool,
	}

	return cfg, apiKeyFile.Name(), nil
}

// Test tool definitions for consistent testing
func createTestTools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "get_weather",
			Description: "Get weather for a location",
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
		},
	}
}

func buildProviderTestRequest(cfg ChatRequestConfig, messages []TypedMessage, model string, system string, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
	return buildChatRequestFromTyped(cfg, messages, model, system, toolDefs, toolChoice, stream)
}

func buildGoogleTestRequest(cfg ChatRequestConfig, messages []TypedMessage, model string, toolDefs []ToolDefinition, toolChoice *ToolChoice, stream bool) (*http.Request, []byte, error) {
	return buildChatRequestFromTyped(cfg, messages, model, configuredSystemPrompt(cfg), toolDefs, toolChoice, stream)
}

// ============================================================================
// OpenAI JSON Format Tests
// ============================================================================

func TestOpenAIRequestJSON_ToolChoiceString(t *testing.T) {
	cfg, apiKeyFile, err := createTestConfig("openai", "gpt-4", true)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}
	defer os.Remove(apiKeyFile)

	messages := []TypedMessage{
		NewTextMessage("user", "What's the weather?"),
	}
	tools := createTestTools()

	// Build request with tools (should default to "auto")
	req, _, err := buildProviderTestRequest(cfg, messages, "gpt-4", "", tools, nil, false)
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	// Extract and verify JSON
	jsonData, err := extractJSONBody(req)
	if err != nil {
		t.Fatalf("Failed to extract JSON: %v", err)
	}

	// Verify tool_choice is a string "auto", not an object
	toolChoice, exists := jsonData["tool_choice"]
	if !exists {
		t.Error("tool_choice field missing")
	}

	if toolChoiceStr, ok := toolChoice.(string); !ok {
		t.Errorf("tool_choice should be string, got %T: %+v", toolChoice, toolChoice)
	} else if toolChoiceStr != "auto" {
		t.Errorf("tool_choice should be 'auto', got '%s'", toolChoiceStr)
	}

	// Verify tools array exists and has correct structure
	if tools, ok := jsonData["tools"].([]interface{}); !ok {
		t.Errorf("tools should be array, got %T", jsonData["tools"])
	} else if len(tools) != 1 {
		t.Errorf("Expected 1 tool, got %d", len(tools))
	} else {
		tool := tools[0].(map[string]interface{})
		if tool["type"] != "function" {
			t.Errorf("Tool type should be 'function', got %v", tool["type"])
		}
		if function, ok := tool["function"].(map[string]interface{}); !ok {
			t.Error("Tool should have function field")
		} else {
			if function["name"] != "get_weather" {
				t.Errorf("Function name should be 'get_weather', got %v", function["name"])
			}
		}
	}
}

func TestOpenAIRequestJSON_ToolChoiceFunction(t *testing.T) {
	_, apiKeyFile, err := createTestConfig("openai", "gpt-4", true)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}
	defer os.Remove(apiKeyFile)

	messages := []TypedMessage{
		NewTextMessage("user", "What's the weather?"),
	}
	tools := createTestTools()

	// Create specific tool choice
	toolChoice := &ToolChoice{
		Type: "tool",
		Name: "get_weather",
	}

	// Build request with specific function selection
	// We need to manually build the request to pass toolChoice
	typedOpenAIMessages := ToOpenAITyped(messages)
	openAIMessages := MarshalOpenAIMessagesForRequest(typedOpenAIMessages)

	reqMap := map[string]interface{}{
		"model":    "gpt-4",
		"messages": openAIMessages,
		"stream":   false,
	}

	// Add tools with specific tool choice
	converted := ConvertToolsForProvider("gpt-4", tools, toolChoice)
	reqMap["tools"] = converted.Tools
	reqMap["tool_choice"] = converted.ToolChoice

	body, err := json.Marshal(reqMap)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	// Parse JSON to verify structure
	var jsonData map[string]interface{}
	if err := json.Unmarshal(body, &jsonData); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	// Verify tool_choice is an object with correct structure
	toolChoiceObj, ok := jsonData["tool_choice"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool_choice should be object for specific function, got %T: %+v",
			jsonData["tool_choice"], jsonData["tool_choice"])
	}

	if toolChoiceObj["type"] != "function" {
		t.Errorf("tool_choice.type should be 'function', got %v", toolChoiceObj["type"])
	}

	functionObj, ok := toolChoiceObj["function"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool_choice.function should exist and be an object, got %T", toolChoiceObj["function"])
	}

	if functionObj["name"] != "get_weather" {
		t.Errorf("tool_choice.function.name should be 'get_weather', got %v", functionObj["name"])
	}
}

func TestOpenAIRequestJSON_ToolChoiceFunctionBuilder(t *testing.T) {
	cfg, apiKeyFile, err := createTestConfig("openai", "gpt-4", true)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}
	defer os.Remove(apiKeyFile)

	messages := []TypedMessage{
		NewTextMessage("user", "What's the weather?"),
	}
	tools := createTestTools()
	toolChoice := &ToolChoice{
		Type: "tool",
		Name: "get_weather",
	}

	req, _, err := buildProviderTestRequest(cfg, messages, "gpt-4", "", tools, toolChoice, false)
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	jsonData, err := extractJSONBody(req)
	if err != nil {
		t.Fatalf("Failed to extract JSON: %v", err)
	}

	toolChoiceObj, ok := jsonData["tool_choice"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool_choice should be object for specific function, got %T: %+v",
			jsonData["tool_choice"], jsonData["tool_choice"])
	}
	if toolChoiceObj["type"] != "function" {
		t.Errorf("tool_choice.type should be 'function', got %v", toolChoiceObj["type"])
	}
	functionObj, ok := toolChoiceObj["function"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool_choice.function should exist and be an object, got %T", toolChoiceObj["function"])
	}
	if functionObj["name"] != "get_weather" {
		t.Errorf("tool_choice.function.name should be 'get_weather', got %v", functionObj["name"])
	}
}

func TestOpenAIRequestJSON_NoTools(t *testing.T) {
	cfg, apiKeyFile, err := createTestConfig("openai", "gpt-4", false)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}
	defer os.Remove(apiKeyFile)

	messages := []TypedMessage{
		NewTextMessage("user", "Hello"),
	}

	// Build request without tools
	req, _, err := buildProviderTestRequest(cfg, messages, "gpt-4", "", nil, nil, false)
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	// Extract and verify JSON
	jsonData, err := extractJSONBody(req)
	if err != nil {
		t.Fatalf("Failed to extract JSON: %v", err)
	}

	// Verify no tools or tool_choice fields
	if _, exists := jsonData["tools"]; exists {
		t.Error("tools field should not exist when no tools provided")
	}
	if _, exists := jsonData["tool_choice"]; exists {
		t.Error("tool_choice field should not exist when no tools provided")
	}
}

func TestOpenAIRequestJSON_MultimodalContent(t *testing.T) {
	cfg, apiKeyFile, err := createTestConfig("openai", "gpt-4-vision", false)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}
	defer os.Remove(apiKeyFile)

	// Create message with mixed content
	messages := []TypedMessage{
		{
			Role: "user",
			Blocks: []Block{
				TextBlock{Text: "What's in this image?"},
				ImageBlock{URL: "https://example.com/image.jpg", Detail: "high"},
			},
		},
	}

	req, _, err := buildProviderTestRequest(cfg, messages, "gpt-4-vision", "", nil, nil, false)
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	jsonData, err := extractJSONBody(req)
	if err != nil {
		t.Fatalf("Failed to extract JSON: %v", err)
	}

	// Verify message content is an array for multimodal
	msgs := jsonData["messages"].([]interface{})
	msg := msgs[0].(map[string]interface{})
	content, ok := msg["content"].([]interface{})
	if !ok {
		t.Fatalf("Multimodal content should be array, got %T", msg["content"])
	}

	if len(content) != 2 {
		t.Errorf("Expected 2 content items, got %d", len(content))
	}

	// Check text block
	textBlock := content[0].(map[string]interface{})
	if textBlock["type"] != "text" {
		t.Errorf("First block should be text, got %v", textBlock["type"])
	}

	// Check image block
	imageBlock := content[1].(map[string]interface{})
	if imageBlock["type"] != "image_url" {
		t.Errorf("Second block should be image_url, got %v", imageBlock["type"])
	}
	if imageUrl, ok := imageBlock["image_url"].(map[string]interface{}); !ok {
		t.Error("image_url should have nested object")
	} else {
		if imageUrl["url"] != "https://example.com/image.jpg" {
			t.Errorf("Image URL mismatch: %v", imageUrl["url"])
		}
		if imageUrl["detail"] != "high" {
			t.Errorf("Image detail should be 'high', got %v", imageUrl["detail"])
		}
	}
}

// ============================================================================
// Anthropic JSON Format Tests
// ============================================================================

func TestAnthropicRequestJSON_ToolChoice(t *testing.T) {
	cfg, apiKeyFile, err := createTestConfig("anthropic", "claude-3-opus", true)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}
	defer os.Remove(apiKeyFile)

	messages := []TypedMessage{
		NewTextMessage("user", "What's the weather?"),
	}
	tools := createTestTools()

	req, _, err := buildProviderTestRequest(cfg, messages, "claude-3-opus", "You are helpful", tools, nil, false)
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	jsonData, err := extractJSONBody(req)
	if err != nil {
		t.Fatalf("Failed to extract JSON: %v", err)
	}

	// Verify tool_choice is an object with type field
	toolChoice, ok := jsonData["tool_choice"].(map[string]interface{})
	if !ok {
		t.Fatalf("Anthropic tool_choice should be object, got %T", jsonData["tool_choice"])
	}

	if toolChoice["type"] != "auto" {
		t.Errorf("tool_choice.type should be 'auto', got %v", toolChoice["type"])
	}

	// Verify tools format
	if tools, ok := jsonData["tools"].([]interface{}); !ok {
		t.Error("tools should be array")
	} else {
		tool := tools[0].(map[string]interface{})
		if tool["name"] != "get_weather" {
			t.Errorf("Tool name should be 'get_weather', got %v", tool["name"])
		}
		if _, hasInputSchema := tool["input_schema"]; !hasInputSchema {
			t.Error("Anthropic tool should have input_schema field")
		}
	}
}

func TestAnthropicRequestJSON_SystemMessage(t *testing.T) {
	cfg, apiKeyFile, err := createTestConfig("anthropic", "claude-3-opus", false)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}
	defer os.Remove(apiKeyFile)

	messages := []TypedMessage{
		NewTextMessage("user", "Hello"),
	}

	req, _, err := buildProviderTestRequest(cfg, messages, "claude-3-opus", "You are a helpful assistant", nil, nil, false)
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	jsonData, err := extractJSONBody(req)
	if err != nil {
		t.Fatalf("Failed to extract JSON: %v", err)
	}

	// Verify system is a top-level field, not in messages
	if system, ok := jsonData["system"].(string); !ok {
		t.Errorf("system should be top-level string field, got %T", jsonData["system"])
	} else if system != "You are a helpful assistant" {
		t.Errorf("system content mismatch: %s", system)
	}

	// Verify system is NOT in messages array
	msgs := jsonData["messages"].([]interface{})
	for _, msg := range msgs {
		msgMap := msg.(map[string]interface{})
		if msgMap["role"] == "system" {
			t.Error("System message should not be in messages array for Anthropic")
		}
	}
}

func TestAnthropicRequestJSON_InlineSystemMessage(t *testing.T) {
	cfg, apiKeyFile, err := createTestConfig("anthropic", "claude-3-opus", false)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}
	defer os.Remove(apiKeyFile)

	messages := []TypedMessage{
		NewTextMessage("system", "You are a helpful assistant"),
		NewTextMessage("user", "Hello"),
	}

	req, _, err := buildProviderTestRequest(cfg, messages, "claude-3-opus", "", nil, nil, false)
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	jsonData, err := extractJSONBody(req)
	if err != nil {
		t.Fatalf("Failed to extract JSON: %v", err)
	}

	if system, ok := jsonData["system"].(string); !ok {
		t.Errorf("system should be top-level string field, got %T", jsonData["system"])
	} else if system != "You are a helpful assistant" {
		t.Errorf("system content mismatch: %s", system)
	}

	msgs := jsonData["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Fatalf("Expected 1 non-system message, got %d", len(msgs))
	}
	if role := msgs[0].(map[string]interface{})["role"]; role == "system" {
		t.Error("System message should not remain in messages array for Anthropic")
	}
}

func TestAnthropicRequestJSON_ContentBlocks(t *testing.T) {
	cfg, apiKeyFile, err := createTestConfig("anthropic", "claude-3-opus", false)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}
	defer os.Remove(apiKeyFile)

	// Create message with multiple content blocks
	messages := []TypedMessage{
		{
			Role: "user",
			Blocks: []Block{
				TextBlock{Text: "Analyze this:"},
				ImageBlock{URL: "https://example.com/image.jpg"},
				FileBlock{FileID: "file-123"},
			},
		},
	}

	req, _, err := buildProviderTestRequest(cfg, messages, "claude-3-opus", "", nil, nil, false)
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	jsonData, err := extractJSONBody(req)
	if err != nil {
		t.Fatalf("Failed to extract JSON: %v", err)
	}

	msgs := jsonData["messages"].([]interface{})
	msg := msgs[0].(map[string]interface{})

	// For Anthropic, content should be an array when there are multiple blocks
	content, ok := msg["content"].([]interface{})
	if !ok {
		t.Fatalf("Content should be array for multiple blocks, got %T", msg["content"])
	}

	if len(content) != 3 {
		t.Errorf("Expected 3 content blocks, got %d", len(content))
	}

	// Verify each block type
	textBlock := content[0].(map[string]interface{})
	if textBlock["type"] != "text" {
		t.Errorf("First block should be text, got %v", textBlock["type"])
	}

	imageBlock := content[1].(map[string]interface{})
	if imageBlock["type"] != "image" {
		t.Errorf("Second block should be image, got %v", imageBlock["type"])
	}

	fileBlock := content[2].(map[string]interface{})
	if fileBlock["type"] != "file" {
		t.Errorf("Third block should be file, got %v", fileBlock["type"])
	}
	if fileData, ok := fileBlock["file"].(map[string]interface{}); !ok {
		t.Error("File block should have file field")
	} else if fileData["file_id"] != "file-123" {
		t.Errorf("File ID should be 'file-123', got %v", fileData["file_id"])
	}
}

// ============================================================================
// Google JSON Format Tests
// ============================================================================

func TestGoogleRequestJSON_NoToolChoice(t *testing.T) {
	cfg, apiKeyFile, err := createTestConfig("google", "gemini-pro", true)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}
	defer os.Remove(apiKeyFile)

	messages := []TypedMessage{
		NewTextMessage("user", "What's the weather?"),
	}
	tools := createTestTools()

	req, _, err := buildGoogleTestRequest(cfg, messages, "gemini-pro", tools, nil, false)
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	jsonData, err := extractJSONBody(req)
	if err != nil {
		t.Fatalf("Failed to extract JSON: %v", err)
	}

	// Verify tool_choice does NOT exist for Google
	if _, exists := jsonData["tool_choice"]; exists {
		t.Error("tool_choice should not exist in Google requests")
	}

	// Verify tools format (functionDeclarations)
	if tools, ok := jsonData["tools"].([]interface{}); !ok {
		t.Error("tools should be array")
	} else if len(tools) > 0 {
		tool := tools[0].(map[string]interface{})
		if funcDecls, ok := tool["functionDeclarations"].([]interface{}); !ok {
			t.Error("Google tools should have functionDeclarations array")
		} else if len(funcDecls) > 0 {
			funcDecl := funcDecls[0].(map[string]interface{})
			if funcDecl["name"] != "get_weather" {
				t.Errorf("Function name should be 'get_weather', got %v", funcDecl["name"])
			}
		}
	}
}

func TestGoogleRequestJSON_PartsStructure(t *testing.T) {
	cfg, apiKeyFile, err := createTestConfig("google", "gemini-pro", false)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}
	defer os.Remove(apiKeyFile)

	// Create messages with text
	messages := []TypedMessage{
		NewTextMessage("user", "Hello, how are you?"),
		NewTextMessage("assistant", "I'm doing well, thank you!"),
	}

	req, _, err := buildGoogleTestRequest(cfg, messages, "gemini-pro", nil, nil, false)
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	jsonData, err := extractJSONBody(req)
	if err != nil {
		t.Fatalf("Failed to extract JSON: %v", err)
	}

	// Google uses "contents" not "messages"
	contents, ok := jsonData["contents"].([]interface{})
	if !ok {
		t.Fatalf("Google should have 'contents' array, got %T", jsonData["contents"])
	}

	// Check first message
	firstMsg := contents[0].(map[string]interface{})
	if firstMsg["role"] != "user" {
		t.Errorf("First message role should be 'user', got %v", firstMsg["role"])
	}

	// Check parts structure
	parts, ok := firstMsg["parts"].([]interface{})
	if !ok {
		t.Fatalf("Message should have 'parts' array, got %T", firstMsg["parts"])
	}

	if len(parts) != 1 {
		t.Errorf("Expected 1 part, got %d", len(parts))
	}

	part := parts[0].(map[string]interface{})
	if part["text"] != "Hello, how are you?" {
		t.Errorf("Part text mismatch: %v", part["text"])
	}

	// Check assistant message (should be "model" role for Google)
	secondMsg := contents[1].(map[string]interface{})
	if secondMsg["role"] != "model" {
		t.Errorf("Assistant messages should have role='model' for Google, got %v", secondMsg["role"])
	}
}

func TestGoogleRequestJSON_SystemInstruction(t *testing.T) {
	cfg, apiKeyFile, err := createTestConfig("google", "gemini-pro", false)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}
	defer os.Remove(apiKeyFile)

	cfg.System = "You are a helpful assistant"

	messages := []TypedMessage{
		NewTextMessage("user", "Hello"),
	}

	req, _, err := buildGoogleTestRequest(cfg, messages, "gemini-pro", nil, nil, false)
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	jsonData, err := extractJSONBody(req)
	if err != nil {
		t.Fatalf("Failed to extract JSON: %v", err)
	}

	// Verify systemInstruction field
	if sysInst, ok := jsonData["systemInstruction"].(map[string]interface{}); !ok {
		t.Errorf("systemInstruction should be object, got %T", jsonData["systemInstruction"])
	} else {
		parts, ok := sysInst["parts"].([]interface{})
		if !ok {
			t.Fatalf("systemInstruction should have parts array, got %T", sysInst["parts"])
		}
		if len(parts) != 1 {
			t.Errorf("Expected 1 part in systemInstruction, got %d", len(parts))
		}
		part := parts[0].(map[string]interface{})
		if part["text"] != "You are a helpful assistant" {
			t.Errorf("System instruction text mismatch: %v", part["text"])
		}
	}
}

func TestGoogleRequestJSON_InlineSystemInstruction(t *testing.T) {
	cfg, apiKeyFile, err := createTestConfig("google", "gemini-pro", false)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}
	defer os.Remove(apiKeyFile)
	cfg.System = ""

	messages := []TypedMessage{
		NewTextMessage("system", "You are a helpful assistant"),
		NewTextMessage("user", "Hello"),
	}

	req, _, err := buildGoogleTestRequest(cfg, messages, "gemini-pro", nil, nil, false)
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	jsonData, err := extractJSONBody(req)
	if err != nil {
		t.Fatalf("Failed to extract JSON: %v", err)
	}

	if sysInst, ok := jsonData["systemInstruction"].(map[string]interface{}); !ok {
		t.Errorf("systemInstruction should be object, got %T", jsonData["systemInstruction"])
	} else {
		parts, ok := sysInst["parts"].([]interface{})
		if !ok {
			t.Fatalf("systemInstruction should have parts array, got %T", sysInst["parts"])
		}
		part := parts[0].(map[string]interface{})
		if part["text"] != "You are a helpful assistant" {
			t.Errorf("System instruction text mismatch: %v", part["text"])
		}
	}

	contents := jsonData["contents"].([]interface{})
	if len(contents) != 1 {
		t.Fatalf("Expected 1 non-system content message, got %d", len(contents))
	}
	if role := contents[0].(map[string]interface{})["role"]; role == "system" {
		t.Error("System message should not remain inline for Google")
	}
}

func TestGoogleRequestJSON_FunctionCalls(t *testing.T) {
	cfg, apiKeyFile, err := createTestConfig("google", "gemini-pro", true)
	if err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}
	defer os.Remove(apiKeyFile)

	// Create message with tool use (function call)
	messages := []TypedMessage{
		NewTextMessage("user", "What's the weather in Paris?"),
		{
			Role: "assistant",
			Blocks: []Block{
				TextBlock{Text: "I'll check the weather for you."},
				ToolUseBlock{
					ID:    "call-123",
					Name:  "get_weather",
					Input: json.RawMessage(`{"location": "Paris"}`),
				},
			},
		},
	}

	req, _, err := buildGoogleTestRequest(cfg, messages, "gemini-pro", nil, nil, false)
	if err != nil {
		t.Fatalf("Failed to build request: %v", err)
	}

	jsonData, err := extractJSONBody(req)
	if err != nil {
		t.Fatalf("Failed to extract JSON: %v", err)
	}

	contents := jsonData["contents"].([]interface{})
	assistantMsg := contents[1].(map[string]interface{})
	parts := assistantMsg["parts"].([]interface{})

	// Should have 2 parts: text and functionCall
	if len(parts) != 2 {
		t.Errorf("Expected 2 parts (text and functionCall), got %d", len(parts))
	}

	// Check function call part
	funcCallPart := parts[1].(map[string]interface{})
	if funcCall, ok := funcCallPart["functionCall"].(map[string]interface{}); !ok {
		t.Error("Second part should have functionCall field")
	} else {
		if funcCall["name"] != "get_weather" {
			t.Errorf("Function name should be 'get_weather', got %v", funcCall["name"])
		}
		if args, ok := funcCall["args"].(map[string]interface{}); !ok {
			t.Error("functionCall should have args field")
		} else if args["location"] != "Paris" {
			t.Errorf("Location should be 'Paris', got %v", args["location"])
		}
	}
}

// ============================================================================
// Cross-Provider Validation Tests
// ============================================================================

func TestCrossProvider_ToolChoiceConsistency(t *testing.T) {
	tools := createTestTools()

	testCases := []struct {
		provider       string
		model          string
		expectedFormat string // "string", "object", or "none"
	}{
		{"openai", "gpt-4", "string"},
		{"openai", "gpt-3.5-turbo", "string"},
		{"anthropic", "claude-3-opus", "object"},
		{"anthropic", "claude-3-sonnet", "object"},
		{"google", "gemini-pro", "none"},
		{"google", "gemini-1.5-flash", "none"},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%s_%s", tc.provider, tc.model), func(t *testing.T) {
			// Use ConvertToolsForProvider to get the tool_choice
			converted := ConvertToolsForProvider(tc.model, tools, nil)

			switch tc.expectedFormat {
			case "string":
				if _, ok := converted.ToolChoice.(string); !ok {
					t.Errorf("%s/%s should have string tool_choice, got %T",
						tc.provider, tc.model, converted.ToolChoice)
				}
			case "object":
				switch choice := converted.ToolChoice.(type) {
				case AnthropicToolChoice:
					// Expected for Anthropic
					if choice.Type != "auto" {
						t.Errorf("Expected type='auto', got %s", choice.Type)
					}
				case OpenAIToolChoice:
					// Could happen for specific function selection
					if choice.Type != "function" && choice.Type != "auto" {
						t.Errorf("Unexpected OpenAIToolChoice type: %s", choice.Type)
					}
				default:
					t.Errorf("%s/%s should have typed tool_choice object, got %T",
						tc.provider, tc.model, converted.ToolChoice)
				}
			case "none":
				if converted.ToolChoice != nil {
					t.Errorf("%s/%s should have nil tool_choice, got %v",
						tc.provider, tc.model, converted.ToolChoice)
				}
			}
		})
	}
}
