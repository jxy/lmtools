package core

import (
	"encoding/json"
)

// Block represents a content block in a message
type Block interface {
	isBlock()
}

// TextBlock represents a text content block
type TextBlock struct {
	Text             string
	ThoughtSignature string
}

func (TextBlock) isBlock() {}

// ToolUseBlock represents a tool use request block
type ToolUseBlock struct {
	ID               string
	Name             string
	Input            json.RawMessage
	ThoughtSignature string
}

func (ToolUseBlock) isBlock() {}

// ToolResultBlock represents a tool execution result block
type ToolResultBlock struct {
	ToolUseID string
	Name      string // Function name (needed for Google's functionResponse)
	Content   string
	IsError   bool
}

func (ToolResultBlock) isBlock() {}

// ThinkingBlock represents Anthropic reasoning content that must be preserved
// for providers that support it, while being dropped by providers that do not.
type ThinkingBlock struct {
	Thinking  string
	Signature string
}

func (ThinkingBlock) isBlock() {}

// ImageBlock represents an image content block
type ImageBlock struct {
	URL    string
	Detail string // "auto", "low", or "high"
}

func (ImageBlock) isBlock() {}

// AudioBlock represents an audio content block
type AudioBlock struct {
	ID       string // Audio ID for input_audio
	Data     string // Base64 encoded audio data (optional)
	Format   string // Audio format like "wav", "mp3" (optional)
	URL      string // URL to audio file (optional)
	Duration int    // Duration in seconds (optional)
}

func (AudioBlock) isBlock() {}

// FileBlock represents a file content block
type FileBlock struct {
	FileID string // File ID for file inputs
}

func (FileBlock) isBlock() {}

// TypedMessage represents a message in a conversation with typed blocks
type TypedMessage struct {
	Role   string  // "system", "user", or "assistant"
	Blocks []Block // Content blocks (text, tool use, tool results)
}

// NewTextMessage creates a TypedMessage with a single text block
func NewTextMessage(role, text string) TypedMessage {
	return TypedMessage{
		Role:   role,
		Blocks: []Block{TextBlock{Text: text}},
	}
}
