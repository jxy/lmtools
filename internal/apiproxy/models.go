package apiproxy

import (
	"encoding/json"
)

// Common types
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Anthropic API Models

// AnthropicRequest represents a request to the Anthropic Messages API
type AnthropicRequest struct {
	Model         string                 `json:"model"`
	MaxTokens     int                    `json:"max_tokens"`
	Messages      []AnthropicMessage     `json:"messages"`
	System        json.RawMessage        `json:"system,omitempty"`
	StopSequences []string               `json:"stop_sequences,omitempty"`
	Stream        bool                   `json:"stream,omitempty"`
	Temperature   *float64               `json:"temperature,omitempty"`
	TopP          *float64               `json:"top_p,omitempty"`
	TopK          *int                   `json:"top_k,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	Tools         []AnthropicTool        `json:"tools,omitempty"`
	ToolChoice    *AnthropicToolChoice   `json:"tool_choice,omitempty"`
}

// AnthropicMessage represents a message in the Anthropic format
type AnthropicMessage struct {
	Role    Role            `json:"role"`
	Content json.RawMessage `json:"content"`
}

// AnthropicContentBlock represents different types of content blocks
type AnthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// For tool use
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
	// For tool result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	// For images
	Source *ImageSource `json:"source,omitempty"`
}

// ImageSource represents an image source
type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
}

// AnthropicTool represents a tool definition
type AnthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// AnthropicToolChoice represents tool choice configuration
type AnthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// AnthropicResponse represents a response from the Anthropic Messages API
type AnthropicResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         Role                    `json:"role"`
	Content      []AnthropicContentBlock `json:"content"`
	Model        string                  `json:"model"`
	StopReason   string                  `json:"stop_reason,omitempty"`
	StopSequence string                  `json:"stop_sequence,omitempty"`
	Usage        AnthropicUsage          `json:"usage"`
}

// AnthropicUsage represents token usage information
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// AnthropicTokenCountRequest represents a token counting request
type AnthropicTokenCountRequest struct {
	Model      string               `json:"model"`
	Messages   []AnthropicMessage   `json:"messages"`
	System     json.RawMessage      `json:"system,omitempty"`
	Tools      []AnthropicTool      `json:"tools,omitempty"`
	ToolChoice *AnthropicToolChoice `json:"tool_choice,omitempty"`
}

// AnthropicTokenCountResponse represents a token counting response
type AnthropicTokenCountResponse struct {
	InputTokens int `json:"input_tokens"`
}

// OpenAI API Models

// OpenAIRequest represents a request to the OpenAI Chat Completions API
type OpenAIRequest struct {
	Model            string             `json:"model"`
	Messages         []OpenAIMessage    `json:"messages"`
	MaxTokens        int                `json:"max_tokens,omitempty"`
	Temperature      *float64           `json:"temperature,omitempty"`
	TopP             *float64           `json:"top_p,omitempty"`
	N                int                `json:"n,omitempty"`
	Stream           bool               `json:"stream,omitempty"`
	Stop             []string           `json:"stop,omitempty"`
	PresencePenalty  *float64           `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64           `json:"frequency_penalty,omitempty"`
	LogitBias        map[string]float64 `json:"logit_bias,omitempty"`
	User             string             `json:"user,omitempty"`
	Tools            []OpenAITool       `json:"tools,omitempty"`
	ToolChoice       interface{}        `json:"tool_choice,omitempty"`
}

// OpenAIMessage represents a message in the OpenAI format
type OpenAIMessage struct {
	Role       Role        `json:"role"`
	Content    interface{} `json:"content"` // Can be string or array of content parts
	Name       string      `json:"name,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
}

// OpenAITool represents a tool definition in OpenAI format
type OpenAITool struct {
	Type     string             `json:"type"` // Always "function"
	Function OpenAIToolFunction `json:"function"`
}

// OpenAIToolFunction represents a function tool
type OpenAIToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ToolCall represents a tool call in OpenAI format
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // Always "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall represents the function call details
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// OpenAIResponse represents a response from the OpenAI Chat Completions API
type OpenAIResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   OpenAIUsage    `json:"usage"`
}

// OpenAIChoice represents a choice in the response
type OpenAIChoice struct {
	Index        int           `json:"index"`
	Message      OpenAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

// OpenAIUsage represents token usage information
type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Google/Gemini API Models

// GeminiRequest represents a request to the Gemini API
type GeminiRequest struct {
	Contents         []GeminiContent         `json:"contents"`
	Tools            []GeminiTool            `json:"tools,omitempty"`
	ToolConfig       *GeminiToolConfig       `json:"toolConfig,omitempty"`
	SafetySettings   []GeminiSafetySetting   `json:"safetySettings,omitempty"`
	GenerationConfig *GeminiGenerationConfig `json:"generationConfig,omitempty"`
}

// GeminiContent represents content in Gemini format
type GeminiContent struct {
	Role  string       `json:"role"` // "user" or "model"
	Parts []GeminiPart `json:"parts"`
}

// GeminiPart represents a part of content
type GeminiPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *GeminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *GeminiFunctionResponse `json:"functionResponse,omitempty"`
}

// GeminiFunctionCall represents a function call
type GeminiFunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

// GeminiFunctionResponse represents a function response
type GeminiFunctionResponse struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

// GeminiTool represents a tool in Gemini format
type GeminiTool struct {
	FunctionDeclarations []GeminiFunctionDeclaration `json:"functionDeclarations"`
}

// GeminiFunctionDeclaration represents a function declaration
type GeminiFunctionDeclaration struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// GeminiToolConfig represents tool configuration
type GeminiToolConfig struct {
	FunctionCallingConfig GeminiFunctionCallingConfig `json:"functionCallingConfig"`
}

// GeminiFunctionCallingConfig represents function calling configuration
type GeminiFunctionCallingConfig struct {
	Mode                 string   `json:"mode"` // "AUTO", "ANY", "NONE"
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

// GeminiSafetySetting represents safety settings
type GeminiSafetySetting struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

// GeminiGenerationConfig represents generation configuration
type GeminiGenerationConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	TopK            *int     `json:"topK,omitempty"`
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

// GeminiResponse represents a response from the Gemini API
type GeminiResponse struct {
	Candidates    []GeminiCandidate `json:"candidates"`
	UsageMetadata GeminiUsage       `json:"usageMetadata"`
}

// GeminiCandidate represents a candidate response
type GeminiCandidate struct {
	Content       GeminiContent  `json:"content"`
	FinishReason  string         `json:"finishReason"`
	SafetyRatings []SafetyRating `json:"safetyRatings"`
}

// SafetyRating represents a safety rating
type SafetyRating struct {
	Category    string `json:"category"`
	Probability string `json:"probability"`
}

// GeminiUsage represents usage metadata
type GeminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

// Argo API Models (imported from argolib)

// ArgoMessage represents a message in Argo format
type ArgoMessage struct {
	Role       string      `json:"role"`
	Content    interface{} `json:"content"`                // Can be string or []AnthropicContentBlock
	ToolCallID string      `json:"tool_call_id,omitempty"` // For OpenAI tool messages
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`   // For OpenAI assistant tool calls
}

// ArgoTool represents a tool definition for Argo API
// The format varies by provider (OpenAI, Gemini, Anthropic)
type ArgoTool interface{}

// ArgoChatRequest represents a chat request to Argo API
type ArgoChatRequest struct {
	User                string        `json:"user"`
	Model               string        `json:"model"`
	Messages            []ArgoMessage `json:"messages"`
	Temperature         *float64      `json:"temperature,omitempty"`
	TopP                *float64      `json:"top_p,omitempty"`
	MaxTokens           int           `json:"max_tokens,omitempty"`
	MaxCompletionTokens int           `json:"max_completion_tokens,omitempty"`
	Stop                []string      `json:"stop,omitempty"`
	Tools               []ArgoTool    `json:"tools,omitempty"`
	ToolChoice          interface{}   `json:"tool_choice,omitempty"`
}

// ArgoToolCall represents a tool call in the response
type ArgoToolCall struct {
	ID       string                 `json:"id,omitempty"`
	Type     string                 `json:"type,omitempty"`
	Name     string                 `json:"name,omitempty"`
	Function *ArgoFunctionCall      `json:"function,omitempty"`
	Input    map[string]interface{} `json:"input,omitempty"`
	Args     map[string]interface{} `json:"args,omitempty"`
}

// ArgoFunctionCall represents a function call (OpenAI format)
type ArgoFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ArgoResponseContent represents the response content structure
type ArgoResponseContent struct {
	Content   string         `json:"content"`
	ToolCalls []ArgoToolCall `json:"tool_calls,omitempty"`
}

// ArgoChatResponse represents a chat response from Argo API
type ArgoChatResponse struct {
	Response interface{} `json:"response"` // Can be string or ArgoResponseContent
}

// ArgoEmbedRequest represents an embed request to Argo API
type ArgoEmbedRequest struct {
	User   string   `json:"user"`
	Model  string   `json:"model"`
	Prompt []string `json:"prompt"`
}

// ArgoEmbedResponse represents an embed response from Argo API
type ArgoEmbedResponse struct {
	Embedding [][]float64 `json:"embedding"`
}
