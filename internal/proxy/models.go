package proxy

import (
	"encoding/json"
	"lmtools/internal/core"
)

// EstimateTokenCount estimates the number of tokens in a text string
// using a simple heuristic of dividing character count by 3.
//
// IMPORTANT: This function is ONLY needed for the Argo API provider,
// which does not return token counts in its responses. Other providers
// (OpenAI, Google, Anthropic) provide actual token counts in their
// responses and should use those instead of calling this function.
func EstimateTokenCount(text string) int {
	return len(text) / 3
}

// EstimateTokenCountFromChars estimates the number of tokens from character count
// using a simple heuristic of dividing character count by 3
func EstimateTokenCountFromChars(charCount int) int {
	return charCount / 3
}

// EstimateRequestTokens estimates the total input tokens for an Anthropic request
// This includes system message, conversation messages, and tool definitions
func EstimateRequestTokens(req *AnthropicRequest) int {
	totalChars := 0

	// Count system message
	if req.System != nil {
		// Try to extract text content from system message
		var systemContent string
		if err := json.Unmarshal(req.System, &systemContent); err == nil {
			totalChars += len(systemContent)
		} else {
			// If it's not a simple string, count the raw JSON
			totalChars += len(req.System)
		}
	}

	// Count messages
	for _, msg := range req.Messages {
		// Try to extract text content from message
		var content string
		if err := json.Unmarshal(msg.Content, &content); err == nil {
			totalChars += len(content)
		} else {
			// For complex content (arrays, objects), count the raw JSON
			totalChars += len(msg.Content)
		}
		// Add some overhead for role
		totalChars += len(string(msg.Role))
	}

	// Count tools
	for _, tool := range req.Tools {
		totalChars += len(tool.Name) + len(tool.Description)
		// Count the input schema
		if toolJSON, err := json.Marshal(tool.InputSchema); err == nil {
			totalChars += len(toolJSON)
		}
	}

	// Add some overhead for the overall request structure
	totalChars += 100 // Rough estimate for JSON structure overhead

	return EstimateTokenCountFromChars(totalChars)
}

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
	Thinking      *AnthropicThinking     `json:"thinking,omitempty"`
}

// AnthropicThinking represents the thinking configuration for Claude models
type AnthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// AnthropicMessage represents a message in the Anthropic format
type AnthropicMessage struct {
	Role    core.Role       `json:"role"`
	Content json.RawMessage `json:"content"`
}

// AnthropicContentBlock represents different types of content blocks
type AnthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
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
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
	// For tool result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// AnthropicTool represents a tool definition
type AnthropicTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"input_schema"`
}

// AnthropicToolChoice represents tool choice configuration
type AnthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// AnthropicResponse represents a response from the Anthropic Messages API
type AnthropicResponse struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       core.Role               `json:"role"`
	Content    []AnthropicContentBlock `json:"content"`
	Model      string                  `json:"model"`
	StopReason string                  `json:"stop_reason,omitempty"`
	Usage      *AnthropicUsage         `json:"usage,omitempty"`
}

// AnthropicUsage represents token usage information
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// AnthropicStreamEvent represents a server-sent event from the streaming API
type AnthropicStreamEvent struct {
	Type         string                 `json:"type"`
	Index        int                    `json:"index,omitempty"`
	Delta        *AnthropicContentBlock `json:"delta,omitempty"`
	ContentBlock *AnthropicContentBlock `json:"content_block,omitempty"`
	Message      *AnthropicResponse     `json:"message,omitempty"`
	Usage        *AnthropicUsage        `json:"usage,omitempty"`
}

// MessageStartEvent represents a message_start event
type MessageStartEvent struct {
	Type    string            `json:"type"`
	Message AnthropicResponse `json:"message"`
}

// ContentBlockStartEvent represents a content_block_start event
type ContentBlockStartEvent struct {
	Type         string                `json:"type"`
	Index        int                   `json:"index"`
	ContentBlock AnthropicContentBlock `json:"content_block"`
}

// ContentBlockDeltaEvent represents a content_block_delta event
type ContentBlockDeltaEvent struct {
	Type  string       `json:"type"`
	Index int          `json:"index"`
	Delta DeltaContent `json:"delta"`
}

// DeltaContent represents the delta content in a streaming event
type DeltaContent struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

// ContentBlockStopEvent represents a content_block_stop event
type ContentBlockStopEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

// MessageDeltaEvent represents a message_delta event
type MessageDeltaEvent struct {
	Type  string          `json:"type"`
	Delta MessageDelta    `json:"delta"`
	Usage *AnthropicUsage `json:"usage,omitempty"`
}

// MessageDelta represents the delta in a message_delta event
type MessageDelta struct {
	StopReason   string `json:"stop_reason,omitempty"`
	StopSequence string `json:"stop_sequence,omitempty"`
}

// MessageStopEvent represents a message_stop event
type MessageStopEvent struct {
	Type string `json:"type"`
}

// ErrorEvent represents an error event
type ErrorEvent struct {
	Type  string    `json:"type"`
	Error ErrorInfo `json:"error"`
}

// ErrorInfo represents error information
type ErrorInfo struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// AnthropicTokenCountRequest represents a token counting request
type AnthropicTokenCountRequest struct {
	Model    string             `json:"model"`
	System   json.RawMessage    `json:"system,omitempty"`
	Messages []AnthropicMessage `json:"messages"`
	Tools    []AnthropicTool    `json:"tools,omitempty"`
}

// AnthropicTokenCountResponse represents a token counting response
type AnthropicTokenCountResponse struct {
	InputTokens int `json:"input_tokens"`
}

// OpenAI API Models

// OpenAIRequest represents a request to the OpenAI Chat Completion API
type OpenAIRequest struct {
	Model            string          `json:"model"`
	Messages         []OpenAIMessage `json:"messages"`
	Temperature      *float64        `json:"temperature,omitempty"`
	TopP             *float64        `json:"top_p,omitempty"`
	N                *int            `json:"n,omitempty"`
	Stream           bool            `json:"stream,omitempty"`
	Stop             []string        `json:"stop,omitempty"`
	MaxTokens        *int            `json:"max_tokens,omitempty"`
	PresencePenalty  *float64        `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64        `json:"frequency_penalty,omitempty"`
	LogitBias        map[string]int  `json:"logit_bias,omitempty"`
	User             string          `json:"user,omitempty"`
	Tools            []OpenAITool    `json:"tools,omitempty"`
	ToolChoice       interface{}     `json:"tool_choice,omitempty"`
	ResponseFormat   *ResponseFormat `json:"response_format,omitempty"`
	ReasoningEffort  string          `json:"reasoning_effort,omitempty"`
}

// OpenAIMessage represents a message in the OpenAI format
type OpenAIMessage struct {
	Role       core.Role   `json:"role"`
	Content    interface{} `json:"content"` // Can be string or []OpenAIContent
	Name       string      `json:"name,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
}

// OpenAIContent represents content in multimodal messages
type OpenAIContent struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL represents an image URL in OpenAI format
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// OpenAITool represents a tool in OpenAI format
type OpenAITool struct {
	Type     string     `json:"type"`
	Function OpenAIFunc `json:"function"`
}

// OpenAIFunc represents a function definition
type OpenAIFunc struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// ToolCall represents a tool call in OpenAI format
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall represents a function call
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ResponseFormat specifies the format of the response
type ResponseFormat struct {
	Type string `json:"type"`
}

// OpenAIResponse represents a response from the OpenAI API
type OpenAIResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   *OpenAIUsage   `json:"usage,omitempty"`
}

// OpenAIChoice represents a choice in the response
type OpenAIChoice struct {
	Index        int           `json:"index"`
	Message      OpenAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason,omitempty"`
}

// OpenAIUsage represents token usage
type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// OpenAIStreamChunk represents a chunk in the streaming response
type OpenAIStreamChunk struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"`
	Created int64               `json:"created"`
	Model   string              `json:"model"`
	Choices []OpenAIStreamDelta `json:"choices"`
	Usage   *OpenAIUsage        `json:"usage,omitempty"`
}

// OpenAIStreamDelta represents a delta in streaming
type OpenAIStreamDelta struct {
	Index        int         `json:"index"`
	Delta        OpenAIDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason,omitempty"`
}

// OpenAIDelta represents the delta content
type OpenAIDelta struct {
	Role      *core.Role      `json:"role,omitempty"`
	Content   *string         `json:"content,omitempty"`
	ToolCalls []ToolCallDelta `json:"tool_calls,omitempty"`
}

// ToolCallDelta represents a tool call delta in streaming
type ToolCallDelta struct {
	Index    int                `json:"index"`
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function *FunctionCallDelta `json:"function,omitempty"`
}

// FunctionCallDelta represents a function call delta
type FunctionCallDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// Google AI API Models

// GoogleRequest represents a request to the Google AI API
type GoogleRequest struct {
	Contents         []GoogleContent   `json:"contents"`
	Tools            []GoogleTool      `json:"tools,omitempty"`
	ToolConfig       *GoogleToolConfig `json:"toolConfig,omitempty"`
	SafetySettings   []GoogleSafety    `json:"safetySettings,omitempty"`
	GenerationConfig *GoogleGenConfig  `json:"generationConfig,omitempty"`
}

// GoogleContent represents content in Google AI format
type GoogleContent struct {
	Role  string       `json:"role"`
	Parts []GooglePart `json:"parts"`
}

// GooglePart represents a part of content
type GooglePart struct {
	Text         string              `json:"text,omitempty"`
	InlineData   *GoogleInlineData   `json:"inlineData,omitempty"`
	FunctionCall *GoogleFunctionCall `json:"functionCall,omitempty"`
	FunctionResp *GoogleFunctionResp `json:"functionResponse,omitempty"`
}

// GoogleInlineData represents inline data (e.g., images)
type GoogleInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// GoogleFunctionCall represents a function call
type GoogleFunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

// GoogleFunctionResp represents a function response
type GoogleFunctionResp struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

// GoogleTool represents a tool definition
type GoogleTool struct {
	FunctionDeclarations []GoogleFunction `json:"functionDeclarations"`
}

// GoogleFunction represents a function declaration
type GoogleFunction struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// GoogleFunctionDeclaration is an alias for GoogleFunction
type GoogleFunctionDeclaration = GoogleFunction

// GoogleToolConfig represents tool configuration
type GoogleToolConfig struct {
	FunctionCallingConfig GoogleFunctionConfig `json:"functionCallingConfig"`
}

// GoogleFunctionConfig represents function calling configuration
type GoogleFunctionConfig struct {
	Mode                 string   `json:"mode"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

// GoogleSafety represents safety settings
type GoogleSafety struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

// GoogleGenConfig represents generation configuration
type GoogleGenConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	TopK            *int     `json:"topK,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

// GoogleGenerationConfig is an alias for GoogleGenConfig
type GoogleGenerationConfig = GoogleGenConfig

// GoogleResponse represents a response from the Google AI API
type GoogleResponse struct {
	Candidates     []GoogleCandidate     `json:"candidates"`
	UsageMetadata  *GoogleUsage          `json:"usageMetadata,omitempty"`
	PromptFeedback *GooglePromptFeedback `json:"promptFeedback,omitempty"`
}

// GoogleCandidate represents a response candidate
type GoogleCandidate struct {
	Content       GoogleContent        `json:"content"`
	FinishReason  string               `json:"finishReason,omitempty"`
	Index         int                  `json:"index"`
	SafetyRatings []GoogleSafetyRating `json:"safetyRatings,omitempty"`
}

// GoogleUsage represents usage metadata
type GoogleUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

// GooglePromptFeedback represents prompt feedback
type GooglePromptFeedback struct {
	BlockReason   string               `json:"blockReason,omitempty"`
	SafetyRatings []GoogleSafetyRating `json:"safetyRatings,omitempty"`
}

// GoogleSafetyRating represents a safety rating
type GoogleSafetyRating struct {
	Category    string `json:"category"`
	Probability string `json:"probability"`
}

// Argo API Models

// ArgoChatRequest represents a chat request to the Argo API
type ArgoChatRequest struct {
	User                string             `json:"user"`
	Model               string             `json:"model"`
	Messages            []ArgoMessage      `json:"messages"`
	Temperature         *float64           `json:"temperature,omitempty"`
	TopP                *float64           `json:"top_p,omitempty"`
	Stop                []string           `json:"stop,omitempty"`
	MaxTokens           int                `json:"max_tokens,omitempty"`
	MaxCompletionTokens int                `json:"max_completion_tokens,omitempty"`
	Tools               interface{}        `json:"tools,omitempty"`
	ToolChoice          interface{}        `json:"tool_choice,omitempty"`
	ReasoningEffort     string             `json:"reasoning_effort,omitempty"`
	Thinking            *AnthropicThinking `json:"thinking,omitempty"`
}

// ArgoMessage represents a message in Argo format
type ArgoMessage struct {
	Role       string      `json:"role"`
	Content    interface{} `json:"content"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
}

// ArgoChatResponse represents a chat response from Argo
type ArgoChatResponse struct {
	Response interface{} `json:"response"`
}

// ArgoEmbedRequest represents an embedding request to Argo
type ArgoEmbedRequest struct {
	User   string   `json:"user"`
	Model  string   `json:"model"`
	Prompt []string `json:"prompt"`
}

// ArgoEmbedResponse represents an embedding response from Argo
type ArgoEmbedResponse struct {
	Embedding [][]float64 `json:"embedding"`
}

// ArgoTool represents a tool in Argo format
type ArgoTool struct {
	Type     string   `json:"type"`
	Function ArgoFunc `json:"function"`
}

// ArgoFunc represents a function definition in Argo format
type ArgoFunc struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}
