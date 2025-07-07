package argo

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

func HandleResponse(cfg Config, resp *http.Response) (string, error) {
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
			return "", fmt.Errorf("failed to unmarshal embed response: %w", err)
		}
		return embedResp.Embedding, nil
	}

	if cfg.StreamChat {
		f, _, err := CreateTimestampedFile(cfg.LogDir, "stream_chat_output", "log")
		if err != nil {
			return "", fmt.Errorf("failed to create log file: %w", err)
		}
		defer func() {
			if err := f.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "failed to close log file: %v\n", err)
			}
		}()
		if _, err := io.Copy(io.MultiWriter(os.Stdout, f), resp.Body); err != nil {
			return "", fmt.Errorf("error streaming response: %w", err)
		}
		return "", nil
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
		return "", fmt.Errorf("failed to unmarshal chat response: %w", err)
	}
	return chatResp.Response, nil
}
