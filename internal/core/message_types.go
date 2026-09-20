package core

import "encoding/json"

// Block represents a content block in a message
type Block interface {
	isBlock()
}

// TextBlock represents a text content block
type TextBlock struct {
	Text string
}

func (TextBlock) isBlock() {}

// ToolUseBlock represents a tool use request block
type ToolUseBlock struct {
	ID           string
	Type         string
	Namespace    string
	OriginalName string
	Name         string
	Input        json.RawMessage
	InputString  string
}

func (ToolUseBlock) isBlock() {}

// ToolResultBlock represents a tool execution result block
type ToolResultBlock struct {
	ToolUseID string
	Type      string
	Namespace string
	Name      string // Function name (needed for Google's functionResponse)
	Content   string
	IsError   bool
	// Images ride the result beside its text, which is how view_image hands
	// the model the file it asked for. Each renderer places them in the shape
	// its wire accepts: Anthropic and Responses nest them in the result, Google
	// nests them under functionResponse.parts, and Chat Completions, whose tool
	// message is text only, sends them in a user message right after the
	// round's tool messages.
	Images []ImageBlock
}

func (ToolResultBlock) isBlock() {}

// ReasoningBlock represents provider reasoning artifacts that must be preserved
// unmodified across provider calls when required by the provider protocol.
type ReasoningBlock struct {
	Provider         string
	Type             string
	ID               string
	Status           string
	Text             string
	Summary          json.RawMessage
	Content          json.RawMessage
	Signature        string
	EncryptedContent string
	Raw              json.RawMessage
}

func (ReasoningBlock) isBlock() {}

// ImageBlock represents an image content block
type ImageBlock struct {
	URL    string
	Detail string // "auto", "low", or "high"
	// Name is the attached file's base name, kept for session display. No
	// renderer sends it.
	Name string
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

// UserMessageBlocks builds the canonical block layout of the user turn typed
// at the CLI: the attached images in the order given, then one text block when
// the prompt is non-empty. Every builder of that turn — the session request
// plan, the session commit, the in-memory store, and the no-session request
// builder — goes through here so the request the provider answers matches the
// message the session records. Images lead because Anthropic documents better
// results with the image ahead of the question, and the other providers do not
// care about the order.
func UserMessageBlocks(text string, images []ImageBlock) []Block {
	blocks := make([]Block, 0, len(images)+1)
	for _, image := range images {
		blocks = append(blocks, image)
	}
	if text != "" {
		blocks = append(blocks, TextBlock{Text: text})
	}
	return blocks
}

// NewUserMessage wraps UserMessageBlocks in a user TypedMessage.
func NewUserMessage(text string, images []ImageBlock) TypedMessage {
	return TypedMessage{
		Role:   string(RoleUser),
		Blocks: UserMessageBlocks(text, images),
	}
}
