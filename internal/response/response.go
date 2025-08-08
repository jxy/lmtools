package response

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lmtools/internal/config"
	"lmtools/internal/errors"
	"lmtools/internal/logger"
	"net/http"
	"os"
)

func HandleResponse(ctx context.Context, cfg config.Config, resp *http.Response) (string, error) {
	defer resp.Body.Close()

	// Validate HTTP status first
	if resp.StatusCode != http.StatusOK {
		// Read limited body for error message
		limitedBody := io.LimitReader(resp.Body, 1024) // 1KB limit
		errorData, err := io.ReadAll(limitedBody)
		if err != nil {
			errorData = []byte("failed to read error response")
		}
		return "", errors.NewHTTPError(resp.StatusCode, string(errorData))
	}

	if cfg.Embed {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("failed to read response body: %w", err)
		}
		if err := logger.LogJSON(logger.GetLogDir(), "embed_output", data); err != nil {
			return "", fmt.Errorf("failed to log embed response: %w", err)
		}
		var embedResp struct {
			Embedding [][]float64 `json:"embedding"`
		}
		if err := json.Unmarshal(data, &embedResp); err != nil {
			// Don't include full data in error to avoid memory issues with large responses
			dataPreview := string(data)
			if len(dataPreview) > 100 {
				dataPreview = dataPreview[:100] + "..."
			}
			return "", fmt.Errorf("failed to unmarshal embed response (preview: %s): %w", dataPreview, err)
		}
		// Convert the embedding array to a string representation
		if len(embedResp.Embedding) == 0 {
			return "", fmt.Errorf("empty embedding response")
		}
		// Check if the first embedding vector is empty
		if len(embedResp.Embedding[0]) == 0 {
			return "[]", nil
		}
		// Marshal the first embedding vector to JSON string
		embeddingJSON, err := json.Marshal(embedResp.Embedding[0])
		if err != nil {
			return "", fmt.Errorf("failed to marshal embedding: %w", err)
		}
		return string(embeddingJSON), nil
	}

	if cfg.StreamChat {
		f, path, err := logger.CreateLogFile(logger.GetLogDir(), "stream_chat_output")
		if err != nil {
			return "", fmt.Errorf("failed to create log file: %w", err)
		}
		defer func() {
			if closeErr := f.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "failed to close log file %s: %v\n", path, closeErr)
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
				return "", fmt.Errorf("error streaming response to stdout and log file %s: %w", path, err)
			}
			return "", nil
		}
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}
	if err := logger.LogJSON(logger.GetLogDir(), "chat_output", data); err != nil {
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
