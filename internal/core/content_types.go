package core

import (
	"encoding/json"
	"fmt"
	"lmtools/internal/logger"
)

// ARCHITECTURAL NOTE: These concrete types replace interface{} usage throughout the codebase.
// They provide compile-time type safety and make the code more maintainable.
// All content types implement clear interfaces and have well-defined structures.

// AudioData represents audio content with specific fields
type AudioData struct {
	ID       string `json:"id,omitempty"`       // Reference ID for audio (e.g., OpenAI input_audio id)
	Format   string `json:"format,omitempty"`   // e.g., "wav", "mp3"
	Data     string `json:"data,omitempty"`     // Base64 encoded audio
	URL      string `json:"url,omitempty"`      // URL to audio file
	Duration int    `json:"duration,omitempty"` // Duration in seconds
}

// FileData represents file content with specific fields
type FileData struct {
	FileID   string `json:"file_id,omitempty"`   // OpenAI file ID
	Name     string `json:"name,omitempty"`      // File name
	MimeType string `json:"mime_type,omitempty"` // MIME type
	Data     string `json:"data,omitempty"`      // Base64 encoded content
	URL      string `json:"url,omitempty"`       // URL to file
	Size     int64  `json:"size,omitempty"`      // File size in bytes
}

// ImageData represents image content with specific fields
type ImageData struct {
	URL      string `json:"url,omitempty"`       // Image URL
	Data     string `json:"data,omitempty"`      // Base64 encoded image
	MimeType string `json:"mime_type,omitempty"` // e.g., "image/png"
	Detail   string `json:"detail,omitempty"`    // "auto", "low", or "high" for OpenAI
	Width    int    `json:"width,omitempty"`     // Image width in pixels
	Height   int    `json:"height,omitempty"`    // Image height in pixels
}

// ToolInputData represents tool input parameters
type ToolInputData struct {
	Parameters json.RawMessage `json:"parameters"` // Tool-specific parameters as JSON
}

// ToolResultData represents tool execution results
type ToolResultData struct {
	Output   string `json:"output"`              // Tool output
	Error    bool   `json:"error,omitempty"`     // Whether the tool execution failed
	ErrorMsg string `json:"error_msg,omitempty"` // Error message if failed
}

// ContentType represents the type of content
type ContentType string

const (
	ContentTypeText       ContentType = "text"
	ContentTypeImage      ContentType = "image"
	ContentTypeAudio      ContentType = "audio"
	ContentTypeFile       ContentType = "file"
	ContentTypeToolUse    ContentType = "tool_use"
	ContentTypeToolResult ContentType = "tool_result"
)

// Content interface that all content types must implement
type Content interface {
	GetType() ContentType
	GetText() string // Returns text representation or empty string
}

// TextContent represents text content
type TextContent struct {
	Type ContentType `json:"type"`
	Text string      `json:"text"`
}

func (t TextContent) GetType() ContentType { return ContentTypeText }
func (t TextContent) GetText() string      { return t.Text }

// ImageContent represents image content
type ImageContent struct {
	Type  ContentType `json:"type"`
	Image ImageData   `json:"image"`
}

func (i ImageContent) GetType() ContentType { return ContentTypeImage }
func (i ImageContent) GetText() string      { return "" }

// AudioContent represents audio content
type AudioContent struct {
	Type  ContentType `json:"type"`
	Audio AudioData   `json:"audio"`
}

func (a AudioContent) GetType() ContentType { return ContentTypeAudio }
func (a AudioContent) GetText() string      { return "" }

// FileContent represents file content
type FileContent struct {
	Type ContentType `json:"type"`
	File FileData    `json:"file"`
}

func (f FileContent) GetType() ContentType { return ContentTypeFile }
func (f FileContent) GetText() string      { return "" }

// ToolUseContent represents a tool use request
type ToolUseContent struct {
	Type  ContentType   `json:"type"`
	ID    string        `json:"id"`
	Name  string        `json:"name"`
	Input ToolInputData `json:"input"`
}

func (t ToolUseContent) GetType() ContentType { return ContentTypeToolUse }
func (t ToolUseContent) GetText() string      { return "" }

// ToolResultContent represents a tool execution result
type ToolResultContent struct {
	Type      ContentType    `json:"type"`
	ToolUseID string         `json:"tool_use_id"`
	Result    ToolResultData `json:"result"`
}

func (t ToolResultContent) GetType() ContentType { return ContentTypeToolResult }
func (t ToolResultContent) GetText() string      { return t.Result.Output }

// logUnknownType logs unknown content types
func logUnknownType(input interface{}) {
	logger.GetLogger().Debugf("ConvertToContent: unknown input type %T, defaulting to empty text", input)
}

// ConvertToContent converts various content representations to typed Content
func ConvertToContent(input interface{}) Content {
	switch v := input.(type) {
	case string:
		return TextContent{Type: ContentTypeText, Text: v}
	case TextContent:
		return v
	case ImageContent:
		return v
	case AudioContent:
		return v
	case FileContent:
		return v
	case ToolUseContent:
		return v
	case ToolResultContent:
		return v
	case map[string]interface{}:
		// Handle map representations
		typeStr, _ := v["type"].(string)
		switch ContentType(typeStr) {
		case ContentTypeText:
			text, _ := v["text"].(string)
			return TextContent{Type: ContentTypeText, Text: text}
		case ContentTypeImage:
			return convertMapToImageContent(v)
		case ContentTypeAudio:
			return convertMapToAudioContent(v)
		case ContentTypeFile:
			return convertMapToFileContent(v)
		case ContentTypeToolUse:
			return convertMapToToolUseContent(v)
		case ContentTypeToolResult:
			return convertMapToToolResultContent(v)
		default:
			// Log unknown map type and default to text content
			if typeStr != "" {
				logUnknownType(fmt.Sprintf("map with type=%s", typeStr))
			} else {
				logUnknownType("map without type field")
			}
			return TextContent{Type: ContentTypeText, Text: ""}
		}
	default:
		// Log unknown type and default to empty text content
		logUnknownType(input)
		return TextContent{Type: ContentTypeText, Text: ""}
	}
}

func convertMapToImageContent(m map[string]interface{}) ImageContent {
	content := ImageContent{Type: ContentTypeImage}

	if imageData, ok := m["image"].(map[string]interface{}); ok {
		content.Image = ImageData{
			URL:      GetString(imageData, "url"),
			Data:     GetString(imageData, "data"),
			MimeType: GetString(imageData, "mime_type"),
			Detail:   GetString(imageData, "detail"),
			Width:    GetInt(imageData, "width"),
			Height:   GetInt(imageData, "height"),
		}
		return content
	}
	if sourceData, ok := m["source"].(map[string]interface{}); ok {
		content.Image = ImageData{
			URL:      GetString(sourceData, "url"),
			Data:     GetString(sourceData, "data"),
			MimeType: GetString(sourceData, "media_type"),
		}
		return content
	}
	if imageURL, ok := m["image_url"].(map[string]interface{}); ok {
		content.Image = ImageData{
			URL:    GetString(imageURL, "url"),
			Detail: GetString(imageURL, "detail"),
		}
		return content
	}

	return content
}

func convertMapToAudioContent(m map[string]interface{}) AudioContent {
	content := AudioContent{Type: ContentTypeAudio}

	if audioData, ok := m["audio"].(map[string]interface{}); ok {
		content.Audio = AudioData{
			ID:       GetString(audioData, "id"),
			Format:   GetString(audioData, "format"),
			Data:     GetString(audioData, "data"),
			URL:      GetString(audioData, "url"),
			Duration: GetInt(audioData, "duration"),
		}
		// Ensure audio format defaults to "wav" if not specified
		ensureAudioFormat(&content.Audio)
		return content
	}
	if inputAudio, ok := m["input_audio"].(map[string]interface{}); ok {
		content.Audio = AudioData{
			ID:       GetString(inputAudio, "id"),
			Format:   GetString(inputAudio, "format"),
			Data:     GetString(inputAudio, "data"),
			URL:      GetString(inputAudio, "url"),
			Duration: GetInt(inputAudio, "duration"),
		}
		// Ensure audio format defaults to "wav" if not specified
		ensureAudioFormat(&content.Audio)
		return content
	}

	return content
}

func convertMapToFileContent(m map[string]interface{}) FileContent {
	content := FileContent{Type: ContentTypeFile}

	if fileData, ok := m["file"].(map[string]interface{}); ok {
		content.File = FileData{
			FileID:   GetString(fileData, "file_id"),
			Name:     GetString(fileData, "name"),
			MimeType: GetString(fileData, "mime_type"),
			Data:     GetString(fileData, "data"),
			URL:      GetString(fileData, "url"),
			Size:     GetInt64(fileData, "size"),
		}
		return content
	}
	return content
}

func convertMapToToolUseContent(m map[string]interface{}) ToolUseContent {
	content := ToolUseContent{
		Type: ContentTypeToolUse,
		ID:   GetString(m, "id"),
		Name: GetString(m, "name"),
	}

	if input, ok := m["input"]; ok {
		if inputBytes, err := json.Marshal(input); err == nil {
			content.Input = ToolInputData{Parameters: json.RawMessage(inputBytes)}
		}
	}

	return content
}

func convertMapToToolResultContent(m map[string]interface{}) ToolResultContent {
	content := ToolResultContent{
		Type:      ContentTypeToolResult,
		ToolUseID: GetString(m, "tool_use_id"),
	}

	// Handle different result formats
	if result, ok := m["result"].(map[string]interface{}); ok {
		content.Result = ToolResultData{
			Output:   GetString(result, "output"),
			Error:    GetBool(result, "error"),
			ErrorMsg: GetString(result, "error_msg"),
		}
	} else if contentStr := GetString(m, "content"); contentStr != "" {
		// Handle Anthropic format
		content.Result = ToolResultData{
			Output: contentStr,
			Error:  GetBool(m, "is_error"),
		}
	}

	return content
}
