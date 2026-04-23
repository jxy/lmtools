package proxy

import (
	"encoding/json"
	"lmtools/internal/core"
)

// AnthropicRequest represents a request to the Anthropic Messages API.
type AnthropicRequest struct {
	Model             string                 `json:"model"`
	MaxTokens         int                    `json:"max_tokens"`
	Messages          []AnthropicMessage     `json:"messages"`
	System            json.RawMessage        `json:"system,omitempty"`
	StopSequences     []string               `json:"stop_sequences,omitempty"`
	Stream            bool                   `json:"stream,omitempty"`
	Temperature       *float64               `json:"temperature,omitempty"`
	TopP              *float64               `json:"top_p,omitempty"`
	TopK              *int                   `json:"top_k,omitempty"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
	Tools             []AnthropicTool        `json:"tools,omitempty"`
	ToolChoice        *AnthropicToolChoice   `json:"tool_choice,omitempty"`
	Thinking          *AnthropicThinking     `json:"thinking,omitempty"`
	OutputConfig      *AnthropicOutputConfig `json:"output_config,omitempty"`
	Container         string                 `json:"container,omitempty"`
	ContextManagement interface{}            `json:"context_management,omitempty"`
	ServiceTier       string                 `json:"service_tier,omitempty"`
	InferenceGeo      string                 `json:"inference_geo,omitempty"`
	Speed             string                 `json:"speed,omitempty"`
	CacheControl      *AnthropicCacheControl `json:"cache_control,omitempty"`
	MCPServers        []interface{}          `json:"mcp_servers,omitempty"`
	Betas             string                 `json:"-"`
}

// AnthropicThinking represents the thinking configuration for Claude models.
type AnthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	Display      string `json:"display,omitempty"`
}

// AnthropicOutputConfig represents Anthropic output configuration.
type AnthropicOutputConfig struct {
	Effort string      `json:"effort,omitempty"`
	Format interface{} `json:"format,omitempty"`
}

// AnthropicCacheControl represents prompt-cache directives on supported fields.
type AnthropicCacheControl struct {
	Type string `json:"type,omitempty"`
	TTL  string `json:"ttl,omitempty"`
}

// AnthropicMessage represents a message in the Anthropic format.
type AnthropicMessage struct {
	Role    core.Role       `json:"role"`
	Content json.RawMessage `json:"content"`
}

// AnthropicContentBlock represents different types of content blocks.
type AnthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// For thinking blocks (Claude 3 Opus 4.1+)
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	// For image blocks
	Source map[string]interface{} `json:"source,omitempty"`
	// For audio blocks (OpenAI input_audio)
	InputAudio map[string]interface{} `json:"input_audio,omitempty"`
	// For file blocks
	File map[string]interface{} `json:"file,omitempty"`
	// For tool use
	ID     string                 `json:"id,omitempty"`
	Name   string                 `json:"name,omitempty"`
	Input  map[string]interface{} `json:"input,omitempty"`
	Caller interface{}            `json:"caller,omitempty"`
	// For tool result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	// For newer Anthropic blocks such as document/search_result/citations and
	// server-tool result blocks.
	Title            string                 `json:"title,omitempty"`
	Context          string                 `json:"context,omitempty"`
	Data             string                 `json:"data,omitempty"`
	Citations        []interface{}          `json:"citations,omitempty"`
	CitationsEnabled *bool                  `json:"citations_enabled,omitempty"`
	CacheControl     *AnthropicCacheControl `json:"cache_control,omitempty"`
}

// MarshalJSON provides custom JSON encoding to match Anthropic's event schema.
func (b AnthropicContentBlock) MarshalJSON() ([]byte, error) {
	m := map[string]interface{}{
		"type": b.Type,
	}

	switch b.Type {
	case "text":
		m["text"] = b.Text
	case "tool_use":
		if b.ID != "" {
			m["id"] = b.ID
		}
		if b.Name != "" {
			m["name"] = b.Name
		}
		if b.Input == nil {
			m["input"] = map[string]interface{}{}
		} else {
			m["input"] = b.Input
		}
	case "tool_result":
		if b.ToolUseID != "" {
			m["tool_use_id"] = b.ToolUseID
		}
		if len(b.Content) > 0 {
			m["content"] = b.Content
		}
		if b.IsError {
			m["is_error"] = true
		}
	default:
		if b.Text != "" {
			m["text"] = b.Text
		}
		if b.Thinking != "" {
			m["thinking"] = b.Thinking
		}
		if b.Signature != "" {
			m["signature"] = b.Signature
		}
		if b.Source != nil {
			m["source"] = b.Source
		}
		if b.InputAudio != nil {
			m["input_audio"] = b.InputAudio
		}
		if b.File != nil {
			m["file"] = b.File
		}
		if b.ID != "" {
			m["id"] = b.ID
		}
		if b.Name != "" {
			m["name"] = b.Name
		}
		if b.Input != nil {
			m["input"] = b.Input
		}
		if b.ToolUseID != "" {
			m["tool_use_id"] = b.ToolUseID
		}
		if len(b.Content) > 0 {
			m["content"] = b.Content
		}
		if b.IsError {
			m["is_error"] = b.IsError
		}
	}

	addAnthropicContentBlockExtras(m, b)

	return json.Marshal(m)
}

func addAnthropicContentBlockExtras(m map[string]interface{}, b AnthropicContentBlock) {
	if b.Title != "" {
		m["title"] = b.Title
	}
	if b.Context != "" {
		m["context"] = b.Context
	}
	if b.Data != "" {
		m["data"] = b.Data
	}
	if len(b.Citations) > 0 {
		m["citations"] = b.Citations
	}
	if b.CitationsEnabled != nil {
		m["citations_enabled"] = *b.CitationsEnabled
	}
	if b.CacheControl != nil {
		m["cache_control"] = b.CacheControl
	}
	if b.Caller != nil {
		m["caller"] = b.Caller
	}
}

// AnthropicTool represents a tool definition.
type AnthropicTool struct {
	Type         string                 `json:"type,omitempty"`
	Name         string                 `json:"name,omitempty"`
	Description  string                 `json:"description,omitempty"`
	InputSchema  interface{}            `json:"input_schema,omitempty"`
	CacheControl *AnthropicCacheControl `json:"cache_control,omitempty"`
}

// AnthropicToolChoice represents tool choice configuration.
type AnthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// AnthropicResponse represents a response from the Anthropic Messages API.
type AnthropicResponse struct {
	ID                string                  `json:"id"`
	Type              string                  `json:"type"`
	Role              core.Role               `json:"role"`
	Content           []AnthropicContentBlock `json:"content"`
	Model             string                  `json:"model"`
	StopReason        string                  `json:"stop_reason,omitempty"`
	StopSequence      string                  `json:"stop_sequence,omitempty"`
	StopDetails       interface{}             `json:"stop_details,omitempty"`
	Usage             *AnthropicUsage         `json:"usage,omitempty"`
	Container         interface{}             `json:"container,omitempty"`
	ContextManagement interface{}             `json:"context_management,omitempty"`
	ServiceTier       string                  `json:"service_tier,omitempty"`
	InferenceGeo      string                  `json:"inference_geo,omitempty"`
	Speed             string                  `json:"speed,omitempty"`
}

// AnthropicUsage represents token usage information.
type AnthropicUsage struct {
	InputTokens              int         `json:"input_tokens"`
	OutputTokens             int         `json:"output_tokens"`
	CacheCreationInputTokens int         `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int         `json:"cache_read_input_tokens,omitempty"`
	CacheCreation            interface{} `json:"cache_creation,omitempty"`
	ServerToolUse            interface{} `json:"server_tool_use,omitempty"`
	ServiceTier              string      `json:"service_tier,omitempty"`
	InferenceGeo             string      `json:"inference_geo,omitempty"`
}

// AnthropicStreamEvent represents a server-sent event from the streaming API.
type AnthropicStreamEvent struct {
	Type         string                 `json:"type"`
	Index        int                    `json:"index,omitempty"`
	Delta        *AnthropicContentBlock `json:"delta,omitempty"`
	ContentBlock *AnthropicContentBlock `json:"content_block,omitempty"`
	Message      *AnthropicResponse     `json:"message,omitempty"`
	Usage        *AnthropicUsage        `json:"usage,omitempty"`
}

// MessageStartEvent represents a message_start event.
type MessageStartEvent struct {
	Type    string            `json:"type"`
	Message AnthropicResponse `json:"message"`
}

// ContentBlockStartEvent represents a content_block_start event.
type ContentBlockStartEvent struct {
	Type         string                `json:"type"`
	Index        int                   `json:"index"`
	ContentBlock AnthropicContentBlock `json:"content_block"`
}

// ContentBlockDeltaEvent represents a content_block_delta event.
type ContentBlockDeltaEvent struct {
	Type  string       `json:"type"`
	Index int          `json:"index"`
	Delta DeltaContent `json:"delta"`
}

// DeltaContent represents the delta content in a streaming event.
type DeltaContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// For thinking_delta and signature_delta events.
	Thinking  string      `json:"thinking,omitempty"`
	Signature string      `json:"signature,omitempty"`
	Citation  interface{} `json:"citation,omitempty"`
	// Use pointer so empty string is emitted when explicitly present.
	PartialJSON *string `json:"partial_json,omitempty"`
}

// ContentBlockStopEvent represents a content_block_stop event.
type ContentBlockStopEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

// MessageDeltaEvent represents a message_delta event.
type MessageDeltaEvent struct {
	Type  string          `json:"type"`
	Delta MessageDelta    `json:"delta"`
	Usage *AnthropicUsage `json:"usage,omitempty"`
}

// MessageDelta represents the delta in a message_delta event.
type MessageDelta struct {
	StopReason        string      `json:"stop_reason,omitempty"`
	StopSequence      string      `json:"stop_sequence,omitempty"`
	StopDetails       interface{} `json:"stop_details,omitempty"`
	ContextManagement interface{} `json:"context_management,omitempty"`
}

// MessageStopEvent represents a message_stop event.
type MessageStopEvent struct {
	Type string `json:"type"`
}

// ErrorEvent represents an error event.
type ErrorEvent struct {
	Type  string    `json:"type"`
	Error ErrorInfo `json:"error"`
}

// ErrorInfo represents error information.
type ErrorInfo struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// PingEvent represents a ping event for keeping the connection alive.
type PingEvent struct {
	Type string `json:"type"`
}

// AnthropicTokenCountRequest represents a token counting request.
type AnthropicTokenCountRequest struct {
	Model    string             `json:"model"`
	System   json.RawMessage    `json:"system,omitempty"`
	Messages []AnthropicMessage `json:"messages"`
	Tools    []AnthropicTool    `json:"tools,omitempty"`
}

// AnthropicTokenCountResponse represents a token counting response.
type AnthropicTokenCountResponse struct {
	InputTokens int `json:"input_tokens"`
}
