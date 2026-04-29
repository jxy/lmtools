package core

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
