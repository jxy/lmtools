package core

import "encoding/json"

// ARCHITECTURAL NOTE: These types represent provider-specific message formats.
// They are only used at API boundaries for serialization/deserialization.
// All internal processing uses TypedMessage as the canonical representation.
// This ensures type safety and eliminates map[string]interface{} usage.

// OpenAIMessage represents a message in OpenAI format
type OpenAIMessage struct {
	Role       string           `json:"role"`
	Content    interface{}      `json:"content,omitempty"` // string or []OpenAIContent
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"` // For tool response messages
	Name       string           `json:"name,omitempty"`
}

// OpenAIContent represents content in OpenAI multimodal format
type OpenAIContent struct {
	Type       string                 `json:"type"` // "text", "image_url", "input_audio", "file"
	Text       string                 `json:"text,omitempty"`
	ImageURL   *OpenAIImageURL        `json:"image_url,omitempty"`
	InputAudio map[string]interface{} `json:"input_audio,omitempty"`
	File       map[string]interface{} `json:"file,omitempty"`
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
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []AnthropicContent
}

// AnthropicContent represents content in Anthropic format
type AnthropicContent struct {
	Type       string                 `json:"type"` // "text", "image", "tool_use", "tool_result", etc.
	Text       string                 `json:"text,omitempty"`
	Source     *AnthropicImageSource  `json:"source,omitempty"`
	ID         string                 `json:"id,omitempty"`          // For tool_use
	Name       string                 `json:"name,omitempty"`        // For tool_use
	Input      json.RawMessage        `json:"input,omitempty"`       // For tool_use
	ToolUseID  string                 `json:"tool_use_id,omitempty"` // For tool_result
	Content    string                 `json:"content,omitempty"`     // For tool_result
	IsError    bool                   `json:"is_error,omitempty"`    // For tool_result
	InputAudio map[string]interface{} `json:"input_audio,omitempty"` // For audio
	File       map[string]interface{} `json:"file,omitempty"`        // For file
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
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"` // JSON Schema
}

// AnthropicTool represents a tool definition in Anthropic format
type AnthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"` // JSON Schema
}

// GoogleTool represents a tool definition in Google format
type GoogleTool struct {
	FunctionDeclarations []GoogleFunctionDeclaration `json:"functionDeclarations"`
}

// GoogleFunctionDeclaration represents a function declaration in Google format
type GoogleFunctionDeclaration struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"` // Google-specific schema format
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
