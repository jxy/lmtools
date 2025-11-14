package proxy

import (
	"context"
	"lmtools/internal/constants"
)

// SplitMode defines the type of content being split
type SplitMode int

const (
	// TextMode for plain text splitting
	TextMode SplitMode = iota
	// JSONMode for JSON content splitting
	JSONMode
)

// ContentSplitter provides unified content splitting for streaming
type ContentSplitter struct {
	chunkSize int
	mode      SplitMode
	ctx       context.Context
}

// NewContentSplitter creates a new content splitter
func NewContentSplitter(ctx context.Context, mode SplitMode, chunkSize int) *ContentSplitter {
	if chunkSize <= 0 {
		switch mode {
		case JSONMode:
			chunkSize = constants.DefaultJSONChunkSize
		default:
			chunkSize = constants.DefaultTextChunkSize
		}
	}

	return &ContentSplitter{
		ctx:       ctx,
		mode:      mode,
		chunkSize: chunkSize,
	}
}

// Split splits content according to the configured mode
func (s *ContentSplitter) Split(content string) []string {
	if content == "" {
		return []string{}
	}

	switch s.mode {
	case JSONMode:
		return s.splitJSON(content)
	default:
		return s.splitText(content)
	}
}

// splitText handles text content splitting with UTF-8 boundary respect
func (s *ContentSplitter) splitText(text string) []string {
	// Delegate to existing implementation
	return splitTextForStreaming(s.ctx, text, s.chunkSize)
}

// splitJSON handles JSON content splitting with escape sequence preservation
func (s *ContentSplitter) splitJSON(jsonStr string) []string {
	// Delegate to existing implementation
	return splitJSONForStreaming(s.ctx, jsonStr, s.chunkSize)
}

// SetChunkSize updates the chunk size for splitting
func (s *ContentSplitter) SetChunkSize(size int) {
	if size > 0 {
		s.chunkSize = size
	}
}

// SetMode updates the splitting mode
func (s *ContentSplitter) SetMode(mode SplitMode) {
	s.mode = mode
}

// GetChunkSize returns the current chunk size
func (s *ContentSplitter) GetChunkSize() int {
	return s.chunkSize
}

// GetMode returns the current splitting mode
func (s *ContentSplitter) GetMode() SplitMode {
	return s.mode
}
