package main

import (
	"context"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"lmtools/internal/auth"
	"lmtools/internal/config"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/limitio"
	"lmtools/internal/logger"
	"lmtools/internal/retry"
	"lmtools/internal/session"
	"lmtools/internal/ui"
	"lmtools/internal/ui/tools"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
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

// RequestController encapsulates the request lifecycle to reduce complexity in run()
type RequestController struct {
	ctx      context.Context
	cfg      *config.Config
	notifier core.Notifier
	logDir   string
}

// NewRequestController creates a new request controller
func NewRequestController(ctx context.Context, cfg *config.Config, notifier core.Notifier, logDir string) *RequestController {
	// Add request counter to context for request-scoped logging
	ctx = logger.WithNewRequestCounter(ctx)

	return &RequestController{
		ctx:      ctx,
		cfg:      cfg,
		notifier: notifier,
		logDir:   logDir,
	}
}

// handleRequest processes the main request lifecycle
func (rc *RequestController) handleRequest(inputStr string, sess *session.Session, isRegeneration bool) error {
	// Build HTTP request
	rb, err := rc.buildRequest(sess, inputStr)
	if err != nil {
		return err
	}

	// Send request with retry
	resp, err := rc.sendRequest(rb.Request)
	if err != nil {
		return err
	}

	// Handle response
	response, err := rc.handleResponse(resp)
	if err != nil {
		return err
	}

	// Process tool calls or save response
	if len(response.ToolCalls) > 0 {
		return rc.handleToolCalls(sess, response, rb)
	}

	return rc.handleNormalResponse(response, sess, rb.Model)
}

// buildRequest constructs the HTTP request
func (rc *RequestController) buildRequest(sess *session.Session, inputStr string) (core.RequestBuild, error) {
	return buildHTTPRequest(rc.ctx, rc.cfg, sess, inputStr, rc.logDir, rc.notifier)
}

// sendRequest sends the request with retry logic
func (rc *RequestController) sendRequest(req *http.Request) (*http.Response, error) {
	return sendWithRetry(rc.ctx, req, rc.cfg)
}

// handleResponse processes the HTTP response
func (rc *RequestController) handleResponse(resp *http.Response) (*core.Response, error) {
	response, err := core.HandleResponse(rc.ctx, *rc.cfg, resp, logger.From(rc.ctx), rc.notifier)
	if err != nil {
		return nil, errors.WrapError("handle response", err)
	}
	return &response, nil
}

// handleToolCalls processes responses with tool calls
func (rc *RequestController) handleToolCalls(sess *session.Session, response *core.Response, rb core.RequestBuild) error {
	logger.From(rc.ctx).Infof("Handling tool execution with %d tool calls", len(response.ToolCalls))

	// Build tool execution context
	tc := rc.newToolContext(sess, response, rb)

	// Execute tools and get the final response
	result := core.HandleToolExecution(tc)

	// Handle the result
	return rc.finishToolExecution(result, rc.cfg.StreamChat)
}

// newToolContext creates a tool execution context with all required dependencies
func (rc *RequestController) newToolContext(sess *session.Session, response *core.Response, rb core.RequestBuild) core.ToolContext {
	// Create session store
	store := session.NewStore(sess, logger.From(rc.ctx))

	// Create retry client for tool execution
	retryClient := retry.NewClientWithRetries(rc.cfg.Timeout, rc.cfg.Retries, logger.From(rc.ctx))

	// Configure tool execution
	execCfg := core.ToolExecutionConfig{
		Store:       store,
		RetryClient: retryClient,
		LogRequestFn: func(body []byte) error {
			return logger.From(rc.ctx).LogJSON(rc.logDir, "tool_result_input", body)
		},
		ActualModel: rb.Model,
	}

	approver := NewCliApprover(rc.notifier)

	// Create a cached message builder for efficient tool execution
	messageBuilder := rc.createMessageBuilder(sess)

	// Create CLI-specific UI for tool display
	ui := tools.NewCLIToolUI(rc.notifier, *rc.cfg)

	// Return the complete tool context
	return core.ToolContext{
		Ctx:          rc.ctx,
		Cfg:          *rc.cfg,
		Logger:       logger.From(rc.ctx),
		Notifier:     rc.notifier,
		Approver:     approver,
		ExecCfg:      execCfg,
		Model:        rb.Model,
		ToolDefs:     rb.ToolDefs,
		MessagesFn:   messageBuilder,
		UI:           ui,
		InitialText:  response.Text,
		InitialCalls: response.ToolCalls,
	}
}

// createMessageBuilder creates an appropriate message builder for the session
func (rc *RequestController) createMessageBuilder(sess *session.Session) func(string) ([]core.TypedMessage, error) {
	if sess != nil {
		if cached, err := session.CreateCachedMessageBuilder(rc.ctx, sess.Path); err == nil {
			// Use the cached builder directly
			return cached
		} else {
			logger.From(rc.ctx).Warnf("Failed to create cached message builder: %v", err)
			// Fall back to the default builder with context wrapper
			return func(path string) ([]core.TypedMessage, error) {
				return session.BuildMessagesWithToolInteractions(rc.ctx, path)
			}
		}
	} else {
		// Use default builder with context wrapper
		return func(path string) ([]core.TypedMessage, error) {
			return session.BuildMessagesWithToolInteractions(rc.ctx, path)
		}
	}
}

// finishToolExecution handles the final result of tool execution
func (rc *RequestController) finishToolExecution(result core.ToolExecutionResult, isStreaming bool) error {
	if result.Error != nil {
		return result.Error
	}

	// Print the final text if not streaming
	if result.FinalText != "" && !isStreaming {
		fmt.Print(result.FinalText)
	}

	return nil
}

// handleNormalResponse processes responses without tool calls
func (rc *RequestController) handleNormalResponse(response *core.Response, sess *session.Session, model string) error {
	// Save assistant response to session
	if err := persistAssistantOnly(rc.ctx, response.Text, sess, rc.cfg, rc.notifier, model); err != nil {
		return err
	}

	// Print output if not streaming
	if response.Text != "" && !rc.cfg.StreamChat {
		fmt.Print(response.Text)
	}
	return nil
}

func main() {
	// Create a notifier instance for error reporting
	notifier := ui.NewNotifier()

	if err := run(notifier); err != nil {
		notifier.Errorf("Error: %v", err)
		os.Exit(getExitCode(err))
	}
}

func run(notifier core.Notifier) error {
	// Single context with signal handling
	ctx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.ParseFlags(os.Args[1:])
	if err != nil {
		return errors.WrapError("parse flags", err)
	}

	// Initialize logging
	logDir, err := setupLogging(cfg, notifier)
	if err != nil {
		return err
	}
	defer logger.Close()

	// Setup sessions directory and configuration
	if err := setupSessionsConfig(ctx, cfg); err != nil {
		return err
	}

	// Handle special flags that don't require full processing
	if handled, err := handleSpecialFlags(ctx, &cfg, notifier, logDir); handled {
		return err
	}

	// Check if we're branching from an assistant message (regeneration)
	isRegeneration := false
	if cfg.Branch != "" {
		isAssistant, err := session.IsAssistantMessage(cfg.Branch)
		if err != nil {
			return errors.WrapError("check branch message type", err)
		}
		isRegeneration = isAssistant
	}

	// Read and validate input
	inputStr, err := readAndValidateInput(isRegeneration)
	if err != nil {
		return err
	}

	// Prepare session using coordinator
	var sess *session.Session
	var executedPending bool

	if !cfg.NoSession {
		coordinator := session.NewCoordinator(&cfg, notifier)
		approver := NewCliApprover(notifier)

		result, err := coordinator.PrepareSession(ctx, inputStr, isRegeneration, approver)
		if err != nil {
			return err
		}
		sess = result.Session
		executedPending = result.ExecutedPending
	}

	// Only require input if not regenerating and not continuing tool execution
	if !isRegeneration && inputStr == "" && !executedPending {
		return errors.WrapError("validate input", stdErrors.New("input cannot be empty"))
	}

	// Create request controller and handle the request
	controller := NewRequestController(ctx, &cfg, notifier, logDir)
	return controller.handleRequest(inputStr, sess, isRegeneration)
}

// handleSpecialFlags handles flags that don't require the full request processing
func handleSpecialFlags(ctx context.Context, cfg *config.Config, notifier core.Notifier, logDir string) (bool, error) {
	// Handle show-sessions flag
	if cfg.ShowSessions {
		return true, session.ShowSessions(notifier)
	}

	// Handle show flag
	if cfg.Show != "" {
		return true, session.ShowDispatcher(cfg.Show, notifier)
	}

	// Handle delete flag
	if cfg.Delete != "" {
		return true, session.DeleteNode(cfg.Delete)
	}

	// Handle list-models flag
	if cfg.ListModels {
		return true, listModels(ctx, *cfg, logDir)
	}

	return false, nil
}

// Removed duplicate message builder functions - now using session.BuildAssistantMessageWithToolCalls
// and session.BuildUserMessageWithToolResults from internal/session/message_builder.go

// executePendingTools moved to internal/session/pending_tools.go as ExecutePendingTools

// setupLogging initializes the logging system based on configuration
func setupLogging(cfg config.Config, notifier core.Notifier) (string, error) {
	logDir := logger.GetLogDir()
	if cfg.LogDir != "" {
		// Validate and convert to absolute path if needed
		absLogDir, err := filepath.Abs(cfg.LogDir)
		if err != nil {
			return "", errors.WrapError("validate log directory", err)
		}

		// Create directory if it doesn't exist
		if err := os.MkdirAll(absLogDir, constants.DirPerm); err != nil {
			return "", errors.WrapError("create log directory", err)
		}

		// Verify it's a directory (not a file)
		info, err := os.Stat(absLogDir)
		if err != nil {
			return "", errors.WrapError("access log directory", err)
		}
		if !info.IsDir() {
			return "", errors.WrapError("validate log directory", fmt.Errorf("log-dir path exists but is not a directory: %s", absLogDir))
		}

		logDir = absLogDir
	}

	if err := logger.InitializeWithOptions(
		logger.WithLogDir(logDir),
		logger.WithLevel(strings.ToLower(cfg.LogLevel)),
		logger.WithFormat("text"),
		logger.WithComponent("lmc"),
		logger.WithStderr(true),            // Explicit: enable stderr output
		logger.WithFile(true),              // Explicit: enable file output
		logger.WithStderrMinLevel("error"), // Only errors to stderr for lmc
	); err != nil {
		// Log warning but continue - logging is not critical for operation
		notifier.Warnf("Failed to initialize logging: %v", err)
	}

	return logDir, nil
}

// setupSessionsConfig sets up the sessions directory and configuration
func setupSessionsConfig(ctx context.Context, cfg config.Config) error {
	// Set custom sessions directory if provided
	if cfg.SessionsDir != "" {
		// Validate and convert to absolute path if needed
		absDir, err := filepath.Abs(cfg.SessionsDir)
		if err != nil {
			return errors.WrapError("validate sessions directory", err)
		}

		// Create directory if it doesn't exist
		if err := os.MkdirAll(absDir, constants.DirPerm); err != nil {
			return errors.WrapError("create sessions directory", err)
		}

		// Verify it's a directory (not a file)
		info, err := os.Stat(absDir)
		if err != nil {
			return errors.WrapError("access sessions directory", err)
		}
		if !info.IsDir() {
			return errors.WrapError("validate sessions directory", fmt.Errorf("sessions-dir path exists but is not a directory: %s", absDir))
		}

		session.SetSessionsDir(absDir)
		logger.From(ctx).Infof("Using custom sessions directory: %s", absDir)
	}

	// Set skip flock check if provided
	if cfg.SkipFlockCheck {
		session.SetSkipFlockCheck(true)
	}

	return nil
}

// readAndValidateInput reads input from stdin and validates it
func readAndValidateInput(isRegeneration bool) (string, error) {
	// Only read stdin if not regenerating
	var inputStr string
	if !isRegeneration {
		// Read stdin with size limit to prevent DoS
		inputBytes, err := limitio.ReadLimited(os.Stdin, constants.MaxCLIInputSize)
		if err != nil {
			return "", errors.WrapError("read stdin", err)
		}

		inputStr = strings.TrimSpace(string(inputBytes))
	}
	return inputStr, nil
}

// buildHTTPRequest builds the HTTP request based on configuration
func buildHTTPRequest(ctx context.Context, cfg *config.Config, sess *session.Session, inputStr string, logDir string, notifier core.Notifier) (core.RequestBuild, error) {
	var req *http.Request
	var body []byte
	var model string
	var toolDefs []core.ToolDefinition
	var err error

	// Log request start
	actualModel := cfg.Model
	if actualModel == "" {
		if cfg.Embed {
			actualModel = core.DefaultEmbedModel
		} else {
			actualModel = core.GetDefaultChatModel(cfg.Provider)
		}
	}
	logger.From(ctx).Infof("Starting request | Model: %s | Provider: %s", actualModel, cfg.Provider)

	if sess != nil {
		// Always use the tool-aware message builder for sessions
		// This ensures tool interactions are preserved during regeneration
		rb, err := core.BuildRequestWithToolInteractions(ctx, *cfg, sess, session.BuildMessagesWithToolInteractions)
		if err != nil {
			return core.RequestBuild{}, errors.WrapError("build request", err)
		}
		req = rb.Request
		body = rb.Body
		model = rb.Model
		toolDefs = rb.ToolDefs
	} else {
		req, body, err = core.BuildRequest(*cfg, inputStr)
		if err == nil {
			model = actualModel
			if cfg.IsToolEnabled() {
				toolDefs = core.GetBuiltinUniversalCommandTool()
			}
		}
	}

	if err != nil {
		return core.RequestBuild{}, errors.WrapError("build request", err)
	}

	// Log request details
	var requestData map[string]interface{}
	if err := json.Unmarshal(body, &requestData); err == nil {
		messages, _ := requestData["messages"].([]interface{})
		tools, _ := requestData["tools"].([]interface{})
		logger.From(ctx).Infof("Request built | Messages: %d | Tools: %d | Stream: %v",
			len(messages), len(tools), cfg.StreamChat)
	}

	// Log request
	opName := getOperationName(cfg)
	if err := logger.From(ctx).LogJSON(logDir, opName, body); err != nil {
		// Log error but don't fail the request - logging is not critical
		notifier.Warnf("Failed to log request: %v", err)
	}

	return core.RequestBuild{
		Request:  req,
		Body:     body,
		Model:    model,
		ToolDefs: toolDefs,
	}, nil
}

// sendWithRetry sends the HTTP request with retry logic
func sendWithRetry(ctx context.Context, req *http.Request, cfg *config.Config) (*http.Response, error) {
	// Create pooled HTTP client with retry logic
	retryClient := retry.NewClientWithRetries(cfg.Timeout, cfg.Retries, logger.From(ctx))

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
	logger.From(ctx).Infof("Sending request to %s", logURL)

	startTime := time.Now()
	resp, err := retryClient.Do(ctx, req, "lmc")
	// Handle error with response cleanup
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		logger.From(ctx).Errorf("Request failed after %v: %v", time.Since(startTime), err)
		return nil, err
	}

	// Log response received
	logger.From(ctx).Infof("Response received | Status: %d | Duration: %v", resp.StatusCode, time.Since(startTime))

	return resp, nil
}

// persistAssistantOnly saves assistant response when there are no tool calls
func persistAssistantOnly(ctx context.Context, out string, sess *session.Session, cfg *config.Config, notifier core.Notifier, model string) error {
	// Save assistant response to session if enabled (but NOT when there are tool calls - HandleToolExecution will do it)
	if sess != nil && out != "" {
		logger.From(ctx).Debugf("Saving assistant response to session | Length: %d | Streaming: %v", len(out), cfg.StreamChat)
		store := session.NewStore(sess, logger.From(ctx))
		previousPath := sess.Path
		path, messageID, err := store.SaveAssistant(ctx, out, nil, model)
		if err != nil {
			// Log error but don't fail the request
			notifier.Warnf("Warning: failed to save response to session: %v", err)
		} else if path != previousPath {
			notifier.Infof("Response saved to sibling branch %s as message %s",
				session.GetSessionID(path), messageID)
		} else {
			logger.From(ctx).Debugf("Response saved to session %s as message %s", session.GetSessionID(path), messageID)
		}
	} else {
		logger.From(ctx).Debugf("Not saving response | Session: %v | Output length: %d", sess != nil, len(out))
	}

	return nil
}

// listModels queries and displays available models for the configured provider
func listModels(ctx context.Context, cfg config.Config, logDir string) error {
	// Determine provider
	provider := cfg.Provider
	if provider == "" {
		provider = constants.ProviderArgo
	}

	// Build models endpoint URL
	var url string
	if cfg.ProviderURL != "" {
		// Custom provider URL
		url = strings.TrimRight(cfg.ProviderURL, "/") + "/models"
	} else {
		// Standard provider endpoints
		switch provider {
		case constants.ProviderArgo:
			if cfg.ArgoEnv == "prod" {
				url = "https://apps.inside.anl.gov/argoapi/api/v1/models/"
			} else {
				url = "https://apps-dev.inside.anl.gov/argoapi/api/v1/models/"
			}
		case constants.ProviderOpenAI:
			url = "https://api.openai.com/v1/models"
		case constants.ProviderGoogle:
			url = "https://generativelanguage.googleapis.com/v1beta/models"
		case constants.ProviderAnthropic:
			url = "https://api.anthropic.com/v1/models"
		default:
			return errors.WrapError("validate provider", fmt.Errorf("unknown provider: %s", provider))
		}
	}

	// Create HTTP request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return errors.WrapError("create request", err)
	}

	// Add authentication headers if needed
	if provider != constants.ProviderArgo && cfg.ProviderURL == "" {
		// Standard non-Argo providers need API key
		if cfg.APIKeyFile == "" {
			return errors.WrapError("validate config", fmt.Errorf("-api-key-file is required for %s provider when listing models", provider))
		}

		apiKey, err := auth.ReadKeyFile(cfg.APIKeyFile)
		if err != nil {
			return errors.WrapError("read API key", err)
		}

		// Use centralized header setting
		auth.SetProviderHeaders(req, provider, apiKey)

		// Handle Google's special case (API key in URL)
		if provider == constants.ProviderGoogle {
			if err := auth.ApplyGoogleAPIKey(req, apiKey); err != nil {
				return errors.WrapError("apply Google API key", err)
			}
		}
	}

	// Make request
	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return errors.WrapError("fetch models", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, err := limitio.ReadLimited(resp.Body, constants.MaxErrorResponseSize)
		if err != nil {
			return errors.WrapError("fetch models", fmt.Errorf("HTTP %d (failed to read body: %v)", resp.StatusCode, err))
		}
		return errors.WrapError("fetch models", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body)))
	}

	// Read response with size limit
	data, err := limitio.ReadLimited(resp.Body, constants.MaxCLIResponseSize)
	if err != nil {
		return errors.WrapError("read response", err)
	}

	// Log the raw response for debugging
	if err := logger.From(ctx).LogJSON(logDir, "list_models_response", data); err != nil {
		logger.From(ctx).Warnf("Failed to log models response: %v", err)
	}

	// Parse and display models based on provider
	switch provider {
	case constants.ProviderArgo:
		return displayArgoModels(data)
	case constants.ProviderOpenAI:
		return displayOpenAIModels(data)
	case constants.ProviderGoogle:
		return displayGoogleModels(data)
	case constants.ProviderAnthropic:
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

	return errors.WrapError("parse Argo models response", stdErrors.New("unable to parse Argo models response - check logs for raw response"))
}

func displayOpenAIModels(data []byte) error {
	var response struct {
		Data []struct {
			ID      string `json:"id"`
			Created int64  `json:"created"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return errors.WrapError("parse OpenAI models", err)
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
		return errors.WrapError("parse Google models", err)
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
		return errors.WrapError("parse Anthropic models", err)
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

func getOperationName(cfg *config.Config) string {
	if cfg.Embed {
		return "embed_input"
	}
	if cfg.StreamChat {
		return "stream_chat_input"
	}
	return "chat_input"
}

// getExitCode returns the appropriate exit code for an error
func getExitCode(err error) int {
	if err == nil {
		return exitSuccess
	}

	// Check for interruption
	if stdErrors.Is(err, core.ErrInterrupted) || stdErrors.Is(err, context.Canceled) {
		return exitInterrupted
	}

	// Everything else is a general error
	return exitError
}
