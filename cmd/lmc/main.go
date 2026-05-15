package main

import (
	"bytes"
	"context"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"io"
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
	"net/http/httputil"
	"os"
	"os/signal"
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

func executeRequest(ctx context.Context, cfg *config.Config, opts core.RequestOptions, notifier core.Notifier, logDir, inputStr string, plan *session.RequestPlan) error {
	ctx = logger.WithNewRequestCounter(ctx)

	rb, err := buildHTTPRequest(ctx, cfg, opts, plan, inputStr)
	if err != nil {
		return err
	}
	logBuiltHTTPRequest(ctx, cfg, logDir, notifier, rb.Request, rb.Body)

	resp, err := sendWithRetry(ctx, rb.Request, cfg)
	if err != nil {
		return err
	}

	response, err := core.HandleResponseWithOptions(ctx, opts, resp, logger.From(ctx), notifier, core.ResponseParseOptions{
		ArgoLegacy: opts.IsArgoLegacy(),
		ToolDefs:   rb.ToolDefs,
	})
	if err != nil {
		return errors.WrapError("handle response", err)
	}

	var sess *session.Session
	if plan != nil {
		committed, err := plan.Commit(ctx)
		if err != nil {
			return errors.WrapError("commit session", err)
		}
		sess = committed
	}

	if len(response.ToolCalls) > 0 {
		return handleToolCalls(ctx, cfg, opts, notifier, logDir, sess, &response, rb, inputStr)
	}

	return handleNormalResponse(ctx, cfg, notifier, &response, sess, rb.Model)
}

func handleToolCalls(ctx context.Context, cfg *config.Config, opts core.RequestOptions, notifier core.Notifier, logDir string, sess *session.Session, response *core.Response, rb core.RequestBuild, inputStr string) error {
	logger.From(ctx).Infof("Handling tool execution with %d tool calls", len(response.ToolCalls))

	tc := newToolContext(ctx, cfg, opts, notifier, logDir, sess, response, rb, inputStr)

	result := core.HandleToolExecution(tc)

	return finishToolExecution(result)
}

func newToolContext(ctx context.Context, cfg *config.Config, opts core.RequestOptions, notifier core.Notifier, logDir string, sess *session.Session, response *core.Response, rb core.RequestBuild, inputStr string) core.ToolContext {
	store, messageBuilder := createToolStoreAndMessageBuilder(ctx, opts, sess, inputStr)

	retryClient := retry.NewClientWithRetries(cfg.Timeout, cfg.Retries, logger.From(ctx))

	execCfg := core.ToolExecutionConfig{
		Store:       store,
		RetryClient: retryClient,
		LogRequestFn: func(body []byte) error {
			return logger.From(ctx).LogJSON(logDir, "tool_result_input", body)
		},
		ActualModel: rb.Model,
	}

	approver := NewCliApprover(notifier)

	ui := tools.NewCLIToolUI(notifier, opts)

	return core.ToolContext{
		Ctx:             ctx,
		Cfg:             opts,
		Logger:          logger.From(ctx),
		Notifier:        notifier,
		Approver:        approver,
		ExecCfg:         execCfg,
		Model:           rb.Model,
		ToolDefs:        rb.ToolDefs,
		MessagesFn:      messageBuilder,
		UI:              ui,
		InitialResponse: *response,
		InitialText:     response.Text,
		InitialCalls:    response.ToolCalls,
		InitialStreamed: response.Streamed,
	}
}

func createToolStoreAndMessageBuilder(ctx context.Context, opts core.RequestOptions, sess *session.Session, inputStr string) (core.SessionStore, func(string) ([]core.TypedMessage, error)) {
	if sess != nil {
		return session.NewStore(sess, logger.From(ctx)), createMessageBuilder(ctx, sess)
	}

	store := core.NewMemorySessionStore(opts.GetEffectiveSystem(), inputStr)
	return store, store.Messages
}

func createMessageBuilder(ctx context.Context, sess *session.Session) func(string) ([]core.TypedMessage, error) {
	cached, err := session.CreateCachedMessageBuilder(ctx, sess.Path)
	if err == nil {
		return cached
	}
	logger.From(ctx).Warnf("Failed to create cached message builder: %v", err)
	return func(path string) ([]core.TypedMessage, error) {
		return session.BuildMessagesWithToolInteractions(ctx, path)
	}
}

func finishToolExecution(result core.ToolExecutionResult) error {
	if result.Error != nil {
		return result.Error
	}

	if result.FinalText != "" && !result.FinalStreamed {
		fmt.Print(result.FinalText)
	}

	return nil
}

func handleNormalResponse(ctx context.Context, cfg *config.Config, notifier core.Notifier, response *core.Response, sess *session.Session, model string) error {
	if err := persistAssistantOnly(ctx, *response, sess, cfg, notifier, model); err != nil {
		return err
	}

	if response.Text != "" && !response.Streamed {
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
	opts := cfg.RequestOptions()

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

	if cfg.PrintCurl {
		plan, err := prepareSessionRequestPlan(ctx, &cfg, opts, notifier, inputStr, isRegeneration, session.PendingToolPreview)
		if err != nil {
			return err
		}
		hasPendingTools := plan != nil && plan.HasPendingTools
		if !isRegeneration && inputStr == "" && !hasPendingTools {
			return errors.WrapError("validate input", stdErrors.New("input cannot be empty"))
		}
		rb, err := buildHTTPRequest(ctx, &cfg, opts, plan, inputStr)
		if err != nil {
			return err
		}
		fmt.Println(renderCurlCommand(rb.Request, rb.Body))
		return nil
	}

	// Prepare session using coordinator
	var plan *session.RequestPlan
	var hasPendingTools bool

	plan, err = prepareSessionRequestPlan(ctx, &cfg, opts, notifier, inputStr, isRegeneration, session.PendingToolExecute)
	if err != nil {
		return err
	}
	if plan != nil {
		hasPendingTools = plan.HasPendingTools
	}

	// Only require input if not regenerating and not continuing tool execution
	if !isRegeneration && inputStr == "" && !hasPendingTools {
		return errors.WrapError("validate input", stdErrors.New("input cannot be empty"))
	}

	return executeRequest(ctx, &cfg, opts, notifier, logDir, inputStr, plan)
}

func prepareSessionRequestPlan(ctx context.Context, cfg *config.Config, opts core.RequestOptions, notifier core.Notifier, inputStr string, isRegeneration bool, pendingToolMode session.PendingToolMode) (*session.RequestPlan, error) {
	if cfg.NoSession {
		return nil, nil
	}
	coordinator := session.NewCoordinator(opts, notifier)
	approver := NewCliApprover(notifier)
	return coordinator.PrepareRequest(ctx, inputStr, isRegeneration, approver, session.PrepareRequestOptions{
		PendingTools: pendingToolMode,
	})
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
		if cfg.PrintCurl {
			req, err := buildListModelsRequest(*cfg)
			if err != nil {
				return true, err
			}
			fmt.Println(renderCurlCommand(req, nil))
			return true, nil
		}
		return true, listModels(ctx, *cfg, logDir)
	}

	return false, nil
}

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

// buildHTTPRequest builds the HTTP request based on configuration.
func buildHTTPRequest(ctx context.Context, cfg *config.Config, opts core.RequestOptions, plan *session.RequestPlan, inputStr string) (core.RequestBuild, error) {
	var req *http.Request
	var body []byte
	var model string
	var toolDefs []core.ToolDefinition
	var err error

	// Log request start
	actualModel := actualModelForConfig(cfg, opts)
	logger.From(ctx).Infof("Starting request | Model: %s | Provider: %s", actualModel, cfg.Provider)

	if plan != nil {
		toolDefs = toolDefinitionsForOptions(opts)
		req, body, err = core.BuildChatRequest(opts, plan.Messages, core.ChatBuildOptions{
			ToolDefs: toolDefs,
			Stream:   opts.IsStreamChat(),
		})
		if err != nil {
			return core.RequestBuild{}, errors.WrapError("build request", err)
		}
		model = actualModel
	} else {
		req, body, err = core.BuildRequest(opts, inputStr)
		if err == nil {
			model = actualModel
			toolDefs = toolDefinitionsForOptions(opts)
		}
	}

	if err != nil {
		return core.RequestBuild{}, errors.WrapError("build request", err)
	}

	return core.RequestBuild{
		Request:  req,
		Body:     body,
		Model:    model,
		ToolDefs: toolDefs,
	}, nil
}

func actualModelForConfig(cfg *config.Config, opts core.RequestOptions) string {
	actualModel := cfg.Model
	if actualModel != "" {
		return actualModel
	}
	if cfg.Embed {
		return core.DefaultEmbedModel
	}
	provider := providers.ResolveProvider(opts.Provider)
	return core.GetDefaultChatModel(provider)
}

func toolDefinitionsForOptions(opts core.RequestOptions) []core.ToolDefinition {
	if opts.IsToolEnabled() {
		return core.GetBuiltinUniversalCommandTool()
	}
	return nil
}

func logBuiltHTTPRequest(ctx context.Context, cfg *config.Config, logDir string, notifier core.Notifier, req *http.Request, body []byte) {
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
	logWireHTTPRequest(ctx, req, body)
}

func renderCurlCommand(req *http.Request, body []byte) string {
	parts := []string{"curl", "-X", req.Method}

	headerNames := make([]string, 0, len(req.Header))
	for name := range req.Header {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)
	for _, name := range headerNames {
		values := append([]string(nil), req.Header.Values(name)...)
		for _, value := range values {
			parts = append(parts, "-H", name+": "+value)
		}
	}

	if len(body) > 0 {
		parts = append(parts, "--data-binary", "@-")
	}
	parts = append(parts, req.URL.String())

	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, shellQuote(part))
	}
	command := strings.Join(quoted, " ")
	if len(body) == 0 {
		return command
	}
	delimiter := heredocDelimiter(body)
	return command + " <<'" + delimiter + "'\n" + string(body) + "\n" + delimiter
}

func heredocDelimiter(body []byte) string {
	for i := 0; ; i++ {
		delimiter := "EOJ"
		if i > 0 {
			delimiter = fmt.Sprintf("EOJ_%d", i)
		}
		if !bytes.Contains(append(append([]byte{'\n'}, body...), '\n'), []byte("\n"+delimiter+"\n")) {
			return delimiter
		}
	}
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if strings.IndexFunc(value, func(r rune) bool {
		return !isShellBarewordRune(r)
	}) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func isShellBarewordRune(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		strings.ContainsRune("@%_+=:,./-", r)
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
	logger.From(ctx).Debugf("WIRE BACKEND REQUEST URL:\n%s", req.URL.String())

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
	logWireHTTPResponseHeaders(ctx, resp)

	return resp, nil
}

func logWireHTTPRequest(ctx context.Context, req *http.Request, body []byte) {
	log := logger.From(ctx)
	if !log.IsDebugEnabled() || req == nil {
		return
	}

	clone := req.Clone(ctx)
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	clone.ContentLength = int64(len(body))

	dump, err := httputil.DumpRequestOut(clone, true)
	if err != nil {
		log.Debugf("WIRE BACKEND REQUEST dump failed: %v", err)
		return
	}
	log.Debugf("WIRE BACKEND REQUEST:\n%s", string(dump))
}

func logWireHTTPResponseHeaders(ctx context.Context, resp *http.Response) {
	log := logger.From(ctx)
	if !log.IsDebugEnabled() || resp == nil {
		return
	}

	var buf bytes.Buffer
	proto := resp.Proto
	if proto == "" {
		proto = "HTTP/1.1"
	}
	status := resp.Status
	if status == "" {
		status = fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	fmt.Fprintf(&buf, "%s %s\r\n", proto, status)
	_ = resp.Header.Write(&buf)
	buf.WriteString("\r\n")
	log.Debugf("WIRE BACKEND RESPONSE HEADERS:\n%s", buf.String())
}

// persistAssistantOnly saves assistant response when there are no tool calls
func persistAssistantOnly(ctx context.Context, response core.Response, sess *session.Session, cfg *config.Config, notifier core.Notifier, model string) error {
	// Save assistant response to session if enabled (but NOT when there are tool calls - HandleToolExecution will do it)
	if sess != nil && (response.Text != "" || response.ThoughtSignature != "" || len(response.Blocks) > 0) {
		logger.From(ctx).Debugf("Saving assistant response to session | Length: %d | Streaming: %v", len(response.Text), cfg.StreamChat)
		store := session.NewStore(sess, logger.From(ctx))
		previousPath := sess.Path
		path, messageID, err := store.SaveAssistantResponse(ctx, response, model)
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
	provider := providers.ResolveProvider(cfg.Provider)
	req, err := buildListModelsRequest(cfg)
	if err != nil {
		return err
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

func buildListModelsRequest(cfg config.Config) (*http.Request, error) {
	provider := cfg.Provider
	provider = providers.ResolveProvider(provider)

	url, err := providers.ResolveModelsURLWithArgoOptions(provider, cfg.ProviderURL, cfg.ArgoEnv, cfg.ArgoLegacy)
	if err != nil {
		return nil, errors.WrapError("validate provider", err)
	}

	// Create HTTP request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, errors.WrapError("create request", err)
	}

	// Add authentication headers if needed
	if providers.RequiresAPIKey(provider) && cfg.ProviderURL == "" {
		// Standard non-Argo providers need API key
		if cfg.APIKeyFile == "" {
			return nil, errors.WrapError("validate config", fmt.Errorf("-api-key-file is required for %s provider when listing models", provider))
		}

		apiKey, err := auth.ReadKeyFile(cfg.APIKeyFile)
		if err != nil {
			return nil, errors.WrapError("read API key", err)
		}

		if err := auth.ApplyProviderCredentials(req, provider, apiKey); err != nil {
			if provider == constants.ProviderGoogle {
				return nil, errors.WrapError("apply Google API key", err)
			}
			return nil, errors.WrapError("apply provider credentials", err)
		}
	}

	return req, nil
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
