package proxy

// ArgoChatRequest represents a chat request to the Argo API.
type ArgoChatRequest struct {
	User                string                 `json:"user"`
	Model               string                 `json:"model"`
	Messages            []ArgoMessage          `json:"messages"`
	Temperature         *float64               `json:"temperature,omitempty"`
	TopP                *float64               `json:"top_p,omitempty"`
	Stop                []string               `json:"stop,omitempty"`
	MaxTokens           int                    `json:"max_tokens,omitempty"`
	MaxCompletionTokens int                    `json:"max_completion_tokens,omitempty"`
	Tools               interface{}            `json:"tools,omitempty"`
	ToolChoice          interface{}            `json:"tool_choice,omitempty"`
	ReasoningEffort     string                 `json:"reasoning_effort,omitempty"`
	ResponseFormat      *ResponseFormat        `json:"response_format,omitempty"`
	Metadata            map[string]interface{} `json:"metadata,omitempty"`
	ServiceTier         string                 `json:"service_tier,omitempty"`
	Thinking            *AnthropicThinking     `json:"thinking,omitempty"`
}

// ArgoMessage represents a message in Argo format.
type ArgoMessage struct {
	Role       string      `json:"role"`
	Content    interface{} `json:"content"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
}

// ArgoChatResponse represents a chat response from Argo.
type ArgoChatResponse struct {
	Response interface{} `json:"response"`
}

// ArgoEmbedRequest represents an embedding request to Argo.
type ArgoEmbedRequest struct {
	User   string   `json:"user"`
	Model  string   `json:"model"`
	Prompt []string `json:"prompt"`
}

// ArgoEmbedResponse represents an embedding response from Argo.
type ArgoEmbedResponse struct {
	Embedding [][]float64 `json:"embedding"`
}

// ArgoTool represents a tool in Argo format.
type ArgoTool struct {
	Type     string   `json:"type"`
	Function ArgoFunc `json:"function"`
}

// ArgoFunc represents a function definition in Argo format.
type ArgoFunc struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}
