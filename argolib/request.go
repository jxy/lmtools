package argo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type EmbedRequest struct {
	User   string   `json:"user"`
	Model  string   `json:"model"`
	Prompt []string `json:"prompt"`
}

type ChatRequest struct {
	User     string    `json:"user"`
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func BuildRequest(cfg Config, input string) (*http.Request, []byte, error) {
	urlBase := GetBaseURL(cfg.Env)

	model := cfg.Model
	var (
		body     []byte
		endpoint string
		err      error
	)
	if cfg.Embed {
		if model == "" {
			model = DefaultEmbedModel
		}
		// Validate embed model
		if err := ValidateEmbedModel(model); err != nil {
			return nil, nil, err
		}
		req := EmbedRequest{User: cfg.User, Model: model, Prompt: []string{input}}
		if body, err = json.Marshal(req); err != nil {
			return nil, nil, fmt.Errorf("failed to marshal embed request: %w", err)
		}
		endpoint = fmt.Sprintf("%s/embed/", urlBase)
	} else {
		if model == "" {
			model = DefaultChatModel
		}
		// Validate chat model
		if err := ValidateChatModel(model); err != nil {
			return nil, nil, err
		}
		if cfg.PromptChat {
			req := EmbedRequest{User: cfg.User, Model: model, Prompt: []string{input}}
			if body, err = json.Marshal(req); err != nil {
				return nil, nil, fmt.Errorf("failed to marshal prompt chat request: %w", err)
			}
		} else {
			req := ChatRequest{
				User:  cfg.User,
				Model: model,
				Messages: []Message{
					{Role: "system", Content: cfg.System},
					{Role: "user", Content: input},
				},
			}
			if body, err = json.Marshal(req); err != nil {
				return nil, nil, fmt.Errorf("failed to marshal chat request: %w", err)
			}
		}
		if cfg.StreamChat {
			endpoint = fmt.Sprintf("%s/streamchat/", urlBase)
		} else {
			endpoint = fmt.Sprintf("%s/chat/", urlBase)
		}
	}

	httpReq, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/plain")
	httpReq.Header.Set("Accept-Encoding", "identity")
	return httpReq, body, nil
}

// MaxReplayableBodySize is the maximum size for automatic body buffering in NewReplayableRequest
const MaxReplayableBodySize = 10 * 1024 * 1024 // 10MB

// NewReplayableRequest creates an HTTP request with automatic GetBody support for retries.
// It automatically sets GetBody when the body is one of:
// - *bytes.Reader (size limited to MaxReplayableBodySize)
// - *bytes.Buffer (size limited to MaxReplayableBodySize)
// - *strings.Reader (size limited to MaxReplayableBodySize)
// For other body types, it will buffer up to MaxReplayableBodySize bytes.
// Returns an error if the body exceeds MaxReplayableBodySize (10MB).
//
// Note: The provided reader should not be used concurrently after calling this function,
// as it may be read from to extract the body data. For native reader types (*bytes.Reader,
// *bytes.Buffer, *strings.Reader), the entire content is copied into memory.
func NewReplayableRequest(method, url string, body io.Reader) (*http.Request, error) {
	// Handle nil body
	if body == nil {
		return http.NewRequest(method, url, nil)
	}

	// Check if body is already replayable
	switch v := body.(type) {
	case *bytes.Reader:
		// Save current position before any operations
		origPos, err := v.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, fmt.Errorf("failed to get current position: %w", err)
		}
		// Get the total size (not remaining bytes)
		// We need to seek to end to get total size, then restore position
		totalSize, err := v.Seek(0, io.SeekEnd)
		if err != nil {
			return nil, fmt.Errorf("failed to seek to end: %w", err)
		}
		if _, err := v.Seek(origPos, io.SeekStart); err != nil {
			return nil, fmt.Errorf("failed to restore position: %w", err)
		}
		// Check size limit
		if totalSize > MaxReplayableBodySize {
			return nil, fmt.Errorf("bytes.Reader body too large for replay: %d bytes exceeds %d bytes limit", totalSize, MaxReplayableBodySize)
		}
		buf := make([]byte, totalSize)
		if _, err := v.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("failed to seek to start: %w", err)
		}
		_, err = io.ReadFull(v, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("failed to read bytes.Reader: %w", err)
		}
		// Restore original position
		if _, err := v.Seek(origPos, io.SeekStart); err != nil {
			return nil, fmt.Errorf("failed to restore position after read: %w", err)
		}
		req, err := http.NewRequest(method, url, bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		req.ContentLength = int64(len(buf))
		// Create new reader for each retry to avoid concurrent access
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(buf)), nil
		}
		return req, nil

	case *bytes.Buffer:
		// Get a copy of the buffer's bytes
		bodyBytes := v.Bytes()
		// Check size limit
		if len(bodyBytes) > MaxReplayableBodySize {
			return nil, fmt.Errorf("bytes.Buffer body too large for replay: %d bytes exceeds %d bytes limit", len(bodyBytes), MaxReplayableBodySize)
		}
		req, err := http.NewRequest(method, url, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req.ContentLength = int64(len(bodyBytes))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
		return req, nil

	case *strings.Reader:
		// Save current position before any operations
		origPos, err := v.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, fmt.Errorf("failed to get current position: %w", err)
		}
		// Get the total size (not remaining bytes)
		// We need to seek to end to get total size, then restore position
		totalSize, err := v.Seek(0, io.SeekEnd)
		if err != nil {
			return nil, fmt.Errorf("failed to seek to end: %w", err)
		}
		if _, err := v.Seek(origPos, io.SeekStart); err != nil {
			return nil, fmt.Errorf("failed to restore position: %w", err)
		}
		// Check size limit
		if totalSize > MaxReplayableBodySize {
			return nil, fmt.Errorf("strings.Reader body too large for replay: %d bytes exceeds %d bytes limit", totalSize, MaxReplayableBodySize)
		}
		// Note: This doubles memory usage for large strings (up to 10MB)
		// but is necessary since strings.Reader doesn't expose the underlying string
		buf := make([]byte, totalSize)
		if _, err := v.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("failed to seek to start: %w", err)
		}
		_, err = io.ReadFull(v, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("failed to read strings.Reader: %w", err)
		}
		// Restore original position
		if _, err := v.Seek(origPos, io.SeekStart); err != nil {
			return nil, fmt.Errorf("failed to restore position after read: %w", err)
		}
		str := string(buf)
		req, err := http.NewRequest(method, url, strings.NewReader(str))
		if err != nil {
			return nil, err
		}
		req.ContentLength = int64(len(str))
		// Create new reader for each retry to avoid concurrent access
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(str)), nil
		}
		return req, nil

	default:
		// For other body types, buffer up to MaxReplayableBodySize
		limitReader := io.LimitReader(body, MaxReplayableBodySize+1)
		buf := new(bytes.Buffer)
		n, err := io.Copy(buf, limitReader)
		if err != nil {
			return nil, fmt.Errorf("failed to buffer request body: %w", err)
		}
		// Check if body exceeds the limit
		if n > MaxReplayableBodySize {
			return nil, fmt.Errorf("request body too large for automatic buffering: exceeds %d bytes", MaxReplayableBodySize)
		}

		bodyBytes := buf.Bytes()
		req, err := http.NewRequest(method, url, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req.ContentLength = int64(len(bodyBytes))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
		return req, nil
	}
}
