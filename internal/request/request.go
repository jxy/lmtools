package request

import (
	"bytes"
	"encoding/json"
	"fmt"
	"lmtools/internal/config"
	"lmtools/internal/models"
	"lmtools/internal/session"
	"net/http"
)

type EmbedRequest struct {
	User   string   `json:"user"`
	Model  string   `json:"model"`
	Prompt []string `json:"prompt"`
}

type ChatRequest struct {
	User     string        `json:"user"`
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func BuildRequest(cfg config.Config, input string) (*http.Request, []byte, error) {
	urlBase := models.GetBaseURL(cfg.Env)

	model := cfg.Model
	var (
		body     []byte
		endpoint string
		err      error
	)
	if cfg.Embed {
		if model == "" {
			model = models.DefaultEmbedModel
		}
		// Validate embed model
		if err := config.ValidateEmbedModel(model); err != nil {
			return nil, nil, err
		}
		req := EmbedRequest{User: cfg.User, Model: model, Prompt: []string{input}}
		if body, err = json.Marshal(req); err != nil {
			return nil, nil, fmt.Errorf("failed to marshal embed request: %w", err)
		}
		endpoint = fmt.Sprintf("%s/embed/", urlBase)
	} else {
		if model == "" {
			model = models.DefaultChatModel
		}
		// Validate chat model
		if err := config.ValidateChatModel(model); err != nil {
			return nil, nil, err
		}
		req := ChatRequest{
			User:  cfg.User,
			Model: model,
			Messages: []ChatMessage{
				{Role: "system", Content: cfg.System},
				{Role: "user", Content: input},
			},
		}
		if body, err = json.Marshal(req); err != nil {
			return nil, nil, fmt.Errorf("failed to marshal chat request: %w", err)
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

// BuildRequestWithSession builds a request that includes conversation history from a session
func BuildRequestWithSession(cfg config.Config, sess *session.Session) (*http.Request, []byte, error) {
	if cfg.Embed {
		return nil, nil, fmt.Errorf("embed mode does not support sessions")
	}

	// Get conversation history
	messages, err := session.GetLineage(sess.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get conversation history: %w", err)
	}

	// Convert to ChatMessage format
	chatMessages := []ChatMessage{{Role: "system", Content: cfg.System}}
	for _, msg := range messages {
		chatMessages = append(chatMessages, ChatMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	urlBase := models.GetBaseURL(cfg.Env)
	model := cfg.Model
	if model == "" {
		model = models.DefaultChatModel
	}

	// Validate chat model
	if err := config.ValidateChatModel(model); err != nil {
		return nil, nil, err
	}

	req := ChatRequest{
		User:     cfg.User,
		Model:    model,
		Messages: chatMessages,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal chat request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/chat/", urlBase)
	if cfg.StreamChat {
		endpoint = fmt.Sprintf("%s/streamchat/", urlBase)
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
