package main

import (
	"bytes"
	"context"
	"encoding/json"
	stdErrors "errors"
	"flag"
	"fmt"
	"io"
	"lmtools/internal/config"
	"lmtools/internal/constants"
	"lmtools/internal/core"
	"lmtools/internal/errors"
	"lmtools/internal/limitio"
	"lmtools/internal/logger"
	"lmtools/internal/mcp"
	"lmtools/internal/modelcatalog"
	"lmtools/internal/providerrequest"
	"lmtools/internal/providers"
	"lmtools/internal/retry"
	"lmtools/internal/session"
	"lmtools/internal/ui"
	"lmtools/internal/version"
	"lmtools/internal/workingcontext"
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

// executeRequest sends a planned turn and handles the response: the plan
// commits once the response parses, then either the tool loop or the answer
// save writes the rest. The outcome records how far the writes got on every
// exit.
func executeRequest(ctx context.Context, env *turnEnv, opts core.RequestOptions, inputStr string, plan *session.Plan) (out turnOutcome, err error) {
	ctx = logger.WithNewRequestCounter(ctx)
	start := time.Now()
	defer func() { logRequestEnd(ctx, start, err) }()
	if env.progress != nil {
		ctx = retry.WithProgress(ctx, env.progress.report)
		defer env.progress.stop()
	}

	rb, err := buildTurnRequest(ctx, env, opts, plan, inputStr)
	if err != nil {
		return out, err
	}
	logBuiltHTTPRequest(ctx, env.cfg, env.logDir, env.notifier, rb.Request, rb.Body, getOperationName(env.cfg))

	resp, err := sendWithRetry(ctx, newRetryClient(ctx, env.cfg), rb.Request)
	if err != nil {
		return out, err
	}
	presenter := newResponsePresenter(env.stdout, env.stderr, env.cfg.ShowThinking)
	presenter.separate = env.answersOnScreen
	presenter.answerLabel = env.answerLabel
	if env.quoteThinking {
		presenter.quoteThinking(env.columns)
	}
	presenter.notifier = env.notifier
	presenter.hiddenThinkingNoted = &env.hiddenThinkingNoted
	if env.progress != nil {
		presenter.progress = env.progress
	}
	defer presenter.Close()
	defer func() { out.PartialAnswer = presenter.lastIncomplete }()

	response, err := core.HandleResponseWithOptions(ctx, opts, resp, logger.From(ctx), env.notifier, core.ResponseParseOptions{
		ArgoLegacy: opts.ArgoLegacy,
		ToolDefs:   rb.ToolDefs,
		Output:     presenter,
	})
	if err != nil {
		return out, err
	}
	if response.Model != "" {
		env.served = response.Model
	}

	if plan != nil {
		// The response has arrived, so its turn is recorded even if the run
		// is being cancelled.
		commitCtx, cancel := core.PersistenceContext(ctx)
		commit := plan.Commit
		if commitForTest != nil {
			commit = func(ctx context.Context) (*session.Session, error) { return commitForTest(ctx, plan) }
		}
		committed, err := commit(commitCtx)
		cancel()
		announceOldSessions(env, plan)
		if committed != nil && (err == nil || committed.Head != plan.Start().Message) {
			// Set on a failure too when the commit saved part of its
			// batch: it returns the session at the last message it saved.
			out.Session = committed
		}
		if err != nil {
			err = errors.WrapError("commit session", err)
			if core.ResponseHasContent(response) {
				// The response was presented as it arrived, so the operator
				// saw an answer the transcript does not hold.
				out.UnsavedAnswer = err
			}
			return out, err
		}
		out.PlanCommitted = true
		announceCommit(env, plan, committed)
		if afterTurnCommitForTest != nil {
			afterTurnCommitForTest(committed)
		}
	}

	if err := core.ValidateResponse(opts, response); err != nil {
		out.UnsavedAnswer = persistAssistantOnly(ctx, response, out.Session, env.cfg, env.notifier, rb.Model)
		return out, err
	}
	if len(response.ToolCalls) > 0 {
		logger.From(ctx).Infof("Handling tool execution with %d tool calls", len(response.ToolCalls))
		tc, err := newToolContext(ctx, env, opts, out.Session, &response, rb, inputStr, presenter)
		if err != nil {
			if out.Session == nil {
				return out, err
			}
			log := logger.From(ctx)
			result := core.PersistUnstartedResponse(ctx, response, core.ToolExecutionConfig{
				Store: out.Session.ToolStore(), Journal: env.store.Journal(), ActualModel: rb.Model,
			}, log, err)
			out.UnsavedAnswer = result.UnsavedAnswer
			return out, result.Error
		}
		result := core.HandleToolExecution(tc)
		out.UnsavedAnswer = result.UnsavedAnswer
		return out, finishToolExecution(result)
	}

	out.UnsavedAnswer = persistAssistantOnly(ctx, response, out.Session, env.cfg, env.notifier, rb.Model)
	return out, nil
}

// commitForTest, when set, commits a turn's plan in place of Plan.Commit.
var commitForTest func(context.Context, *session.Plan) (*session.Session, error)

// announceOldSessions tells, after the commit that made the first session
// in a directory of old sessions, whether it succeeded or not, how many old
// sessions the directory holds and how to remove them.
func announceOldSessions(env *turnEnv, plan *session.Plan) {
	if plan.OldSessions > 0 {
		env.notifier.Warnf("%s holds %s written by an older lmc, which this version cannot read. -show-sessions lists them; move the numbered directories out of %s, or delete them with -delete.", env.store.Dir(), core.Plural(plan.OldSessions, "session"), env.store.Dir())
	}
}

// announceCommit names the settings a commit changed in a resumed
// conversation and the message they apply from.
func announceCommit(env *turnEnv, plan *session.Plan, sess *session.Session) {
	if plan.Start().Session != "" && len(plan.Changed) > 0 && plan.SettingsRecord != "" {
		env.notifier.Infof("Changed the %s from message %s on.", joinNames(plan.Changed), sess.Ref().At(plan.SettingsRecord))
	}
}

// joinNames lists names in a sentence: a, b and c.
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func newToolContext(ctx context.Context, env *turnEnv, opts core.RequestOptions, sess *session.Session, response *core.Response, rb core.RequestBuild, inputStr string, output core.ResponseOutput) (core.ToolContext, error) {
	store, messageBuilder, err := contextToolStore(ctx, env, opts, sess, inputStr)
	if err != nil {
		return core.ToolContext{}, err
	}

	retryClient := newRetryClient(ctx, env.cfg)

	execCfg := core.ToolExecutionConfig{
		Store: store,
		Send: func(ctx context.Context, req *http.Request, body []byte) (*http.Response, error) {
			logBuiltHTTPRequest(ctx, env.cfg, env.logDir, env.notifier, req, body, "tool_result_input")
			return sendWithRetry(ctx, retryClient, req)
		},
		ActualModel: rb.Model,
	}
	if sess != nil {
		execCfg.Journal = env.store.Journal()
	}

	return core.ToolContext{
		Ctx:             ctx,
		Cfg:             opts,
		Logger:          logger.From(ctx),
		Notifier:        env.notifier,
		Approver:        env.approver,
		ExecCfg:         execCfg,
		Model:           rb.Model,
		ToolDefs:        rb.ToolDefs,
		MessagesFn:      messageBuilder,
		UI:              env.toolUI,
		Output:          output,
		InitialResponse: *response,
	}, nil
}

// createToolStoreAndMessageBuilder returns where a tool loop saves its rounds
// and how it builds each follow-up request: through the session's head,
// which every save moves, or in memory with -no-session.
func createToolStoreAndMessageBuilder(opts core.RequestOptions, sess *session.Session, inputStr string) (core.SessionStore, func(string) ([]core.TypedMessage, error)) {
	if sess != nil {
		return sess.ToolStore(), func(string) ([]core.TypedMessage, error) {
			view, err := sess.View(opts)
			return view.Messages, err
		}
	}

	store := core.NewMemorySessionStoreForRequest(opts, inputStr)
	return store, store.Messages
}

func finishToolExecution(result core.ToolExecutionResult) error {
	return result.Error
}

func main() {
	// Create a notifier instance for error reporting
	notifier := ui.NewNotifier()

	if err := run(notifier); err != nil {
		_ = reportError(notifier, err)
		os.Exit(getExitCode(err))
	}
}

// reportedError keeps the cause available for exit-code selection while the
// layer that owns the diagnostic stream takes responsibility for displaying it.
type reportedError struct{ error }

func (e reportedError) Unwrap() error { return e.error }

func reportError(notifier core.Notifier, err error) error {
	var reported reportedError
	if err == nil || stdErrors.As(err, &reported) {
		return err
	}
	notifier.Errorf("%v", err)
	return reportedError{err}
}

func run(notifier core.Notifier) error {
	cfg, err := config.ParseFlags(os.Args[1:])
	if stdErrors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return errors.WrapError("parse flags", err)
	}
	if cfg.Version {
		fmt.Println(version.String("lmc"))
		return nil
	}

	// A single run ends at its first interrupt. A loop outlives one: it
	// keeps its signal handler across turns, and until the first prompt the
	// startup phase is what an interrupt ends.
	var (
		ctx        context.Context
		signals    *replSignals
		endStartup func()
	)
	if cfg.REPL {
		signals = watchREPLSignals()
		defer signals.stop()
		ctx, endStartup = signals.begin()
		defer endStartup()
	} else {
		var cancel context.CancelFunc
		ctx, cancel = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
	}
	opts := cfg.RequestOptions()

	warnUnusedReasoningControls(cfg, notifier)
	if note := argoChatHidesThinking(opts); cfg.ShowThinking && note != "" {
		notifier.Warnf("%s", note)
	}

	// Initialize logging
	logDir, err := setupLogging(cfg, notifier)
	if err != nil {
		return err
	}
	defer logger.Close()

	if cfg.SessionsDir != "" {
		logger.From(ctx).Infof("Using custom sessions directory: %s", cfg.SessionsDir)
	}

	// Handle special flags that don't require full processing
	if handled, err := handleSpecialFlags(ctx, &cfg, notifier, logDir); handled {
		return err
	}

	// A run saves its session once the provider has answered, so a
	// sessions directory that cannot hold sessions is refused before then.
	if !cfg.NoSession {
		if err := cfg.Store().Check(); err != nil {
			return err
		}
	}
	var lineage []session.Message
	if !cfg.NoSession && cfg.Start.Message != "" {
		if lineage, err = cfg.Store().Lineage(cfg.Start); err != nil {
			return err
		}
	}
	if err := resolveWindow(ctx, &cfg, &opts, notifier, logDir, lineage); err != nil {
		return err
	}

	// One reader owns standard input from here on: the prompt, and the
	// answers to any question a tool call asks.
	input := newInputOwner(os.Stdin)
	defer func() { _ = input.close() }()
	if input.terminal && input.termErr != nil {
		if cfg.REPL {
			// A blocking read of standard input cannot be ended, and a
			// loop reads again after every question and every turn.
			return errors.WrapError("-repl needs its own open of the terminal on standard input", input.termErr)
		}
		logger.From(ctx).Debugf("Reading the terminal on standard input as a stream: %v", input.termErr)
	}
	if cfg.REPL {
		return runREPL(ctx, endStartup, signals, &cfg, opts, input, logDir)
	}

	// Read and validate input
	inputStr, err := readAndValidateInput(ctx, input)
	if err != nil {
		return err
	}

	host, err := connectMCPServers(ctx, &cfg, &opts, notifier, false)
	if err != nil {
		return err
	}
	if host != nil {
		defer host.Close()
	}

	env := newTurnEnv(&cfg, notifier, logDir, input)
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	if approver, ok := env.approver.(*cliApprover); ok {
		approver.stop = stop
	}
	out, err := runTurn(ctx, env, opts, turnInput{text: inputStr})
	reportSaved(notifier, out, err, inputStr != "" || len(opts.Images) > 0)
	if cause := context.Cause(ctx); stdErrors.Is(cause, errEditorTerminal) {
		return cause
	}
	return err
}

// reportSaved says where a single run's writes ended and, when the run
// failed after a write, how to continue: a commit that stopped part way says
// so in its error; a prompt that was not saved, after the results of
// pending calls were, is sent again from there; and a saved question, tool
// results or pending calls without an answer are answered by resuming there
// with empty input. prompted reports that the run had a prompt to save.
func reportSaved(notifier core.Notifier, out turnOutcome, err error, prompted bool) {
	if out.Session == nil {
		return
	}
	var partial *session.SaveError
	switch {
	case stdErrors.As(err, &partial):
	case err == nil && out.UnsavedAnswer == nil:
		notifier.Infof("Saved at %s.", out.Session.Ref())
	case prompted && !out.PlanCommitted:
		notifier.Infof("Saved through %s; resume there with the same prompt to retry.", describeHead(out.Session))
	default:
		if awaits, _ := out.Session.AwaitsAnswer(); awaits {
			notifier.Infof("Saved through %s; resume there with empty input to get its answer.", describeHead(out.Session))
		} else {
			notifier.Infof("Saved at %s.", out.Session.Ref())
		}
	}
}

// describeHead names the message a session's head is at and what it holds,
// such as "0001/000e, your question".
func describeHead(sess *session.Session) string {
	ref := sess.Ref()
	if ref.Message == "" {
		return ref.String()
	}
	m, err := sess.Store.Message(ref)
	if err != nil {
		return ref.String()
	}
	c, err := sess.Store.Content(ref)
	if err != nil {
		return ref.String()
	}
	kind := session.Kind(m, c)
	if kind == "user input" {
		kind = "your question"
	}
	return ref.String() + ", " + kind
}

// warnUnusedReasoningControls warns when reasoning.mode/context are set but the
// effective request does not use the Responses API. These are Responses-only
// controls; on any other path they are silently omitted downstream, so surface
// a warning rather than dropping them without notice.
func warnUnusedReasoningControls(cfg config.Config, notifier core.Notifier) {
	if cfg.ReasoningMode == "" && cfg.ReasoningContext == "" {
		return
	}
	if providers.UsesResponsesAPIWire(cfg.Provider, cfg.Model, cfg.OpenAIResponses, cfg.ArgoLegacy) {
		return
	}
	notifier.Warnf("-reasoning-mode/-reasoning-context are Responses API controls; ignoring them because the request does not use the Responses API, which OpenAI and Argo gpt* models use unless -chat-completions is given")
}

// argoChatHidesThinking explains why a gpt model on Argo's chat completions
// route, which -chat-completions selects, answers without thinking, and
// returns "" for any other route. OpenAI's Chat Completions API returns no
// reasoning text and Argo adds none; its Responses route returns a summary
// when the request asks for one, which an -effort does there.
func argoChatHidesThinking(opts core.RequestOptions) string {
	model := core.EffectiveChatModel(opts)
	if opts.Embed || opts.ArgoLegacy || constants.NormalizeProvider(opts.Provider) != constants.ProviderArgo ||
		!providers.ArgoModelSupportsResponses(model) ||
		providers.UsesResponsesAPIWire(opts.Provider, model, opts.OpenAIResponses, opts.ArgoLegacy) {
		return ""
	}
	return fmt.Sprintf("Argo's chat completions route returns no thinking for %s; without -chat-completions, the Responses API returns reasoning summaries with -effort", model)
}

// connectMCPServers starts the servers -mcp-config names and attaches their
// tools to the run: advertised beside the built-in ones, called through the
// executor, and described in the system prompt. It runs after the prompt
// is read and before the session plan, which needs the effective system
// prompt the tools extend. The host is nil when no server is configured.
func connectMCPServers(ctx context.Context, cfg *config.Config, opts *core.RequestOptions, notifier core.Notifier, announce bool) (*mcp.Host, error) {
	if len(cfg.MCPServers) == 0 {
		return nil, nil
	}
	for _, warning := range cfg.MCPWarnings {
		notifier.Warnf("-mcp-config: %s", warning)
	}
	var remotes []mcp.ServerConfig
	var local *mcp.ServerConfig
	for _, s := range cfg.MCPServers {
		if s.Type == "local" {
			v := s
			local = &v
			continue
		}
		if cfg.Context.Notebook != "" && s.Name == "notebook" {
			path, err := config.ResolveNotebookExecutable()
			if err != nil {
				return nil, fmt.Errorf("locate notebook executable: %w", err)
			}
			s.Command = path
		}
		remotes = append(remotes, s)
	}
	if announce && len(remotes) > 0 {
		notifier.Infof("Connecting %d MCP server(s)...", len(remotes))
	}
	host, err := mcp.Connect(ctx, remotes, mcp.HostOptions{
		ClientInfo:     mcp.DefaultClientInfo,
		Log:            logger.From(ctx).Debugf,
		Warn:           notifier.Warnf,
		CallTimeout:    opts.GetToolTimeout(),
		StartupTimeout: mcp.DefaultStartupTimeout,
	})
	if err != nil {
		return nil, errors.WrapError("connect MCP servers", err)
	}
	for _, server := range host.Servers() {
		logger.From(ctx).Infof("MCP server %s | Transport: %s | Protocol: %s (%s era) | Tools: %d",
			server.Name, server.Transport, server.Protocol, server.Era, server.ToolCount)
	}
	var services core.MCPTools = host
	if cfg.Context.Enabled() {
		services = workingcontext.NewTools(host, local, cfg.Context.Settings, opts.GetToolTimeout(), notifier.Warnf)
	}
	core.ApplyMCP(opts, services)
	return host, nil
}

// handleSpecialFlags handles flags that don't require the full request processing
func handleSpecialFlags(ctx context.Context, cfg *config.Config, notifier core.Notifier, logDir string) (bool, error) {
	store := cfg.Store()
	if cfg.ShowSessions {
		return true, store.List(os.Stdout)
	}
	if cfg.Show != "" {
		at, err := resolveShown(store, cfg.Show)
		if err != nil {
			return true, err
		}
		return true, store.Show(ctx, os.Stdout, at, session.ShowOptions{})
	}
	if cfg.Delete != "" {
		at, err := session.ParseRef(cfg.Delete)
		if err != nil {
			return true, err
		}
		if err := store.Delete(ctx, at); err != nil {
			return true, err
		}
		if at.Message == "" {
			notifier.Infof("Deleted session %s.", at)
		} else {
			notifier.Infof("Deleted message %s and every message after it.", at)
		}
		return true, nil
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

// resolveShown resolves a -show reference: a message, or a session's newest
// message.
func resolveShown(store *session.Store, text string) (session.Ref, error) {
	at, err := session.ParseRef(text)
	if err != nil {
		return at, err
	}
	if at, err = store.Locate(at); err != nil {
		return at, err
	}
	if at.Message == "" {
		at.Message, err = store.Newest(at.Session)
	}
	return at, err
}

// readAndValidateInput reads the prompt of a single run, all of standard
// input up to its end. On a terminal, Ctrl-C ends the read.
func readAndValidateInput(ctx context.Context, input *inputOwner) (string, error) {
	// Read stdin with size limit to prevent DoS
	inputBytes, err := input.readAll(ctx, constants.MaxCLIInputSize)
	if err != nil {
		return "", errors.WrapError("read stdin", err)
	}
	return strings.TrimSpace(string(inputBytes)), nil
}

// buildHTTPRequest builds the HTTP request of a turn from its messages, or
// from the prompt alone when it has none.
func buildHTTPRequest(ctx context.Context, cfg *config.Config, opts core.RequestOptions, messages []core.TypedMessage, inputStr string) (core.RequestBuild, error) {
	var req *http.Request
	var body []byte
	var model string
	var toolDefs []core.ToolDefinition
	var err error

	// Log request start
	actualModel := actualModelForConfig(cfg, opts)
	logger.From(ctx).Infof("Starting request | Model: %s | Provider: %s", actualModel, cfg.Provider)

	if messages != nil {
		toolDefs = toolDefinitionsForOptions(opts)
		req, body, err = core.BuildChatRequest(opts, messages, core.ChatBuildOptions{
			ToolDefs: toolDefs,
			Stream:   opts.StreamChat,
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
	// The turn's options name the model it is sent with, which the REPL's
	// /model can change after the flags were read.
	if opts.Model != "" {
		return opts.Model
	}
	actualModel := cfg.Model
	if actualModel != "" {
		return actualModel
	}
	if cfg.Embed {
		return core.DefaultEmbedModel
	}
	provider := providers.ResolveProvider(opts.Provider)
	return providers.DefaultChatModel(provider)
}

func toolDefinitionsForOptions(opts core.RequestOptions) []core.ToolDefinition {
	return core.AdvertisedTools(opts)
}

// logBuiltHTTPRequest logs a request of the turn before it is sent, the
// first or a tool round's: a summary, its body to a file named for opName,
// and at DEBUG the request whole.
func logBuiltHTTPRequest(ctx context.Context, cfg *config.Config, logDir string, notifier core.Notifier, req *http.Request, body []byte, opName string) {
	// Log request details
	var requestData map[string]interface{}
	if err := json.Unmarshal(body, &requestData); err == nil {
		messages, _ := requestData["messages"].([]interface{})
		tools, _ := requestData["tools"].([]interface{})
		logger.From(ctx).Infof("Request built | Messages: %d | Tools: %d | Stream: %v",
			len(messages), len(tools), cfg.StreamChat)
	}

	// Log request
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

// logRequestEnd records in the file log the error a turn's request ended
// with: sending it, reading its response, or a tool round after it. The
// caller reports the error on its diagnostic stream, and the file keeps it
// beside the attempts that came before. A cancelled request is the
// operator's doing, noted at info level.
func logRequestEnd(ctx context.Context, start time.Time, err error) {
	switch {
	case err == nil:
	case ctx.Err() != nil:
		logger.From(ctx).Infof("Request cancelled after %v: %v", time.Since(start), err)
	default:
		logger.From(ctx).FileErrorf("Request failed after %v: %v", time.Since(start), err)
	}
}

// retryLogger keeps the retry loop's per-attempt failures off the terminal.
// The retry is already on screen as a status, and the final failure is
// reported once by the caller; the record keeps its ERROR severity in the
// file log, where it explains the attempts that came before the answer.
type retryLogger struct{ *logger.RequestLogger }

func (l retryLogger) Errorf(format string, args ...interface{}) {
	l.FileErrorf(format, args...)
}

// newRetryClient builds the client for a request and its tool rounds. It
// retries a failure that a stream reports before any content, such as
// Argo's "Streaming response failed", as it retries a failed status.
func newRetryClient(ctx context.Context, cfg *config.Config) *retry.Client {
	client := retry.NewClientWithRetries(cfg.Timeout, cfg.Retries, retryLogger{logger.From(ctx)})
	client.RetryStreamStartErrors()
	return client
}

// sendWithRetry sends a request of the turn, the first or a tool round's,
// through retryClient, and logs it going and the response coming back.
func sendWithRetry(ctx context.Context, retryClient *retry.Client, req *http.Request) (*http.Response, error) {
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
	// Handle error with response cleanup; executeRequest logs it
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
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

// persistAssistantOnly saves an answer that carried no tool calls; the tool
// loop saves the answers it handles. A failed save is warned about and does
// not fail the request, as it never has, and is returned so the turn outcome
// can record that the transcript lacks an answer that was already shown.
func persistAssistantOnly(ctx context.Context, response core.Response, sess *session.Session, cfg *config.Config, notifier core.Notifier, model string) error {
	if sess == nil || (response.Text == "" && response.ThoughtSignature == "" && len(response.Blocks) == 0) {
		logger.From(ctx).Debugf("Not saving response | Session: %v | Output length: %d", sess != nil, len(response.Text))
		return nil
	}

	logger.From(ctx).Debugf("Saving assistant response to session | Length: %d | Streaming: %v", len(response.Text), cfg.StreamChat)
	// The answer has been shown, so it is saved even if the run is being
	// cancelled.
	ctx, cancel := core.PersistenceContext(ctx)
	defer cancel()
	saved, err := sess.SaveReply(ctx, response, model)
	if err != nil {
		if !cfg.REPL {
			notifier.Warnf("Failed to save response to session: %v", err)
		}
		return err
	}
	logger.From(ctx).Debugf("Response saved as message %s", sess.Ref().At(saved.ID))
	return nil
}

// listModels queries and displays available models for the configured provider
func listModels(ctx context.Context, cfg config.Config, logDir string) error {
	items, err := fetchModelCatalog(ctx, cfg, logDir)
	if err != nil {
		return err
	}
	displayModels(os.Stdout, providers.ResolveProvider(cfg.Provider), items)
	return nil
}

func fetchModelCatalog(ctx context.Context, cfg config.Config, logDir string) ([]modelcatalog.Item, error) {
	provider := providers.ResolveProvider(cfg.Provider)
	req, err := buildListModelsRequest(cfg)
	if err != nil {
		return nil, err
	}

	// Make request
	client := &http.Client{Timeout: cfg.Timeout}
	logWireHTTPRequest(ctx, req, nil)
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, errors.WrapError("fetch models", err)
	}
	defer resp.Body.Close()
	logWireHTTPResponseHeaders(ctx, resp)
	core.LogWireReads(resp, logger.From(ctx))

	if resp.StatusCode != http.StatusOK {
		body, err := limitio.ReadLimited(resp.Body, constants.MaxErrorResponseSize)
		if err != nil {
			return nil, errors.WrapError("fetch models", fmt.Errorf("HTTP %d (failed to read body: %v)", resp.StatusCode, err))
		}
		return nil, errors.WrapError("fetch models", errors.NewHTTPError(resp.StatusCode, string(body)))
	}

	// Read response with size limit
	data, err := limitio.ReadLimited(resp.Body, constants.MaxCLIResponseSize)
	if err != nil {
		return nil, errors.WrapError("read response", err)
	}

	// Log the raw response for debugging
	if err := logger.From(ctx).LogJSON(logDir, "list_models_response", data); err != nil {
		logger.From(ctx).Warnf("Failed to log models response: %v", err)
	}

	items, err := modelcatalog.Parse(provider, data)
	if err != nil {
		return nil, errors.WrapError("parse models response", err)
	}

	return items, nil
}

func buildListModelsRequest(cfg config.Config) (*http.Request, error) {
	return providerrequest.BuildModelsRequest(providerrequest.ModelsRequestOptions{
		ProviderOptions:                 cfg.Options,
		RequireAPIKeyWithoutProviderURL: true,
	})
}

// displayModels lists the models, each followed by the features the
// provider describes, when it describes any.
func displayModels(w io.Writer, provider string, items []modelcatalog.Item) {
	modelcatalog.Sort(items)

	name := provider
	if info, ok := providers.InfoFor(provider); ok {
		name = info.DisplayName
	}

	fmt.Fprintf(w, "Available %s models:\n", name)
	for _, model := range items {
		if model.DisplayName != "" {
			fmt.Fprintf(w, "  %s (%s)\n", model.ID, model.DisplayName)
		} else {
			fmt.Fprintf(w, "  %s\n", model.ID)
		}
		if features := modelcatalog.Describe(model).String(); features != "" {
			fmt.Fprintf(w, "    %s\n", features)
		}
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
