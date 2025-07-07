package argo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

const (
	ProdURL           = "https://apps.inside.anl.gov/argoapi/api/v1/resource"
	DevURL            = "https://apps-dev.inside.anl.gov/argoapi/api/v1/resource"
	DefaultChatModel  = "gemini25pro"
	DefaultEmbedModel = "v3large"
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
	var urlBase string
	switch cfg.Env {
	case "prod":
		urlBase = ProdURL
	case "", "dev":
		urlBase = DevURL
	default:
		urlBase = cfg.Env
	}

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
