package core

import (
	"encoding/json"
	"fmt"
)

// ARCHITECTURAL NOTE: These types represent provider-specific message formats.
// They are only used at API boundaries for serialization/deserialization.
// All internal processing uses TypedMessage as the canonical representation.
// This ensures type safety and eliminates map[string]interface{} usage.
//
// IMPORTANT: ContentUnion types (OpenAIContentUnion, AnthropicContentUnion)
// are ONLY for parsing responses from providers. They implement UnmarshalJSON
// but deliberately DO NOT implement MarshalJSON. Request building uses the
// dedicated marshaling functions:
//   - MarshalOpenAIMessagesForRequest
//   - MarshalAnthropicMessagesForRequest
//   - MarshalGoogleMessagesForRequest
// These functions build maps directly using ToMap() methods on content types.
// This separation ensures we never accidentally marshal ContentUnion types.

// OpenAIMessage represents a message in OpenAI format
type OpenAIMessage struct {
	Role       string             `json:"role"`
	Content    OpenAIContentUnion `json:"content,omitempty"` // string or []OpenAIContent
	ToolCalls  []OpenAIToolCall   `json:"tool_calls,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"` // For tool response messages
	Name       string             `json:"name,omitempty"`
}

// OpenAIContentUnion represents content that can be string or []OpenAIContent
// Note: This is an internal type used only for parsing responses.
// Request building uses MarshalOpenAIMessagesForRequest which builds maps directly.
type OpenAIContentUnion struct {
	Text     *string         // For simple text content (nil if array)
	Contents []OpenAIContent // For multimodal content (empty if text)
}

// MarshalJSON prevents accidental marshaling of the union type.
// This type should only be used for parsing responses.
// For request building, use MarshalOpenAIMessagesForRequest.
func (c OpenAIContentUnion) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("OpenAIContentUnion must not be marshaled directly; use MarshalOpenAIMessagesForRequest")
}

// ValidateForMarshal ensures only one field is set (Text or Contents, not both)
func (c *OpenAIContentUnion) ValidateForMarshal() error {
	hasText := c.Text != nil && *c.Text != ""
	hasContents := len(c.Contents) > 0
	if hasText && hasContents {
		return fmt.Errorf("invalid OpenAIContentUnion: both Text and Contents are set")
	}
	if !hasText && !hasContents {
		// Both empty is valid (represents no content)
		return nil
	}
	return nil
}

// UnmarshalJSON implements custom JSON unmarshaling from string or array
// This is needed for parsing provider responses and test fixtures
func (c *OpenAIContentUnion) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as string first
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		c.Text = &text
		c.Contents = nil
		return nil
	}

	// Try to unmarshal as array
	var contents []OpenAIContent
	if err := json.Unmarshal(data, &contents); err == nil {
		c.Contents = contents
		c.Text = nil
		return nil
	}

	return fmt.Errorf("invalid content union: expected string or array")
}

// OpenAIContent represents content in OpenAI multimodal format
type OpenAIContent struct {
	Type       string          `json:"type"` // "text", "image_url", "input_audio", "file"
	Text       string          `json:"text,omitempty"`
	ImageURL   *OpenAIImageURL `json:"image_url,omitempty"`
	InputAudio *AudioData      `json:"input_audio,omitempty"`
	File       *FileData       `json:"file,omitempty"`
}

// ToMap converts OpenAIContent to map[string]interface{} for request marshaling
func (c OpenAIContent) ToMap() map[string]interface{} {
	m := map[string]interface{}{"type": c.Type}

	if c.Text != "" {
		m["text"] = c.Text
	}

	if c.ImageURL != nil {
		imageURLMap := map[string]interface{}{
			"url": c.ImageURL.URL,
		}
		if c.ImageURL.Detail != "" {
			imageURLMap["detail"] = c.ImageURL.Detail
		}
		m["image_url"] = imageURLMap
	}

	if c.InputAudio != nil {
		audioMap := map[string]interface{}{}
		if c.InputAudio.ID != "" {
			audioMap["id"] = c.InputAudio.ID
		}
		if c.InputAudio.Data != "" {
			audioMap["data"] = c.InputAudio.Data
		}
		if c.InputAudio.Format != "" {
			audioMap["format"] = c.InputAudio.Format
		}
		if len(audioMap) > 0 {
			m["input_audio"] = audioMap
		}
	}

	if c.File != nil {
		fileMap := map[string]interface{}{}
		if c.File.FileID != "" {
			fileMap["file_id"] = c.File.FileID
		}
		if len(fileMap) > 0 {
			m["file"] = fileMap
		}
	}

	return m
}

// OpenAIImageURL represents an image URL in OpenAI format
type OpenAIImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"` // "auto", "low", or "high"
}

// OpenAIToolCall represents a tool call in OpenAI format
type OpenAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"` // Always "function"
	Function OpenAIFunctionCall `json:"function"`
}

// OpenAIFunctionCall represents a function call in OpenAI format
type OpenAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// AnthropicMessage represents a message in Anthropic format
type AnthropicMessage struct {
	Role    string                `json:"role"`
	Content AnthropicContentUnion `json:"content"` // string or []AnthropicContent
}

// AnthropicContentUnion represents content that can be string or []AnthropicContent
// Note: This is an internal type used only for parsing responses.
// Request building uses MarshalAnthropicMessagesForRequest which builds maps directly.
type AnthropicContentUnion struct {
	Text     *string            // For simple text content (nil if array)
	Contents []AnthropicContent // For multimodal content (empty if text)
}

// MarshalJSON prevents accidental marshaling of the union type.
// This type should only be used for parsing responses.
// For request building, use MarshalAnthropicMessagesForRequest.
func (c AnthropicContentUnion) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("AnthropicContentUnion must not be marshaled directly; use MarshalAnthropicMessagesForRequest")
}

// ValidateForMarshal ensures only one field is set (Text or Contents, not both)
func (c *AnthropicContentUnion) ValidateForMarshal() error {
	hasText := c.Text != nil && *c.Text != ""
	hasContents := len(c.Contents) > 0
	if hasText && hasContents {
		return fmt.Errorf("invalid AnthropicContentUnion: both Text and Contents are set")
	}
	if !hasText && !hasContents {
		// Both empty is valid (represents no content)
		return nil
	}
	return nil
}

// UnmarshalJSON implements custom JSON unmarshaling from string or array
// This is needed for parsing provider responses and test fixtures
func (c *AnthropicContentUnion) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as string first
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		c.Text = &text
		c.Contents = nil
		return nil
	}

	// Try to unmarshal as array
	var contents []AnthropicContent
	if err := json.Unmarshal(data, &contents); err == nil {
		c.Contents = contents
		c.Text = nil
		return nil
	}

	return fmt.Errorf("invalid content union: expected string or array")
}

// AnthropicContent represents content in Anthropic format
type AnthropicContent struct {
	Type       string                `json:"type"` // "text", "image", "tool_use", "tool_result", etc.
	Text       string                `json:"text,omitempty"`
	Source     *AnthropicImageSource `json:"source,omitempty"`
	ID         string                `json:"id,omitempty"`          // For tool_use
	Name       string                `json:"name,omitempty"`        // For tool_use
	Input      json.RawMessage       `json:"input,omitempty"`       // For tool_use
	ToolUseID  string                `json:"tool_use_id,omitempty"` // For tool_result
	Content    string                `json:"content,omitempty"`     // For tool_result
	IsError    bool                  `json:"is_error,omitempty"`    // For tool_result
	InputAudio *AudioData            `json:"input_audio,omitempty"` // For audio
	File       *FileData             `json:"file,omitempty"`        // For file
}

// ToMap converts AnthropicContent to map[string]interface{} for request marshaling
func (c AnthropicContent) ToMap() map[string]interface{} {
	m := map[string]interface{}{"type": c.Type}

	if c.Text != "" {
		m["text"] = c.Text
	}

	if c.Source != nil {
		sourceMap := map[string]interface{}{
			"type": c.Source.Type,
		}
		if c.Source.URL != "" {
			sourceMap["url"] = c.Source.URL
		}
		if c.Source.MediaType != "" {
			sourceMap["media_type"] = c.Source.MediaType
		}
		if c.Source.Data != "" {
			sourceMap["data"] = c.Source.Data
		}
		m["source"] = sourceMap
	}

	if c.ID != "" {
		m["id"] = c.ID
	}

	if c.Name != "" {
		m["name"] = c.Name
	}

	if len(c.Input) > 0 {
		m["input"] = c.Input
	}

	if c.ToolUseID != "" {
		m["tool_use_id"] = c.ToolUseID
	}

	if c.Content != "" {
		m["content"] = c.Content
	}

	if c.IsError {
		m["is_error"] = c.IsError
	}

	if c.InputAudio != nil {
		audioMap := map[string]interface{}{}
		if c.InputAudio.ID != "" {
			audioMap["id"] = c.InputAudio.ID
		}
		if c.InputAudio.Data != "" {
			audioMap["data"] = c.InputAudio.Data
		}
		if c.InputAudio.Format != "" {
			audioMap["format"] = c.InputAudio.Format
		}
		if len(audioMap) > 0 {
			m["input_audio"] = audioMap
		}
	}

	if c.File != nil {
		fileMap := map[string]interface{}{}
		if c.File.FileID != "" {
			fileMap["file_id"] = c.File.FileID
		}
		if len(fileMap) > 0 {
			m["file"] = fileMap
		}
	}

	return m
}

// AnthropicImageSource represents an image source in Anthropic format
type AnthropicImageSource struct {
	Type      string `json:"type"` // "url" or "base64"
	URL       string `json:"url,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"` // For base64
}

// GoogleMessage represents a message in Google format
type GoogleMessage struct {
	Role  string       `json:"role"` // "user", "model", or "system"
	Parts []GooglePart `json:"parts"`
}

// GooglePart represents a part in Google format
type GooglePart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *GoogleFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *GoogleFunctionResponse `json:"functionResponse,omitempty"`
	InlineData       *GoogleInlineData       `json:"inlineData,omitempty"`
}

// ToMap converts GooglePart to map[string]interface{} for request marshaling
func (p GooglePart) ToMap() map[string]interface{} {
	m := map[string]interface{}{}

	if p.Text != "" {
		m["text"] = p.Text
	}

	if p.FunctionCall != nil {
		fcMap := map[string]interface{}{
			"name": p.FunctionCall.Name,
		}
		if len(p.FunctionCall.Args) > 0 {
			fcMap["args"] = p.FunctionCall.Args
		}
		m["functionCall"] = fcMap
	}

	if p.FunctionResponse != nil {
		frMap := map[string]interface{}{
			"name": p.FunctionResponse.Name,
			"response": map[string]interface{}{
				"content": p.FunctionResponse.Response.Content,
			},
		}
		if p.FunctionResponse.Response.Error {
			frMap["response"].(map[string]interface{})["error"] = true
		}
		m["functionResponse"] = frMap
	}

	if p.InlineData != nil {
		m["inlineData"] = map[string]interface{}{
			"mimeType": p.InlineData.MimeType,
			"data":     p.InlineData.Data,
		}
	}

	return m
}

// GoogleFunctionCall represents a function call in Google format
type GoogleFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// GoogleFunctionResponse represents a function response in Google format
type GoogleFunctionResponse struct {
	Name     string                `json:"name"`
	Response GoogleResponseContent `json:"response"`
}

// GoogleResponseContent represents the content of a function response
type GoogleResponseContent struct {
	Content string `json:"content"`
	Error   bool   `json:"error,omitempty"`
}

// GoogleInlineData represents inline data (e.g., images) in Google format
type GoogleInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"` // Base64 encoded
}

// OpenAITool represents a tool definition in OpenAI format
type OpenAITool struct {
	Type     string             `json:"type"` // Always "function"
	Function OpenAIToolFunction `json:"function"`
}

// OpenAIToolFunction represents a function definition in OpenAI format
type OpenAIToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
}

// AnthropicTool represents a tool definition in Anthropic format
type AnthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"` // JSON Schema
}

// GoogleTool represents a tool definition in Google format
type GoogleTool struct {
	FunctionDeclarations []GoogleFunctionDeclaration `json:"functionDeclarations"`
}

// GoogleFunctionDeclaration represents a function declaration in Google format
type GoogleFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // Google-specific schema format
}

// OpenAIToolChoice represents tool choice configuration in OpenAI format
type OpenAIToolChoice struct {
	Type     string                    `json:"type"` // "none", "auto", "function"
	Function *OpenAIToolChoiceFunction `json:"function,omitempty"`
}

// OpenAIToolChoiceFunction specifies a specific function to call
type OpenAIToolChoiceFunction struct {
	Name string `json:"name"`
}

// AnthropicToolChoice represents tool choice configuration in Anthropic format
type AnthropicToolChoice struct {
	Type string `json:"type"`           // "auto", "any", "tool"
	Name string `json:"name,omitempty"` // For specific tool selection
}
