package models

import (
	"encoding/json"
	"strings"
)

// Common role types for chat messages
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Default models
const (
	DefaultChatModel  = "gemini25pro"
	DefaultEmbedModel = "v3large"
)

// API endpoints
const (
	ArgoProdURL = "https://apps.inside.anl.gov/argoapi/api/v1/resource"
	ArgoDevURL  = "https://apps-dev.inside.anl.gov/argoapi/api/v1/resource"
)

// Input validation constants
const (
	MaxInputSizeBytes = 1024 * 1024 // 1MB
)

// Supported chat models
var ChatModels = []string{
	"gpt35",
	"gpt35large",
	"gpt4",
	"gpt4large",
	"gpt4turbo",
	"gpt4o",
	"gpt4olatest",
	"gpto1mini",
	"gpto3mini",
	"gpto1",
	"gpto3",
	"gpto4mini",
	"gpt41",
	"gpt41mini",
	"gpt41nano",
	"gemini25pro",
	"gemini25flash",
	"claudeopus4",
	"claudesonnet4",
	"claudesonnet37",
	"claudesonnet35v2",
}

// Supported embedding models
var EmbedModels = []string{
	"v3small",
	"v3large",
}

// IsValidChatModel checks if a model is valid for chat
func IsValidChatModel(model string) bool {
	for _, m := range ChatModels {
		if m == model {
			return true
		}
	}
	return false
}

// IsValidEmbedModel checks if a model is valid for embedding
func IsValidEmbedModel(model string) bool {
	for _, m := range EmbedModels {
		if m == model {
			return true
		}
	}
	return false
}

// NormalizeModel normalizes model names for consistency
func NormalizeModel(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

// Common request/response types

// ChatMessage represents a message in a chat conversation
type ChatMessage struct {
	Role    Role            `json:"role"`
	Content json.RawMessage `json:"content"`
}

// ChatRequest represents a generic chat request
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
}

// ChatResponse represents a generic chat response
type ChatResponse struct {
	ID      string `json:"id,omitempty"`
	Model   string `json:"model,omitempty"`
	Content string `json:"content,omitempty"`
	Usage   *Usage `json:"usage,omitempty"`
}

// EmbedRequest represents a generic embedding request
type EmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// EmbedResponse represents a generic embedding response
type EmbedResponse struct {
	Model      string      `json:"model,omitempty"`
	Embeddings [][]float64 `json:"embeddings"`
	Usage      *Usage      `json:"usage,omitempty"`
}

// Usage represents token usage information
type Usage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

// StreamChunk represents a chunk in a streaming response
type StreamChunk struct {
	ID      string `json:"id,omitempty"`
	Model   string `json:"model,omitempty"`
	Content string `json:"content,omitempty"`
	Done    bool   `json:"done,omitempty"`
}
