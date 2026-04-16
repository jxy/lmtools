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
	"lmtools/internal/modelcatalog"
	"lmtools/internal/providers"
	"lmtools/internal/retry"
	"lmtools/internal/session"
	"lmtools/internal/ui"
	"lmtools/internal/ui/tools"
	"net/http"
	"os"
	"os/signal"
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
	if err := persistAssistantOnly(rc.ctx, *response, sess, rc.cfg, rc.notifier, model); err != nil {
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
		absLogDir, err := ensureDirectoryPath(cfg.LogDir, "log directory")
		if err != nil {
			return "", err
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
	managerCfg := session.ManagerConfig{
		SkipFlockCheck: cfg.SkipFlockCheck,
	}

	// Set custom sessions directory if provided
	if cfg.SessionsDir != "" {
		absDir, err := ensureDirectoryPath(cfg.SessionsDir, "sessions directory")
		if err != nil {
			return err
		}

		managerCfg.SessionsDir = absDir
		logger.From(ctx).Infof("Using custom sessions directory: %s", absDir)
	}

	session.ConfigureDefaultManager(managerCfg)

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
			provider := providers.ResolveProvider(cfg.Provider)
			actualModel = core.GetDefaultChatModel(provider)
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
func persistAssistantOnly(ctx context.Context, response core.Response, sess *session.Session, cfg *config.Config, notifier core.Notifier, model string) error {
	// Save assistant response to session if enabled (but NOT when there are tool calls - HandleToolExecution will do it)
	if sess != nil && (response.Text != "" || response.ThoughtSignature != "") {
		logger.From(ctx).Debugf("Saving assistant response to session | Length: %d | Streaming: %v", len(response.Text), cfg.StreamChat)
		store := session.NewStore(sess, logger.From(ctx))
		previousPath := sess.Path
		path, messageID, err := store.SaveAssistantWithThoughtSignature(ctx, response.Text, nil, model, response.ThoughtSignature)
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
		logger.From(ctx).Debugf("Not saving response | Session: %v | Output length: %d", sess != nil, len(response.Text))
	}

	return nil
}

// listModels queries and displays available models for the configured provider
func listModels(ctx context.Context, cfg config.Config, logDir string) error {
	provider := cfg.Provider
	provider = providers.ResolveProvider(provider)

	url, err := providers.ResolveModelsURL(provider, cfg.ProviderURL, cfg.ArgoEnv)
	if err != nil {
		return errors.WrapError("validate provider", err)
	}

	// Create HTTP request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return errors.WrapError("create request", err)
	}

	// Add authentication headers if needed
	if providers.RequiresAPIKey(provider) && cfg.ProviderURL == "" {
		// Standard non-Argo providers need API key
		if cfg.APIKeyFile == "" {
			return errors.WrapError("validate config", fmt.Errorf("-api-key-file is required for %s provider when listing models", provider))
		}

		apiKey, err := auth.ReadKeyFile(cfg.APIKeyFile)
		if err != nil {
			return errors.WrapError("read API key", err)
		}

		if err := auth.ApplyProviderCredentials(req, provider, apiKey); err != nil {
			if provider == constants.ProviderGoogle {
				return errors.WrapError("apply Google API key", err)
			}
			return errors.WrapError("apply provider credentials", err)
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

	items, err := parseModelsForDisplay(provider, data)
	if err != nil {
		if cfg.ProviderURL != "" {
			if _, ok := providers.InfoFor(provider); !ok {
				fmt.Println(string(data))
				return nil
			}
		}
		return errors.WrapError("parse models response", err)
	}

	displayModels(provider, items)
	return nil
}

func parseModelsForDisplay(provider string, data []byte) ([]modelcatalog.Item, error) {
	if _, ok := providers.InfoFor(provider); ok {
		return modelcatalog.Parse(provider, data)
	}

	parsers := []func([]byte) ([]modelcatalog.Item, error){
		modelcatalog.ParseOpenAI,
		modelcatalog.ParseAnthropic,
		modelcatalog.ParseGoogle,
		modelcatalog.ParseArgo,
	}
	for _, parse := range parsers {
		if items, err := parse(data); err == nil {
			return items, nil
		}
	}

	return nil, fmt.Errorf("unable to parse models response for provider %s", provider)
}

func displayModels(provider string, items []modelcatalog.Item) {
	modelcatalog.Sort(items)

	name := provider
	if info, ok := providers.InfoFor(provider); ok {
		name = info.DisplayName
	}

	fmt.Printf("Available %s models:\n", name)
	for _, model := range items {
		if model.DisplayName != "" {
			fmt.Printf("  %s (%s)\n", model.ID, model.DisplayName)
			continue
		}
		fmt.Printf("  %s\n", model.ID)
	}
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
