package proxy

// GoogleRequest represents a request to the Google AI API.
type GoogleRequest struct {
	Contents          []GoogleContent          `json:"contents"`
	SystemInstruction *GoogleSystemInstruction `json:"systemInstruction,omitempty"`
	Tools             []GoogleTool             `json:"tools,omitempty"`
	ToolConfig        *GoogleToolConfig        `json:"toolConfig,omitempty"`
	SafetySettings    []GoogleSafety           `json:"safetySettings,omitempty"`
	GenerationConfig  *GoogleGenConfig         `json:"generationConfig,omitempty"`
}

// GoogleSystemInstruction represents Google's out-of-band system prompt field.
type GoogleSystemInstruction struct {
	Parts []GooglePart `json:"parts"`
}

// GoogleContent represents content in Google AI format.
type GoogleContent struct {
	Role  string       `json:"role"`
	Parts []GooglePart `json:"parts"`
}

// GooglePart represents a part of content.
type GooglePart struct {
	Text         string              `json:"text,omitempty"`
	InlineData   *GoogleInlineData   `json:"inlineData,omitempty"`
	FunctionCall *GoogleFunctionCall `json:"functionCall,omitempty"`
	FunctionResp *GoogleFunctionResp `json:"functionResponse,omitempty"`
}

// GoogleInlineData represents inline data (e.g., images).
type GoogleInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// GoogleFunctionCall represents a function call.
type GoogleFunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

// GoogleFunctionResp represents a function response.
type GoogleFunctionResp struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

// GoogleTool represents a tool definition.
type GoogleTool struct {
	FunctionDeclarations []GoogleFunction `json:"functionDeclarations"`
}

// GoogleFunction represents a function declaration.
type GoogleFunction struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// GoogleFunctionDeclaration is an alias for GoogleFunction.
type GoogleFunctionDeclaration = GoogleFunction

// GoogleToolConfig represents tool configuration.
type GoogleToolConfig struct {
	FunctionCallingConfig GoogleFunctionConfig `json:"functionCallingConfig"`
}

// GoogleFunctionConfig represents function calling configuration.
type GoogleFunctionConfig struct {
	Mode                 string   `json:"mode"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

// GoogleSafety represents safety settings.
type GoogleSafety struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

// GoogleGenConfig represents generation configuration.
type GoogleGenConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	TopK            *int     `json:"topK,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

// GoogleGenerationConfig is an alias for GoogleGenConfig.
type GoogleGenerationConfig = GoogleGenConfig

// GoogleResponse represents a response from the Google AI API.
type GoogleResponse struct {
	Candidates     []GoogleCandidate     `json:"candidates"`
	UsageMetadata  *GoogleUsage          `json:"usageMetadata,omitempty"`
	PromptFeedback *GooglePromptFeedback `json:"promptFeedback,omitempty"`
}

// GoogleCandidate represents a response candidate.
type GoogleCandidate struct {
	Content       GoogleContent        `json:"content"`
	FinishReason  string               `json:"finishReason,omitempty"`
	Index         int                  `json:"index"`
	SafetyRatings []GoogleSafetyRating `json:"safetyRatings,omitempty"`
}

// GoogleUsage represents usage metadata.
type GoogleUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

// GooglePromptFeedback represents prompt feedback.
type GooglePromptFeedback struct {
	BlockReason   string               `json:"blockReason,omitempty"`
	SafetyRatings []GoogleSafetyRating `json:"safetyRatings,omitempty"`
}

// GoogleSafetyRating represents a safety rating.
type GoogleSafetyRating struct {
	Category    string `json:"category"`
	Probability string `json:"probability"`
}
