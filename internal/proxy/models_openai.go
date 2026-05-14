package proxy

import (
	"encoding/json"
	"lmtools/internal/core"
)

// OpenAIRequest represents a request to the OpenAI Chat Completion API.
type OpenAIRequest struct {
	Model                string                 `json:"model"`
	Messages             []OpenAIMessage        `json:"messages"`
	Temperature          *float64               `json:"temperature,omitempty"`
	TopP                 *float64               `json:"top_p,omitempty"`
	N                    *int                   `json:"n,omitempty"`
	Stream               bool                   `json:"stream,omitempty"`
	Stop                 []string               `json:"stop,omitempty"`
	MaxTokens            *int                   `json:"max_tokens,omitempty"`
	MaxCompletionTokens  *int                   `json:"max_completion_tokens,omitempty"`
	PresencePenalty      *float64               `json:"presence_penalty,omitempty"`
	FrequencyPenalty     *float64               `json:"frequency_penalty,omitempty"`
	LogitBias            map[string]int         `json:"logit_bias,omitempty"`
	User                 string                 `json:"user,omitempty"`
	Tools                []OpenAITool           `json:"tools,omitempty"`
	ToolChoice           interface{}            `json:"tool_choice,omitempty"`
	ResponseFormat       *ResponseFormat        `json:"response_format,omitempty"`
	ReasoningEffort      string                 `json:"reasoning_effort,omitempty"`
	StreamOptions        *OpenAIStreamOptions   `json:"stream_options,omitempty"`
	Metadata             map[string]interface{} `json:"metadata,omitempty"`
	Store                *bool                  `json:"store,omitempty"`
	ServiceTier          string                 `json:"service_tier,omitempty"`
	Seed                 *int                   `json:"seed,omitempty"`
	Verbosity            string                 `json:"verbosity,omitempty"`
	Modalities           []string               `json:"modalities,omitempty"`
	Audio                *OpenAIAudioConfig     `json:"audio,omitempty"`
	Prediction           interface{}            `json:"prediction,omitempty"`
	WebSearchOptions     interface{}            `json:"web_search_options,omitempty"`
	PromptCacheKey       string                 `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string                 `json:"prompt_cache_retention,omitempty"`
	SafetyIdentifier     string                 `json:"safety_identifier,omitempty"`
	ParallelToolCalls    *bool                  `json:"parallel_tool_calls,omitempty"`
	Logprobs             *bool                  `json:"logprobs,omitempty"`
	TopLogprobs          *int                   `json:"top_logprobs,omitempty"`
	ExtraBody            map[string]interface{} `json:"extra_body,omitempty"`
}

// OpenAIStreamOptions represents streaming options for OpenAI requests.
type OpenAIStreamOptions struct {
	IncludeUsage       bool  `json:"include_usage,omitempty"`
	IncludeObfuscation *bool `json:"include_obfuscation,omitempty"`
}

// OpenAIAudioConfig represents output audio options for multimodal responses.
type OpenAIAudioConfig struct {
	Format string `json:"format,omitempty"`
	Voice  string `json:"voice,omitempty"`
}

// OpenAIMessage represents a message in the OpenAI format.
type OpenAIMessage struct {
	Role         core.Role     `json:"role"`
	Content      interface{}   `json:"content"`
	Name         string        `json:"name,omitempty"`
	FunctionCall *FunctionCall `json:"function_call,omitempty"`
	ToolCalls    []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID   string        `json:"tool_call_id,omitempty"`
	Refusal      *string       `json:"refusal,omitempty"`
	Annotations  []interface{} `json:"annotations,omitempty"`
	Audio        interface{}   `json:"audio,omitempty"`
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
	Type     string      `json:"type"`
	Function OpenAIFunc  `json:"function"`
	Custom   interface{} `json:"custom,omitempty"`
}

// MarshalJSON keeps OpenAI tool payloads compatible with both function tools
// and newer custom tools. The legacy struct shape requires Function to be a
// value, so omit it explicitly when it is not meaningful.
func (t OpenAITool) MarshalJSON() ([]byte, error) {
	m := map[string]interface{}{
		"type": t.Type,
	}
	if t.Type == "function" || !isZeroOpenAIFunc(t.Function) {
		m["function"] = t.Function
	}
	if t.Custom != nil {
		m["custom"] = t.Custom
	}
	return json.Marshal(m)
}

func isZeroOpenAIFunc(fn OpenAIFunc) bool {
	return fn.Name == "" && fn.Description == "" && fn.Parameters == nil && fn.Strict == nil
}

// OpenAIFunc represents a function definition.
type OpenAIFunc struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
	Strict      *bool       `json:"strict,omitempty"`
}

// ToolCall represents a tool call in OpenAI format.
type ToolCall struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function FunctionCall    `json:"function"`
	Custom   *CustomToolCall `json:"custom,omitempty"`
}

// FunctionCall represents a function call.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// CustomToolCall represents an OpenAI Chat custom tool call.
type CustomToolCall struct {
	Name  string `json:"name"`
	Input string `json:"input,omitempty"`
}

// MarshalJSON omits the function field for custom tool calls.
func (tc ToolCall) MarshalJSON() ([]byte, error) {
	m := map[string]interface{}{
		"id":   tc.ID,
		"type": tc.Type,
	}
	if tc.Type == "custom" {
		if tc.Custom != nil {
			m["custom"] = tc.Custom
		}
		return json.Marshal(m)
	}
	m["function"] = tc.Function
	if tc.Custom != nil {
		m["custom"] = tc.Custom
	}
	return json.Marshal(m)
}

// ResponseFormat specifies the format of the response.
type ResponseFormat struct {
	Type       string            `json:"type"`
	JSONSchema *OpenAIJSONSchema `json:"json_schema,omitempty"`
}

// OpenAIJSONSchema specifies the schema used by response_format=json_schema.
type OpenAIJSONSchema struct {
	Name        string      `json:"name,omitempty"`
	Description string      `json:"description,omitempty"`
	Schema      interface{} `json:"schema,omitempty"`
	Strict      *bool       `json:"strict,omitempty"`
}

// OpenAIResponse represents a response from the OpenAI API.
type OpenAIResponse struct {
	ID                  string         `json:"id"`
	Object              string         `json:"object"`
	Created             int64          `json:"created"`
	Model               string         `json:"model"`
	Choices             []OpenAIChoice `json:"choices"`
	Usage               *OpenAIUsage   `json:"usage,omitempty"`
	ServiceTier         string         `json:"service_tier,omitempty"`
	SystemFingerprint   string         `json:"system_fingerprint,omitempty"`
	PromptFilterResults interface{}    `json:"prompt_filter_results,omitempty"`
}

// OpenAIChoice represents a choice in the response.
type OpenAIChoice struct {
	Index                int           `json:"index"`
	Message              OpenAIMessage `json:"message"`
	FinishReason         string        `json:"finish_reason,omitempty"`
	Logprobs             interface{}   `json:"logprobs,omitempty"`
	ContentFilterResults interface{}   `json:"content_filter_results,omitempty"`
}

// OpenAIUsage represents token usage.
type OpenAIUsage struct {
	PromptTokens            int                           `json:"prompt_tokens"`
	CompletionTokens        int                           `json:"completion_tokens"`
	TotalTokens             int                           `json:"total_tokens"`
	PromptTokensDetails     *OpenAITokenDetails           `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *OpenAICompletionTokenDetails `json:"completion_tokens_details,omitempty"`
}

// OpenAITokenDetails represents token accounting details for prompt tokens.
type OpenAITokenDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
	AudioTokens  int `json:"audio_tokens,omitempty"`
}

// OpenAICompletionTokenDetails represents token accounting details for completion tokens.
type OpenAICompletionTokenDetails struct {
	ReasoningTokens          int `json:"reasoning_tokens,omitempty"`
	AudioTokens              int `json:"audio_tokens,omitempty"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens,omitempty"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens,omitempty"`
}

// OpenAIStreamChunk represents a chunk in the streaming response.
type OpenAIStreamChunk struct {
	ID                  string              `json:"id"`
	Object              string              `json:"object"`
	Created             int64               `json:"created"`
	Model               string              `json:"model"`
	Choices             []OpenAIStreamDelta `json:"choices"`
	ServiceTier         string              `json:"service_tier,omitempty"`
	SystemFingerprint   string              `json:"system_fingerprint,omitempty"`
	Obfuscation         string              `json:"obfuscation,omitempty"`
	PromptFilterResults interface{}         `json:"prompt_filter_results,omitempty"`
	// Always include usage key; nil encodes as null to match OpenAI include_usage behavior.
	Usage *OpenAIUsage `json:"usage"`
}

// OpenAIStreamDelta represents a delta in streaming.
type OpenAIStreamDelta struct {
	Index                int         `json:"index"`
	Delta                OpenAIDelta `json:"delta"`
	Logprobs             interface{} `json:"logprobs,omitempty"`
	ContentFilterResults interface{} `json:"content_filter_results,omitempty"`
	// Always include finish_reason key; nil encodes as null for consistent schema.
	FinishReason *string `json:"finish_reason"`
}

// OpenAIDelta represents the delta content.
type OpenAIDelta struct {
	Role         *core.Role         `json:"role,omitempty"`
	Content      *string            `json:"content,omitempty"`
	FunctionCall *FunctionCallDelta `json:"function_call,omitempty"`
	Refusal      *string            `json:"refusal,omitempty"`
	Audio        interface{}        `json:"audio,omitempty"`
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
	if d.FunctionCall != nil {
		m["function_call"] = d.FunctionCall
	}
	if d.Refusal != nil {
		m["refusal"] = *d.Refusal
	}
	if d.Audio != nil {
		m["audio"] = d.Audio
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
