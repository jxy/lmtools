package proxy

import (
	"encoding/json"
	"lmtools/internal/core"
)

// OpenAIRequest represents a request to the OpenAI Chat Completion API.
type OpenAIRequest struct {
	Model            string               `json:"model"`
	Messages         []OpenAIMessage      `json:"messages"`
	Temperature      *float64             `json:"temperature,omitempty"`
	TopP             *float64             `json:"top_p,omitempty"`
	N                *int                 `json:"n,omitempty"`
	Stream           bool                 `json:"stream,omitempty"`
	Stop             []string             `json:"stop,omitempty"`
	MaxTokens        *int                 `json:"max_tokens,omitempty"`
	PresencePenalty  *float64             `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64             `json:"frequency_penalty,omitempty"`
	LogitBias        map[string]int       `json:"logit_bias,omitempty"`
	User             string               `json:"user,omitempty"`
	Tools            []OpenAITool         `json:"tools,omitempty"`
	ToolChoice       interface{}          `json:"tool_choice,omitempty"`
	ResponseFormat   *ResponseFormat      `json:"response_format,omitempty"`
	ReasoningEffort  string               `json:"reasoning_effort,omitempty"`
	StreamOptions    *OpenAIStreamOptions `json:"stream_options,omitempty"`
}

// OpenAIStreamOptions represents streaming options for OpenAI requests.
type OpenAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// OpenAIMessage represents a message in the OpenAI format.
type OpenAIMessage struct {
	Role       core.Role   `json:"role"`
	Content    interface{} `json:"content"`
	Name       string      `json:"name,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
}

// OpenAIContent represents content in multimodal messages.
type OpenAIContent struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL represents an image URL in OpenAI format.
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// OpenAITool represents a tool in OpenAI format.
type OpenAITool struct {
	Type     string     `json:"type"`
	Function OpenAIFunc `json:"function"`
}

// OpenAIFunc represents a function definition.
type OpenAIFunc struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// ToolCall represents a tool call in OpenAI format.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall represents a function call.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ResponseFormat specifies the format of the response.
type ResponseFormat struct {
	Type string `json:"type"`
}

// OpenAIResponse represents a response from the OpenAI API.
type OpenAIResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   *OpenAIUsage   `json:"usage,omitempty"`
}

// OpenAIChoice represents a choice in the response.
type OpenAIChoice struct {
	Index        int           `json:"index"`
	Message      OpenAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason,omitempty"`
}

// OpenAIUsage represents token usage.
type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// OpenAIStreamChunk represents a chunk in the streaming response.
type OpenAIStreamChunk struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"`
	Created int64               `json:"created"`
	Model   string              `json:"model"`
	Choices []OpenAIStreamDelta `json:"choices"`
	// Always include usage key; nil encodes as null to match OpenAI include_usage behavior.
	Usage *OpenAIUsage `json:"usage"`
}

// OpenAIStreamDelta represents a delta in streaming.
type OpenAIStreamDelta struct {
	Index int         `json:"index"`
	Delta OpenAIDelta `json:"delta"`
	// Always include finish_reason key; nil encodes as null for consistent schema.
	FinishReason *string `json:"finish_reason"`
}

// OpenAIDelta represents the delta content.
type OpenAIDelta struct {
	Role    *core.Role `json:"role,omitempty"`
	Content *string    `json:"content,omitempty"`
	// ContentNull forces encoding of `"content": null` when true.
	ContentNull bool            `json:"-"`
	ToolCalls   []ToolCallDelta `json:"tool_calls,omitempty"`
}

// MarshalJSON implements custom JSON encoding for OpenAIDelta to support
// conditional inclusion of `content: null` in the first assistant delta.
func (d OpenAIDelta) MarshalJSON() ([]byte, error) {
	m := map[string]interface{}{}
	if d.Role != nil {
		m["role"] = d.Role
	}
	if d.ContentNull {
		m["content"] = nil
	} else if d.Content != nil {
		m["content"] = *d.Content
	}
	if len(d.ToolCalls) > 0 {
		m["tool_calls"] = d.ToolCalls
	}
	return json.Marshal(m)
}

// ToolCallDelta represents a tool call delta in streaming.
type ToolCallDelta struct {
	Index    int                `json:"index"`
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function *FunctionCallDelta `json:"function,omitempty"`
}

// FunctionCallDelta represents a function call delta.
type FunctionCallDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}
