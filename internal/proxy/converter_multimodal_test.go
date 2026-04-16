package proxy

import (
	"context"
	"encoding/json"
	"lmtools/internal/core"
	"strings"
	"testing"
)

// TestConvertAnthropicResponseToOpenAI_WithMultimodal tests the conversion of Anthropic responses
// with multimodal content to OpenAI format
func TestConvertAnthropicResponseToOpenAI_WithMultimodal(t *testing.T) {
	mapper := &ModelMapper{config: &Config{}}
	converter := NewConverter(mapper)

	tests := []struct {
		name          string
		input         *AnthropicResponse
		originalModel string
		validateFunc  func(t *testing.T, resp *OpenAIResponse)
	}{
		{
			name: "response with missing ID should generate one",
			input: &AnthropicResponse{
				ID:   "", // Missing ID
				Type: "message",
				Role: core.RoleAssistant,
				Content: []AnthropicContentBlock{
					{
						Type: "text",
						Text: "Hello world",
					},
				},
				Model:      "claude-3-opus",
				StopReason: "end_turn",
				Usage: &AnthropicUsage{
					InputTokens:  10,
					OutputTokens: 5,
				},
			},
			originalModel: "gpt-4",
			validateFunc: func(t *testing.T, resp *OpenAIResponse) {
				// Check that ID was generated
				if resp.ID == "" || resp.ID == "chatcmpl-" {
					t.Errorf("Expected generated ID, got: %s", resp.ID)
				}
				// Check it has the correct format
				if !strings.HasPrefix(resp.ID, "chatcmpl-") {
					t.Errorf("ID should start with 'chatcmpl-', got: %s", resp.ID)
				}
				// Verify other fields
				if resp.Model != "gpt-4" {
					t.Errorf("Model = %s, want gpt-4", resp.Model)
				}
				if len(resp.Choices) != 1 {
					t.Errorf("Expected 1 choice, got %d", len(resp.Choices))
				}
				if resp.Choices[0].Message.Content != "Hello world" {
					t.Errorf("Content = %s, want 'Hello world'", resp.Choices[0].Message.Content)
				}
			},
		},
		{
			name: "response with image content blocks",
			input: &AnthropicResponse{
				ID:   "msg_123",
				Type: "message",
				Role: core.RoleAssistant,
				Content: []AnthropicContentBlock{
					{
						Type: "text",
						Text: "Here's an image:",
					},
					{
						Type: "image",
						Source: map[string]interface{}{
							"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==",
						},
					},
				},
				Model:      "claude-3-opus",
				StopReason: "end_turn",
			},
			originalModel: "gpt-4-vision",
			validateFunc: func(t *testing.T, resp *OpenAIResponse) {
				if len(resp.Choices) != 1 {
					t.Fatalf("Expected 1 choice, got %d", len(resp.Choices))
				}

				// Content should be an array with text and image_url
				content, ok := resp.Choices[0].Message.Content.([]interface{})
				if !ok {
					t.Fatalf("Expected content to be array, got %T", resp.Choices[0].Message.Content)
				}

				if len(content) != 2 {
					t.Errorf("Expected 2 content parts, got %d", len(content))
				}

				// Check first part is text
				part1, ok := content[0].(map[string]interface{})
				if !ok {
					t.Fatalf("Expected first part to be map, got %T", content[0])
				}
				if part1["type"] != "text" {
					t.Errorf("First part type = %v, want 'text'", part1["type"])
				}
				if part1["text"] != "Here's an image:" {
					t.Errorf("First part text = %v, want 'Here's an image:'", part1["text"])
				}

				// Check second part is image_url
				part2, ok := content[1].(map[string]interface{})
				if !ok {
					t.Fatalf("Expected second part to be map, got %T", content[1])
				}
				if part2["type"] != "image_url" {
					t.Errorf("Second part type = %v, want 'image_url'", part2["type"])
				}
				imageUrl, ok := part2["image_url"].(map[string]interface{})
				if !ok {
					t.Fatalf("Expected image_url to be map, got %T", part2["image_url"])
				}
				url, ok := imageUrl["url"].(string)
				if !ok || !strings.HasPrefix(url, "data:image/png;base64,") {
					t.Errorf("Image URL not properly formatted: %v", url)
				}
			},
		},
		{
			name: "response with audio content blocks",
			input: &AnthropicResponse{
				ID:   "msg_456",
				Type: "message",
				Role: core.RoleAssistant,
				Content: []AnthropicContentBlock{
					{
						Type: "text",
						Text: "Here's audio:",
					},
					{
						Type: "audio",
						InputAudio: map[string]interface{}{
							"data":   "base64_audio_data",
							"format": "wav",
						},
					},
				},
				Model:      "claude-3-opus",
				StopReason: "end_turn",
			},
			originalModel: "gpt-4-audio",
			validateFunc: func(t *testing.T, resp *OpenAIResponse) {
				if len(resp.Choices) != 1 {
					t.Fatalf("Expected 1 choice, got %d", len(resp.Choices))
				}

				content, ok := resp.Choices[0].Message.Content.([]interface{})
				if !ok {
					t.Fatalf("Expected content to be array, got %T", resp.Choices[0].Message.Content)
				}

				if len(content) != 2 {
					t.Errorf("Expected 2 content parts, got %d", len(content))
				}

				// Check second part is input_audio
				part2, ok := content[1].(map[string]interface{})
				if !ok {
					t.Fatalf("Expected second part to be map, got %T", content[1])
				}
				if part2["type"] != "input_audio" {
					t.Errorf("Second part type = %v, want 'input_audio'", part2["type"])
				}
				inputAudio, ok := part2["input_audio"].(map[string]interface{})
				if !ok {
					t.Fatalf("Expected input_audio to be map, got %T", part2["input_audio"])
				}
				if inputAudio["data"] != "base64_audio_data" {
					t.Errorf("Audio data = %v, want 'base64_audio_data'", inputAudio["data"])
				}
			},
		},
		{
			name: "response with mixed multimodal content",
			input: &AnthropicResponse{
				ID:   "msg_789",
				Type: "message",
				Role: core.RoleAssistant,
				Content: []AnthropicContentBlock{
					{
						Type: "text",
						Text: "First text",
					},
					{
						Type: "image",
						Source: map[string]interface{}{
							"type": "url",
							"url":  "https://example.com/image.png",
						},
					},
					{
						Type: "text",
						Text: "Second text",
					},
					{
						Type: "file",
						File: map[string]interface{}{
							"name": "document.pdf",
							"data": "base64_pdf_data",
						},
					},
				},
				Model:      "claude-3-opus",
				StopReason: "end_turn",
			},
			originalModel: "gpt-4-vision",
			validateFunc: func(t *testing.T, resp *OpenAIResponse) {
				content, ok := resp.Choices[0].Message.Content.([]interface{})
				if !ok {
					t.Fatalf("Expected content to be array, got %T", resp.Choices[0].Message.Content)
				}

				if len(content) != 4 {
					t.Errorf("Expected 4 content parts, got %d", len(content))
				}

				// Verify each part
				expectedTypes := []string{"text", "image_url", "text", "file"}
				for i, expectedType := range expectedTypes {
					part, ok := content[i].(map[string]interface{})
					if !ok {
						t.Fatalf("Part %d: expected map, got %T", i, content[i])
					}
					if part["type"] != expectedType {
						t.Errorf("Part %d: type = %v, want '%s'", i, part["type"], expectedType)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := converter.ConvertAnthropicResponseToOpenAI(tt.input, tt.originalModel)
			tt.validateFunc(t, got)
		})
	}
}

// TestConvertArgoToAnthropicWithRequest_IDGeneration tests that ConvertArgoToAnthropicWithRequest
// always generates an ID if one is missing
func TestConvertArgoToAnthropicWithRequest_IDGeneration(t *testing.T) {
	mapper := &ModelMapper{config: &Config{}}
	converter := NewConverter(mapper)

	tests := []struct {
		name     string
		input    *ArgoChatResponse
		request  *AnthropicRequest
		model    string
		validate func(t *testing.T, resp *AnthropicResponse)
	}{
		{
			name: "string response without ID",
			input: &ArgoChatResponse{
				Response: "This is a simple text response",
			},
			request: &AnthropicRequest{
				Messages: []AnthropicMessage{
					{Role: core.RoleUser, Content: json.RawMessage(`"Hello"`)},
				},
			},
			model: "claude-3-opus",
			validate: func(t *testing.T, resp *AnthropicResponse) {
				// Check ID was generated
				if resp.ID == "" {
					t.Error("Expected generated ID, got empty string")
				}
				if !strings.HasPrefix(resp.ID, "msg_") {
					t.Errorf("ID should start with 'msg_', got: %s", resp.ID)
				}
				// Check content
				if len(resp.Content) != 1 {
					t.Fatalf("Expected 1 content block, got %d", len(resp.Content))
				}
				if resp.Content[0].Text != "This is a simple text response" {
					t.Errorf("Content text = %s, want 'This is a simple text response'", resp.Content[0].Text)
				}
			},
		},
		{
			name: "map response without ID field",
			input: &ArgoChatResponse{
				Response: map[string]interface{}{
					"content": "Response content",
					"model":   "claude-3-opus",
				},
			},
			request: &AnthropicRequest{
				Messages: []AnthropicMessage{
					{Role: core.RoleUser, Content: json.RawMessage(`"Hello"`)},
				},
			},
			model: "claude-3-opus",
			validate: func(t *testing.T, resp *AnthropicResponse) {
				// Check ID was generated
				if resp.ID == "" {
					t.Error("Expected generated ID, got empty string")
				}
				if !strings.HasPrefix(resp.ID, "msg_") {
					t.Errorf("ID should start with 'msg_', got: %s", resp.ID)
				}
			},
		},
		{
			name: "map response with existing ID",
			input: &ArgoChatResponse{
				Response: map[string]interface{}{
					"id":      "existing_id_123",
					"content": "Response content",
					"model":   "claude-3-opus",
				},
			},
			request: &AnthropicRequest{
				Messages: []AnthropicMessage{
					{Role: core.RoleUser, Content: json.RawMessage(`"Hello"`)},
				},
			},
			model: "claude-3-opus",
			validate: func(t *testing.T, resp *AnthropicResponse) {
				// ID should still be generated since Argo responses don't have ID field
				if resp.ID == "" {
					t.Error("Expected generated ID, got empty string")
				}
				if !strings.HasPrefix(resp.ID, "msg_") {
					t.Errorf("ID should start with 'msg_', got: %s", resp.ID)
				}
			},
		},
		{
			name: "response with multimodal content",
			input: &ArgoChatResponse{
				Response: map[string]interface{}{
					"content": []interface{}{
						map[string]interface{}{
							"type": "text",
							"text": "Here's an image:",
						},
						map[string]interface{}{
							"type": "image",
							"source": map[string]interface{}{
								"type": "url",
								"url":  "https://example.com/image.png",
							},
						},
					},
				},
			},
			request: &AnthropicRequest{
				Messages: []AnthropicMessage{
					{Role: core.RoleUser, Content: json.RawMessage(`"Show me an image"`)},
				},
			},
			model: "claude-3-opus",
			validate: func(t *testing.T, resp *AnthropicResponse) {
				// Check ID was generated
				if resp.ID == "" {
					t.Error("Expected generated ID, got empty string")
				}
				// Check multimodal content
				if len(resp.Content) != 2 {
					t.Fatalf("Expected 2 content blocks, got %d", len(resp.Content))
				}
				if resp.Content[0].Type != "text" {
					t.Errorf("First block type = %s, want 'text'", resp.Content[0].Type)
				}
				if resp.Content[1].Type != "image" {
					t.Errorf("Second block type = %s, want 'image'", resp.Content[1].Type)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := converter.ConvertArgoToAnthropicWithRequest(tt.input, tt.model, tt.request)
			tt.validate(t, got)
		})
	}
}

// TestOpenAIRequestToAnthropic_ImageURL tests conversion of OpenAI image_url to Anthropic format
func TestOpenAIRequestToAnthropic_ImageURL(t *testing.T) {
	mapper := &ModelMapper{config: &Config{}}
	converter := NewConverter(mapper)

	tests := []struct {
		name     string
		input    *OpenAIRequest
		wantErr  string
		validate func(t *testing.T, resp *AnthropicRequest)
	}{
		{
			name: "message with image_url content",
			input: &OpenAIRequest{
				Model: "gpt-4-vision",
				Messages: []OpenAIMessage{
					{
						Role: core.RoleUser,
						Content: []interface{}{
							map[string]interface{}{
								"type": "text",
								"text": "What's in this image?",
							},
							map[string]interface{}{
								"type": "image_url",
								"image_url": map[string]interface{}{
									"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==",
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, resp *AnthropicRequest) {
				if len(resp.Messages) != 1 {
					t.Fatalf("Expected 1 message, got %d", len(resp.Messages))
				}

				// Parse the content
				var blocks []AnthropicContentBlock
				// Debug: log the raw content
				t.Logf("Raw content: %s", string(resp.Messages[0].Content))
				if len(resp.Messages[0].Content) == 0 {
					t.Fatal("Content is empty")
				}
				if err := json.Unmarshal(resp.Messages[0].Content, &blocks); err != nil {
					t.Fatalf("Failed to unmarshal content: %v, raw: %s", err, string(resp.Messages[0].Content))
				}

				if len(blocks) != 2 {
					t.Fatalf("Expected 2 content blocks, got %d", len(blocks))
				}

				// Check text block
				if blocks[0].Type != "text" {
					t.Errorf("First block type = %s, want 'text'", blocks[0].Type)
				}
				if blocks[0].Text != "What's in this image?" {
					t.Errorf("Text = %s, want 'What's in this image?'", blocks[0].Text)
				}

				// Check image block
				if blocks[1].Type != "image" {
					t.Errorf("Second block type = %s, want 'image'", blocks[1].Type)
				}
				if blocks[1].Source == nil {
					t.Error("Image block missing Source field")
				}
				sourceMap := blocks[1].Source
				if sourceMap["type"] != "base64" {
					t.Errorf("Source type = %v, want 'base64'", sourceMap["type"])
				}
				if sourceMap["media_type"] != "image/png" {
					t.Errorf("media_type = %v, want 'image/png'", sourceMap["media_type"])
				}
				data, ok := sourceMap["data"].(string)
				if !ok || !strings.HasPrefix(data, "iVBORw0KGgo") {
					t.Errorf("Image data not properly set: %v", data)
				}
				if _, ok := sourceMap["url"]; ok {
					t.Errorf("Expected no URL field for base64 source, got %v", sourceMap["url"])
				}
			},
		},
		{
			name: "message with HTTP image URL",
			input: &OpenAIRequest{
				Model: "gpt-4-vision",
				Messages: []OpenAIMessage{
					{
						Role: core.RoleUser,
						Content: []interface{}{
							map[string]interface{}{
								"type": "image_url",
								"image_url": map[string]interface{}{
									"url": "https://example.com/image.png",
								},
							},
						},
					},
				},
			},
			validate: func(t *testing.T, resp *AnthropicRequest) {
				var blocks []AnthropicContentBlock
				if err := json.Unmarshal(resp.Messages[0].Content, &blocks); err != nil {
					t.Fatalf("Failed to unmarshal content: %v", err)
				}

				if len(blocks) != 1 {
					t.Fatalf("Expected 1 content block, got %d", len(blocks))
				}

				if blocks[0].Type != "image" {
					t.Errorf("Block type = %s, want 'image'", blocks[0].Type)
				}
				sourceMap := blocks[0].Source
				if sourceMap["url"] != "https://example.com/image.png" {
					t.Errorf("URL = %v, want 'https://example.com/image.png'", sourceMap["url"])
				}
				if _, ok := sourceMap["media_type"]; ok {
					t.Errorf("Expected no media_type for URL image source, got %v", sourceMap["media_type"])
				}
			},
		},
		{
			name: "message with input_audio content",
			input: &OpenAIRequest{
				Model: "gpt-4-audio",
				Messages: []OpenAIMessage{
					{
						Role: core.RoleUser,
						Content: []interface{}{
							map[string]interface{}{
								"type": "text",
								"text": "Transcribe this audio:",
							},
							map[string]interface{}{
								"type": "input_audio",
								"input_audio": map[string]interface{}{
									"data":   "base64_audio_data",
									"format": "wav",
								},
							},
						},
					},
				},
			},
			wantErr: "anthropic provider does not support audio input blocks",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := converter.ConvertOpenAIRequestToAnthropic(context.Background(), tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error %q", tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("error = %q, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			tt.validate(t, got)
		})
	}
}

// TestMultimodalContentPipeline tests the complete conversion pipeline
// OpenAI -> Anthropic -> Argo -> Anthropic -> OpenAI
func TestMultimodalContentPipeline(t *testing.T) {
	mapper := &ModelMapper{config: &Config{}}
	converter := NewConverter(mapper)
	ctx := context.Background()

	// Start with an OpenAI request with image content
	openAIReq := &OpenAIRequest{
		Model: "gpt-4-vision",
		Messages: []OpenAIMessage{
			{
				Role: core.RoleUser,
				Content: []interface{}{
					map[string]interface{}{
						"type": "text",
						"text": "Analyze this image",
					},
					map[string]interface{}{
						"type": "image_url",
						"image_url": map[string]interface{}{
							"url": "https://example.com/test.png",
						},
					},
				},
			},
		},
		MaxTokens: intPtr(100),
	}

	// Step 1: OpenAI -> Anthropic
	anthReq, err := converter.ConvertOpenAIRequestToAnthropic(ctx, openAIReq)
	if err != nil {
		t.Fatalf("OpenAI to Anthropic conversion failed: %v", err)
	}

	// Verify image was preserved in Anthropic format
	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(anthReq.Messages[0].Content, &blocks); err != nil {
		t.Fatalf("Failed to unmarshal Anthropic content: %v", err)
	}
	if len(blocks) != 2 || blocks[1].Type != "image" {
		t.Errorf("Image not preserved in Anthropic format")
	}

	// Step 2: Anthropic -> Argo
	argoReq, err := converter.ConvertAnthropicToArgo(ctx, anthReq, "testuser")
	if err != nil {
		t.Fatalf("Anthropic to Argo conversion failed: %v", err)
	}

	// For non-OpenAI models, content should be preserved as-is
	if anthReq.Model == "claude-3-opus" || strings.HasPrefix(anthReq.Model, "claude") {
		// Content should be array of blocks
		if _, ok := argoReq.Messages[0].Content.([]AnthropicContentBlock); !ok {
			t.Errorf("Argo request should preserve content blocks for Claude models")
		}
	}

	// Step 3: Simulate Argo response
	argoResp := &ArgoChatResponse{
		Response: map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": "I can see the image",
				},
				map[string]interface{}{
					"type": "image",
					"source": map[string]interface{}{
						"url": "https://example.com/response.png",
					},
				},
			},
		},
	}

	// Step 4: Argo -> Anthropic (response)
	anthResp := converter.ConvertArgoToAnthropicWithRequest(argoResp, "gpt-4-vision", anthReq)

	// Verify ID was generated
	if anthResp.ID == "" {
		t.Error("Response ID should be generated")
	}

	// Verify multimodal content preserved
	if len(anthResp.Content) != 2 {
		t.Errorf("Expected 2 content blocks in response, got %d", len(anthResp.Content))
	}
	if anthResp.Content[1].Type != "image" {
		t.Errorf("Second block should be image, got %s", anthResp.Content[1].Type)
	}

	// Step 5: Anthropic -> OpenAI (response)
	openAIResp := converter.ConvertAnthropicResponseToOpenAI(anthResp, "gpt-4-vision")

	// Verify final OpenAI response
	if openAIResp.ID == "" || openAIResp.ID == "chatcmpl-" {
		t.Error("OpenAI response should have valid ID")
	}

	// Check multimodal content in OpenAI format
	// The response should have multimodal content as an array
	if openAIResp.Choices[0].Message.Content == nil {
		t.Fatal("Expected content in response, got nil")
	}

	content, ok := openAIResp.Choices[0].Message.Content.([]interface{})
	if !ok {
		// Debug: print what we actually got
		t.Logf("Content type: %T", openAIResp.Choices[0].Message.Content)
		t.Logf("Content value: %v", openAIResp.Choices[0].Message.Content)
		t.Fatalf("Expected content array, got %T", openAIResp.Choices[0].Message.Content)
	}
	if len(content) != 2 {
		t.Errorf("Expected 2 content parts, got %d", len(content))
	}

	// Verify image_url format
	part2, ok := content[1].(map[string]interface{})
	if !ok {
		t.Fatalf("Second part should be map, got %T", content[1])
	}
	if part2["type"] != "image_url" {
		t.Errorf("Second part type = %v, want 'image_url'", part2["type"])
	}
	imageUrl, ok := part2["image_url"].(map[string]interface{})
	if !ok {
		t.Fatalf("image_url should be map, got %T", part2["image_url"])
	}
	if imageUrl["url"] != "https://example.com/response.png" {
		t.Errorf("Image URL = %v, want 'https://example.com/response.png'", imageUrl["url"])
	}
}
