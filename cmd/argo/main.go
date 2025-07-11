package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	argo "lmtools/argolib"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Exit codes - simplified to 3
const (
	exitSuccess     = 0   // Success
	exitError       = 1   // General error
	exitInterrupted = 130 // Standard for SIGINT
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(getExitCode(err))
	}
}

func run() error {
	// Single context with signal handling
	ctx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := argo.ParseFlags(os.Args[1:])
	if err != nil {
		return fmt.Errorf("invalid flags: %w", err)
	}

	// Handle show-sessions flag
	if cfg.ShowSessions {
		return argo.ShowSessions()
	}

	// Handle delete flag
	if cfg.Delete != "" {
		return argo.DeleteNode(cfg.Delete)
	}

	// Initialize logging with info level (hardcoded)
	if err := argo.InitLogging("info"); err != nil {
		return fmt.Errorf("failed to init logging: %w", err)
	}

	// Check if we're branching from an assistant message (regeneration)
	isRegeneration := false
	if cfg.Branch != "" {
		isAssistant, err := argo.IsAssistantMessage(cfg.Branch)
		if err != nil {
			return fmt.Errorf("failed to check branch message type: %w", err)
		}
		isRegeneration = isAssistant
	}

	// Only read stdin if not regenerating
	var inputStr string
	if !isRegeneration {
		inputBytes, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("failed to read from STDIN: %w", err)
		}

		// Combined validation
		if len(inputBytes) > argo.MaxInputSizeBytes {
			return fmt.Errorf("input too large: %d bytes (max: %d bytes)", len(inputBytes), argo.MaxInputSizeBytes)
		}

		inputStr = strings.TrimSpace(string(inputBytes))

		// Only require input if not regenerating
		if inputStr == "" {
			return fmt.Errorf("input cannot be empty")
		}
	}

	// Handle sessions
	var session *argo.Session
	var sessionErr error

	if !cfg.NoSession {
		session, sessionErr = handleSession(&cfg, inputStr, isRegeneration)
		if sessionErr != nil {
			return fmt.Errorf("session error: %w", sessionErr)
		}
	}

	var req *http.Request
	var body []byte

	if session != nil {
		if isRegeneration {
			// For regeneration, build request with the created session
			req, body, err = buildRegenerationRequest(cfg, session)
		} else {
			req, body, err = argo.BuildRequestWithSession(cfg, session)
		}
	} else {
		req, body, err = argo.BuildRequest(cfg, inputStr)
	}

	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}

	// Log request
	opName := getOperationName(&cfg)
	if err := argo.LogJSON(cfg.LogDir, opName, body); err != nil {
		return fmt.Errorf("failed to log request: %w", err)
	}

	// Create HTTP client with timeout
	client := &http.Client{Timeout: cfg.Timeout}

	// Send request with retry (direct synchronous call)
	// Hardcoded backoff time of 1 second
	resp, err := argo.Retry(ctx, client, req, body, cfg.Retries, 1*time.Second)
	// Handle error with response cleanup
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return fmt.Errorf("failed to send request: %w", err)
	}

	// Defer response cleanup for success case
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	out, err := argo.HandleResponse(ctx, cfg, resp)
	if err != nil {
		return fmt.Errorf("failed to handle response: %w", err)
	}

	// Save response to session if enabled
	if session != nil && out != "" {
		responseMsg := argo.Message{
			Role:      "assistant",
			Content:   strings.TrimSpace(out),
			Timestamp: time.Now(),
			Model:     cfg.Model,
		}
		if _, err := argo.AppendMessage(session, responseMsg); err != nil {
			// Log error but don't fail the request
			fmt.Fprintf(os.Stderr, "Warning: failed to save response to session: %v\n", err)
		}
	}

	// Explicit cancel before returning (good practice)
	cancel()

	if out != "" {
		fmt.Print(out)
	}
	return nil
}

func handleSession(cfg *argo.Config, inputStr string, isRegeneration bool) (*argo.Session, error) {
	var session *argo.Session
	var err error

	// Determine how to handle the session
	if cfg.Resume != "" {
		// Resume or branch based on the provided ID
		session, err = handleResumeOrBranch(cfg.Resume, inputStr)
	} else if cfg.Branch != "" {
		// Explicit branch
		sessionPath, messageID := argo.ParseMessageID(cfg.Branch)
		siblingPath, err := argo.CreateSibling(sessionPath, messageID)
		if err != nil {
			return nil, fmt.Errorf("failed to create branch: %w", err)
		}
		session, err = argo.LoadSession(siblingPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load branch session: %w", err)
		}
	} else {
		// Create new session
		session, err = argo.CreateSession()
	}

	if err != nil {
		return nil, err
	}

	// Only save user input if not regenerating
	if !isRegeneration && inputStr != "" {
		userMsg := argo.Message{
			Role:      "user",
			Content:   inputStr,
			Timestamp: time.Now(),
		}

		if _, err := argo.AppendMessage(session, userMsg); err != nil {
			return nil, fmt.Errorf("failed to save user message: %w", err)
		}
	}

	return session, nil
}

func handleResumeOrBranch(resumeID string, inputStr string) (*argo.Session, error) {
	// For -resume, we only accept session IDs (directories), not message IDs

	// First try as absolute path
	if info, err := os.Stat(resumeID); err == nil && info.IsDir() {
		// It's an absolute session path
		return argo.LoadSession(resumeID)
	}

	// Then try as relative to sessions dir
	sessionPath := filepath.Join(argo.GetSessionsDir(), resumeID)
	if info, err := os.Stat(sessionPath); err == nil && info.IsDir() {
		// It's a session ID - continue in that session
		return argo.LoadSession(resumeID)
	}

	// Check if it looks like a message ID (contains a hex component after /)
	if strings.Contains(resumeID, "/") {
		parts := strings.Split(resumeID, "/")
		lastPart := parts[len(parts)-1]
		if _, err := strconv.ParseUint(lastPart, 16, 64); err == nil {
			// It's a message ID - this is not allowed for -resume
			return nil, fmt.Errorf("-resume requires a session ID (directory path), not a message ID; use -branch to create a branch from a message")
		}
	}

	// Not found as directory
	return nil, fmt.Errorf("session not found: %s", resumeID)
}

func getOperationName(cfg *argo.Config) string {
	if cfg.Embed {
		return "embed_input"
	}
	if cfg.StreamChat {
		return "stream_chat_input"
	}
	return "chat_input"
}

// buildRegenerationRequest builds a request for regenerating an assistant message
func buildRegenerationRequest(cfg argo.Config, session *argo.Session) (*http.Request, []byte, error) {
	// The session has already been created in handleSession, so we use it directly
	// Get the lineage for this new branch
	messages, err := argo.GetLineage(session.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get lineage: %w", err)
	}

	// Convert to chat messages
	chatMessages := []argo.ChatMessage{{Role: "system", Content: cfg.System}}
	for _, msg := range messages {
		chatMessages = append(chatMessages, argo.ChatMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	// Build the request
	urlBase := argo.GetBaseURL(cfg.Env)
	model := cfg.Model
	if model == "" {
		model = argo.DefaultChatModel
	}

	if err := argo.ValidateChatModel(model); err != nil {
		return nil, nil, err
	}

	req := argo.ChatRequest{
		User:     cfg.User,
		Model:    model,
		Messages: chatMessages,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request: %w", err)
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

// getExitCode returns the appropriate exit code for an error
func getExitCode(err error) int {
	if err == nil {
		return exitSuccess
	}

	// Check for interruption
	if errors.Is(err, argo.ErrInterrupted) || errors.Is(err, context.Canceled) {
		return exitInterrupted
	}

	// Everything else is a general error
	return exitError
}
