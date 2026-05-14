package proxy

import (
	"encoding/json"
	"lmtools/internal/core"
)

// OpenAIResponsesRequest represents an OpenAI /v1/responses request.
type OpenAIResponsesRequest struct {
	Model              string                    `json:"model"`
	Input              interface{}               `json:"input,omitempty"`
	Instructions       interface{}               `json:"instructions,omitempty"`
	Background         *bool                     `json:"background,omitempty"`
	Conversation       interface{}               `json:"conversation,omitempty"`
	Tools              []map[string]interface{}  `json:"tools,omitempty"`
	ToolChoice         interface{}               `json:"tool_choice,omitempty"`
	Text               *OpenAIResponsesText      `json:"text,omitempty"`
	Reasoning          *OpenAIResponsesReasoning `json:"reasoning,omitempty"`
	MaxOutputTokens    *int                      `json:"max_output_tokens,omitempty"`
	MaxToolCalls       *int                      `json:"max_tool_calls,omitempty"`
	Temperature        *float64                  `json:"temperature,omitempty"`
	TopP               *float64                  `json:"top_p,omitempty"`
	Stream             bool                      `json:"stream,omitempty"`
	StreamOptions      *OpenAIStreamOptions      `json:"stream_options,omitempty"`
	Store              *bool                     `json:"store,omitempty"`
	Metadata           map[string]interface{}    `json:"metadata,omitempty"`
	ServiceTier        string                    `json:"service_tier,omitempty"`
	User               string                    `json:"user,omitempty"`
	SafetyIdentifier   string                    `json:"safety_identifier,omitempty"`
	Prompt             interface{}               `json:"prompt,omitempty"`
	PromptCacheKey     string                    `json:"prompt_cache_key,omitempty"`
	PreviousResponseID string                    `json:"previous_response_id,omitempty"`
	Include            []string                  `json:"include,omitempty"`
	ParallelToolCalls  *bool                     `json:"parallel_tool_calls,omitempty"`
	Truncation         string                    `json:"truncation,omitempty"`
	TopLogprobs        *int                      `json:"top_logprobs,omitempty"`
}

type OpenAIResponsesText struct {
	Format    map[string]interface{} `json:"format,omitempty"`
	Verbosity string                 `json:"verbosity,omitempty"`
}

type OpenAIResponsesReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type OpenAIResponsesConversation struct {
	ID string `json:"id"`
}

// OpenAIResponsesResponse represents an OpenAI /v1/responses response.
type OpenAIResponsesResponse struct {
	ID                string                       `json:"id"`
	Object            string                       `json:"object,omitempty"`
	CreatedAt         int64                        `json:"created_at,omitempty"`
	Status            string                       `json:"status,omitempty"`
	Model             string                       `json:"model"`
	Conversation      *OpenAIResponsesConversation `json:"conversation,omitempty"`
	Output            []OpenAIResponsesOutputItem  `json:"output"`
	OutputText        string                       `json:"output_text,omitempty"`
	Usage             *OpenAIResponsesUsage        `json:"usage,omitempty"`
	Error             interface{}                  `json:"error,omitempty"`
	IncompleteDetails interface{}                  `json:"incomplete_details,omitempty"`
	ParallelToolCalls *bool                        `json:"parallel_tool_calls,omitempty"`
	ServiceTier       string                       `json:"service_tier,omitempty"`
}

// OpenAIResponsesInputTokensResponse represents /v1/responses/input_tokens.
type OpenAIResponsesInputTokensResponse struct {
	Object      string `json:"object"`
	InputTokens int    `json:"input_tokens"`
}

// OpenAIResponsesCompactionResponse represents /v1/responses/compact.
type OpenAIResponsesCompactionResponse struct {
	ID        string                      `json:"id"`
	Object    string                      `json:"object"`
	CreatedAt int64                       `json:"created_at"`
	Output    []OpenAIResponsesOutputItem `json:"output"`
	Usage     *OpenAIResponsesUsage       `json:"usage,omitempty"`
}

func (r OpenAIResponsesResponse) MarshalJSON() ([]byte, error) {
	type alias OpenAIResponsesResponse
	data, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	delete(obj, "output_text")
	return json.Marshal(obj)
}

type OpenAIResponsesOutputItem struct {
	ID               string                       `json:"id,omitempty"`
	Type             string                       `json:"type"`
	Status           string                       `json:"status,omitempty"`
	Role             core.Role                    `json:"role,omitempty"`
	Content          []OpenAIResponsesContentPart `json:"content,omitempty"`
	CallID           string                       `json:"call_id,omitempty"`
	Namespace        string                       `json:"namespace,omitempty"`
	Name             string                       `json:"name,omitempty"`
	Arguments        string                       `json:"arguments,omitempty"`
	Input            string                       `json:"input,omitempty"`
	Summary          []interface{}                `json:"summary,omitempty"`
	EncryptedContent string                       `json:"encrypted_content,omitempty"`
}

type OpenAIResponsesContentPart struct {
	Type        string        `json:"type"`
	Text        string        `json:"text,omitempty"`
	Annotations []interface{} `json:"annotations,omitempty"`
}

type OpenAIResponsesUsage struct {
	InputTokens         int                           `json:"input_tokens,omitempty"`
	OutputTokens        int                           `json:"output_tokens,omitempty"`
	TotalTokens         int                           `json:"total_tokens,omitempty"`
	InputTokensDetails  *OpenAIResponsesInputDetails  `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *OpenAIResponsesOutputDetails `json:"output_tokens_details,omitempty"`
}

type OpenAIResponsesInputDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
}

type OpenAIResponsesOutputDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

func (u *OpenAIResponsesUsage) toOpenAIUsage() *OpenAIUsage {
	if u == nil {
		return nil
	}
	usage := &OpenAIUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
	}
	if u.InputTokensDetails != nil {
		usage.PromptTokensDetails = &OpenAITokenDetails{CachedTokens: u.InputTokensDetails.CachedTokens}
	}
	if u.OutputTokensDetails != nil {
		usage.CompletionTokensDetails = &OpenAICompletionTokenDetails{ReasoningTokens: u.OutputTokensDetails.ReasoningTokens}
	}
	return usage
}

func openAIUsageToResponsesUsage(usage *OpenAIUsage) *OpenAIResponsesUsage {
	if usage == nil {
		return nil
	}
	result := &OpenAIResponsesUsage{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		TotalTokens:  usage.TotalTokens,
	}
	if usage.PromptTokensDetails != nil {
		result.InputTokensDetails = &OpenAIResponsesInputDetails{CachedTokens: usage.PromptTokensDetails.CachedTokens}
	}
	if usage.CompletionTokensDetails != nil {
		result.OutputTokensDetails = &OpenAIResponsesOutputDetails{ReasoningTokens: usage.CompletionTokensDetails.ReasoningTokens}
	}
	return result
}
