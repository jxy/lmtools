package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"lmtools/internal/auth"
	"lmtools/internal/config"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"lmtools/internal/retry"
	"lmtools/internal/session"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
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

	cfg, err := config.ParseFlags(os.Args[1:])
	if err != nil {
		return fmt.Errorf("invalid flags: %w", err)
	}

	// Initialize logging
	logDir := logger.GetLogDir()
	if cfg.LogDir != "" {
		// Validate and convert to absolute path if needed
		absLogDir, err := filepath.Abs(cfg.LogDir)
		if err != nil {
			return fmt.Errorf("invalid log directory: %w", err)
		}

		// Create directory if it doesn't exist
		if err := os.MkdirAll(absLogDir, logger.DirPerm); err != nil {
			return fmt.Errorf("failed to create log directory: %w", err)
		}

		// Verify it's a directory (not a file)
		info, err := os.Stat(absLogDir)
		if err != nil {
			return fmt.Errorf("failed to access log directory: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("log-dir path exists but is not a directory: %s", absLogDir)
		}

		logDir = absLogDir
	}

	if err := logger.InitializeWithOptions(
		logger.WithLogDir(logDir),
		logger.WithLevel("info"),
		logger.WithFormat("text"),
		logger.WithOutputMode(logger.OutputAuto),
		logger.WithComponent("lmc"),
		logger.WithStderrMinLevel("error"), // Only errors to stderr for lmc
	); err != nil {
		// Log error to stderr but continue - logging is not critical
		fmt.Fprintf(os.Stderr, "Warning: failed to initialize logging: %v\n", err)
	}
	defer logger.Close()

	// Set custom sessions directory if provided
	if cfg.SessionsDir != "" {
		// Validate and convert to absolute path if needed
		absDir, err := filepath.Abs(cfg.SessionsDir)
		if err != nil {
			return fmt.Errorf("invalid sessions directory: %w", err)
		}

		// Create directory if it doesn't exist
		if err := os.MkdirAll(absDir, logger.DirPerm); err != nil {
			return fmt.Errorf("failed to create sessions directory: %w", err)
		}

		// Verify it's a directory (not a file)
		info, err := os.Stat(absDir)
		if err != nil {
			return fmt.Errorf("failed to access sessions directory: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("sessions-dir path exists but is not a directory: %s", absDir)
		}

		session.SetSessionsDir(absDir)
		logger.Infof("Using custom sessions directory: %s", absDir)
	}

	// Set skip flock check if provided
	if cfg.SkipFlockCheck {
		session.SetSkipFlockCheck(true)
	}

	// Handle show-sessions flag
	if cfg.ShowSessions {
		return session.ShowSessions()
	}

	// Handle show flag
	if cfg.Show != "" {
		return session.ShowDispatcher(cfg.Show)
	}

	// Handle delete flag
	if cfg.Delete != "" {
		return session.DeleteNode(cfg.Delete)
	}

	// Handle list-models flag
	if cfg.ListModels {
		return listModels(cfg, logDir)
	}

	// Check if we're branching from an assistant message (regeneration)
	isRegeneration := false
	if cfg.Branch != "" {
		isAssistant, err := session.IsAssistantMessage(cfg.Branch)
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
		if len(inputBytes) > core.MaxInputSizeBytes {
			return fmt.Errorf("input too large: %d bytes (max: %d bytes)", len(inputBytes), core.MaxInputSizeBytes)
		}

		inputStr = strings.TrimSpace(string(inputBytes))

		// Only require input if not regenerating
		if inputStr == "" {
			return fmt.Errorf("input cannot be empty")
		}
	}

	// Handle sessions
	var sess *session.Session
	var sessionErr error

	if !cfg.NoSession {
		sess, sessionErr = handleSession(&cfg, inputStr, isRegeneration)
		if sessionErr != nil {
			return fmt.Errorf("session error: %w", sessionErr)
		}
	}

	var req *http.Request
	var body []byte

	// Log request start
	startTime := time.Now()
	logger.Infof("Starting request | Model: %s | Provider: %s", getActualModel(cfg), cfg.Provider)

	if sess != nil {
		if isRegeneration {
			// For regeneration, build request with the created session
			req, body, err = core.BuildRegenerationRequest(cfg, sess, session.GetLineageAdapter)
		} else {
			req, body, err = core.BuildRequestWithSession(cfg, sess, session.GetLineageAdapter)
		}
	} else {
		req, body, err = core.BuildRequest(cfg, inputStr)
	}

	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}

	// Log request details
	var requestData map[string]interface{}
	if err := json.Unmarshal(body, &requestData); err == nil {
		messages, _ := requestData["messages"].([]interface{})
		tools, _ := requestData["tools"].([]interface{})
		logger.Infof("Request built | Messages: %d | Tools: %d | Stream: %v",
			len(messages), len(tools), cfg.StreamChat)
	}

	// Log request
	opName := getOperationName(&cfg)
	if err := logger.LogJSON(logDir, opName, body); err != nil {
		// Log error but don't fail the request - logging is not critical
		fmt.Fprintf(os.Stderr, "Warning: failed to log request: %v\n", err)
	}

	// Create pooled HTTP client with retry logic
	retryClient := retry.NewClientWithRetries(cfg.Timeout, cfg.Retries, logger.GetLogger())

	// Send request with retry using pooled client
	// Redact API key from URL before logging
	logURL := req.URL.String()
	if req.URL.Query().Get("key") != "" {
		u := *req.URL
		q := u.Query()
		q.Set("key", "REDACTED")
		u.RawQuery = q.Encode()
		logURL = u.String()
	}
	logger.Infof("Sending request to %s", logURL)
	resp, err := retryClient.Do(ctx, req, "lmc")
	// Handle error with response cleanup
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		logger.Errorf("Request failed after %v: %v", time.Since(startTime), err)
		return fmt.Errorf("failed to send request: %w", err)
	}

	// Log response received
	logger.Infof("Response received | Status: %d | Duration: %v", resp.StatusCode, time.Since(startTime))

	// Defer response cleanup for success case
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	out, err := core.HandleResponse(ctx, cfg, resp, logger.DefaultLogger())
	if err != nil {
		logger.Errorf("Response handling failed: %v", err)
		return fmt.Errorf("failed to handle response: %w", err)
	}

	// Log completion
	logger.Infof("Request completed successfully | Total duration: %v", time.Since(startTime))

	// Save response to session if enabled
	if sess != nil && out != "" {
		responseMsg := session.Message{
			Role:      "assistant",
			Content:   strings.TrimSpace(out),
			Timestamp: time.Now(),
			Model:     getActualModel(cfg),
		}
		path, msgID, err := session.AppendMessage(sess, responseMsg)
		if err != nil {
			// Log error but don't fail the request
			fmt.Fprintf(os.Stderr, "Warning: failed to save response to session: %v\n", err)
		} else if path != sess.Path {
			// Update session path if a sibling was created
			sess.Path = path
			fmt.Fprintf(os.Stderr, "Note: Response saved to sibling branch %s as message %s\n",
				session.GetSessionID(path), msgID)
		}
	}

	if out != "" {
		fmt.Print(out)
	}
	return nil
}

func handleSession(cfg *config.Config, inputStr string, isRegeneration bool) (*session.Session, error) {
	var sess *session.Session
	var err error

	// Determine how to handle the session
	if cfg.Resume != "" {
		// Resume or branch based on the provided ID
		sess, err = handleResumeOrBranch(cfg.Resume, inputStr)
	} else if cfg.Branch != "" {
		// Explicit branch
		sessionPath, messageID := session.ParseMessageID(cfg.Branch)
		siblingPath, err := session.CreateSibling(sessionPath, messageID)
		if err != nil {
			return nil, fmt.Errorf("failed to create branch: %w", err)
		}
		sess, err = session.LoadSession(siblingPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load branch session: %w", err)
		}
	} else {
		// Create new session
		sess, err = session.CreateSession()
	}

	if err != nil {
		return nil, err
	}

	// Only save user input if not regenerating
	if !isRegeneration && inputStr != "" {
		userMsg := session.Message{
			Role:      "user",
			Content:   inputStr,
			Timestamp: time.Now(),
		}

		path, msgID, err := session.AppendMessage(sess, userMsg)
		if err != nil {
			return nil, fmt.Errorf("failed to save user message: %w", err)
		}

		// Update session path if a sibling was created
		if path != sess.Path {
			sess.Path = path
			// Log that we're using a sibling
			fmt.Fprintf(os.Stderr, "Note: Using sibling branch %s for message %s\n",
				session.GetSessionID(path), msgID)
		}
	}

	return sess, nil
}

// listModels queries and displays available models for the configured provider
func listModels(cfg config.Config, logDir string) error {
	// Determine provider
	provider := cfg.Provider
	if provider == "" {
		provider = "argo"
	}

	// Build models endpoint URL
	var url string
	if cfg.ProviderURL != "" {
		// Custom provider URL
		url = strings.TrimRight(cfg.ProviderURL, "/") + "/models"
	} else {
		// Standard provider endpoints
		switch provider {
		case "argo":
			if cfg.ArgoEnv == "prod" {
				url = "https://apps.inside.anl.gov/argoapi/api/v1/models/"
			} else {
				url = "https://apps-dev.inside.anl.gov/argoapi/api/v1/models/"
			}
		case "openai":
			url = "https://api.openai.com/v1/models"
		case "google":
			url = "https://generativelanguage.googleapis.com/v1beta/models"
		case "anthropic":
			url = "https://api.anthropic.com/v1/models"
		default:
			return fmt.Errorf("unknown provider: %s", provider)
		}
	}

	// Create HTTP request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Add authentication headers if needed
	if provider != "argo" && cfg.ProviderURL == "" {
		// Standard non-Argo providers need API key
		if cfg.APIKeyFile == "" {
			return fmt.Errorf("-api-key-file is required for %s provider when listing models", provider)
		}

		apiKey, err := auth.ReadKeyFile(cfg.APIKeyFile)
		if err != nil {
			return fmt.Errorf("failed to read API key: %w", err)
		}

		// Use centralized header setting
		auth.SetProviderHeaders(req, provider, apiKey)

		// Handle Google's special case (API key in URL)
		if provider == "google" {
			// Google uses API key in URL
			if !strings.Contains(url, "?") {
				url += "?key=" + apiKey
			} else {
				url += "&key=" + apiKey
			}
			u, err := req.URL.Parse(url)
			if err != nil {
				return fmt.Errorf("failed to parse URL %s: %w", url, err)
			}
			req.URL = u
		}
	}

	// Make request
	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("failed to fetch models: HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Read response
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	// Log the raw response for debugging
	if err := logger.LogJSON(logDir, "list_models_response", data); err != nil {
		logger.Warnf("Failed to log models response: %v", err)
	}

	// Parse and display models based on provider
	switch provider {
	case "argo":
		return displayArgoModels(data)
	case "openai":
		return displayOpenAIModels(data)
	case "google":
		return displayGoogleModels(data)
	case "anthropic":
		return displayAnthropicModels(data)
	default:
		// For custom providers, just display raw JSON
		fmt.Println(string(data))
		return nil
	}
}

// Provider-specific model display functions
func displayArgoModels(data []byte) error {
	// Try to parse as array first (old format)
	var models []string
	if err := json.Unmarshal(data, &models); err == nil {
		sort.Strings(models)
		fmt.Println("Available Argo models:")
		for _, model := range models {
			fmt.Printf("  %s\n", model)
		}
		return nil
	}

	// Try to parse as object with models field
	var response struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(data, &response); err == nil && len(response.Models) > 0 {
		sort.Strings(response.Models)
		fmt.Println("Available Argo models:")
		for _, model := range response.Models {
			fmt.Printf("  %s\n", model)
		}
		return nil
	}

	// Try to parse as object with data field containing model objects
	var objectResponse struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
		Models []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &objectResponse); err == nil {
		if len(objectResponse.Data) > 0 {
			sort.Slice(objectResponse.Data, func(i, j int) bool {
				return objectResponse.Data[i].ID < objectResponse.Data[j].ID
			})
			fmt.Println("Available Argo models:")
			for _, model := range objectResponse.Data {
				if model.Name != "" {
					fmt.Printf("  %s (%s)\n", model.ID, model.Name)
				} else {
					fmt.Printf("  %s\n", model.ID)
				}
			}
			return nil
		}
		if len(objectResponse.Models) > 0 {
			sort.Slice(objectResponse.Models, func(i, j int) bool {
				return objectResponse.Models[i].ID < objectResponse.Models[j].ID
			})
			fmt.Println("Available Argo models:")
			for _, model := range objectResponse.Models {
				if model.Name != "" {
					fmt.Printf("  %s (%s)\n", model.ID, model.Name)
				} else {
					fmt.Printf("  %s\n", model.ID)
				}
			}
			return nil
		}
	}

	return fmt.Errorf("unable to parse Argo models response - check logs for raw response")
}

func displayOpenAIModels(data []byte) error {
	var response struct {
		Data []struct {
			ID      string `json:"id"`
			Created int64  `json:"created"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return fmt.Errorf("failed to parse OpenAI models: %w", err)
	}

	// Sort models by ID
	sort.Slice(response.Data, func(i, j int) bool {
		return response.Data[i].ID < response.Data[j].ID
	})

	fmt.Println("Available OpenAI models:")
	for _, model := range response.Data {
		fmt.Printf("  %s\n", model.ID)
	}
	return nil
}

func displayGoogleModels(data []byte) error {
	var response struct {
		Models []struct {
			Name             string   `json:"name"`
			DisplayName      string   `json:"displayName"`
			SupportedMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return fmt.Errorf("failed to parse Google models: %w", err)
	}

	// Sort by model ID (extracted from name)
	sort.Slice(response.Models, func(i, j int) bool {
		idI := strings.TrimPrefix(response.Models[i].Name, "models/")
		idJ := strings.TrimPrefix(response.Models[j].Name, "models/")
		return idI < idJ
	})

	fmt.Println("Available Google models:")
	for _, model := range response.Models {
		// Extract model ID from name (format: models/model-id)
		modelID := strings.TrimPrefix(model.Name, "models/")
		fmt.Printf("  %s (%s)\n", modelID, model.DisplayName)
	}
	return nil
}

func displayAnthropicModels(data []byte) error {
	var response struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Created     string `json:"created_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return fmt.Errorf("failed to parse Anthropic models: %w", err)
	}

	// Sort models by ID
	sort.Slice(response.Data, func(i, j int) bool {
		return response.Data[i].ID < response.Data[j].ID
	})

	fmt.Println("Available Anthropic models:")
	for _, model := range response.Data {
		if model.DisplayName != "" {
			fmt.Printf("  %s (%s)\n", model.ID, model.DisplayName)
		} else {
			fmt.Printf("  %s\n", model.ID)
		}
	}
	return nil
}

func handleResumeOrBranch(resumeID string, inputStr string) (*session.Session, error) {
	// For -resume, we only accept session IDs (directories), not message IDs

	// First try as absolute path
	if info, err := os.Stat(resumeID); err == nil && info.IsDir() {
		// It's an absolute session path
		return session.LoadSession(resumeID)
	}

	// Then try as relative to sessions dir
	sessionPath := filepath.Join(session.GetSessionsDir(), resumeID)
	if info, err := os.Stat(sessionPath); err == nil && info.IsDir() {
		// It's a session ID - continue in that session
		return session.LoadSession(resumeID)
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

func getOperationName(cfg *config.Config) string {
	if cfg.Embed {
		return "embed_input"
	}
	if cfg.StreamChat {
		return "stream_chat_input"
	}
	return "chat_input"
}

// buildRegenerationRequest builds a request for regenerating an assistant message
// getActualModel returns the actual model that will be used for the request
func getActualModel(cfg config.Config) string {
	if cfg.Model != "" {
		return cfg.Model
	}
	if cfg.Embed {
		return core.DefaultEmbedModel
	}
	// Get provider-specific default
	provider := cfg.Provider
	if provider == "" {
		provider = "argo"
	}
	return core.GetDefaultChatModel(provider)
}

// getExitCode returns the appropriate exit code for an error
func getExitCode(err error) int {
	if err == nil {
		return exitSuccess
	}

	// Check for interruption
	if errors.Is(err, core.ErrInterrupted) || errors.Is(err, context.Canceled) {
		return exitInterrupted
	}

	// Everything else is a general error
	return exitError
}
