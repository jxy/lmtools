package argo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

func HandleResponse(ctx context.Context, cfg Config, resp *http.Response) (string, error) {
	// Validate HTTP status first
	if resp.StatusCode != http.StatusOK {
		// Read limited body for error message
		limitedBody := io.LimitReader(resp.Body, 1024) // 1KB limit
		errorData, err := io.ReadAll(limitedBody)
		if err != nil {
			Debugf("failed to read error response body: %v", err)
			errorData = []byte("failed to read error response")
		}
		return "", &HTTPError{
			StatusCode: resp.StatusCode,
			Body:       string(errorData),
		}
	}

	if cfg.Embed {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("failed to read response body: %w", err)
		}
		if err := LogJSON(cfg.LogDir, "embed_output", data); err != nil {
			return "", fmt.Errorf("failed to log embed response: %w", err)
		}
		var embedResp struct {
			Embedding string `json:"embedding"`
		}
		if err := json.Unmarshal(data, &embedResp); err != nil {
			// Don't include full data in error to avoid memory issues with large responses
			dataPreview := string(data)
			if len(dataPreview) > 100 {
				dataPreview = dataPreview[:100] + "..."
			}
			return "", fmt.Errorf("failed to unmarshal embed response (preview: %s): %w", dataPreview, err)
		}
		return embedResp.Embedding, nil
	}

	if cfg.StreamChat {
		f, filename, err := CreateTimestampedFile(cfg.LogDir, "stream_chat_output", "log")
		if err != nil {
			return "", fmt.Errorf("failed to create log file: %w", err)
		}
		defer func() {
			if closeErr := f.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "failed to close log file %s: %v\n", filename, closeErr)
			}
		}()

		// Use context-aware copy to handle interrupts
		done := make(chan error, 1)
		go func() {
			_, err := io.Copy(io.MultiWriter(os.Stdout, f), resp.Body)
			done <- err
		}()

		select {
		case <-ctx.Done():
			return "", fmt.Errorf("streaming interrupted: %w", ctx.Err())
		case err := <-done:
			if err != nil {
				return "", fmt.Errorf("error streaming response to stdout and log file %s: %w", filename, err)
			}
			return "", nil
		}
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}
	if err := LogJSON(cfg.LogDir, "chat_output", data); err != nil {
		return "", fmt.Errorf("failed to log chat response: %w", err)
	}
	var chatResp struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(data, &chatResp); err != nil {
		// Don't include full data in error to avoid memory issues with large responses
		dataPreview := string(data)
		if len(dataPreview) > 100 {
			dataPreview = dataPreview[:100] + "..."
		}
		return "", fmt.Errorf("failed to unmarshal chat response (preview: %s): %w", dataPreview, err)
	}
	return chatResp.Response, nil
}
